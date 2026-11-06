package openai

import (
	"net/http"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestResponsesProtocol_ModelCooldownTerminalErrorShape verifies that an exhausted
// model_cooldown error emits a terminal Responses error event with type="usage_limit_reached"
// and resets_at properly populated.
func TestResponsesProtocol_ModelCooldownTerminalErrorShape(t *testing.T) {
	cooldownErr := coreauth.NewModelCooldownError("gemini-3.8-flash", "antigravity", 90*time.Second)

	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      cooldownErr,
	}

	payload, errBuild := buildResponsesWebsocketErrorPayload(errMsg)
	if errBuild != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload failed: %v", errBuild)
	}

	doc := ojson.NewDoc(payload)
	if got := doc.Get("type").String(); got != "error" {
		t.Fatalf("expected type 'error', got %s", got)
	}
	if got := doc.Get("status").Int(); got != 429 {
		t.Fatalf("expected status 429, got %d", got)
	}
	if got := doc.Get("error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("expected error.type 'usage_limit_reached', got %s", got)
	}
	if got := doc.Get("error.code").String(); got != "model_cooldown" {
		t.Fatalf("expected error.code 'model_cooldown', got %s", got)
	}
	resetsAt := doc.Get("error.resets_at").Int()
	nowUnix := time.Now().Unix()
	if resetsAt < nowUnix+85 || resetsAt > nowUnix+95 {
		t.Fatalf("resets_at out of expected bounds: %d (now=%d)", resetsAt, nowUnix)
	}

	// Verify headers in the payload
	if got := doc.Get("headers.Retry-After").String(); got != "90" {
		t.Fatalf("expected headers.Retry-After '90', got %s", got)
	}
	if got := doc.Get("headers.X-Codex-Primary-Used-Percent").String(); got != "100" {
		t.Fatalf("expected headers.X-Codex-Primary-Used-Percent '100', got %s", got)
	}
	if got := doc.Get("headers.X-Codex-Secondary-Used-Percent").String(); got != "" {
		t.Fatalf("unexpected second exhausted window, got %s", got)
	}
}

// TestResponsesProtocol_Transient429DoesNotInventReset verifies that capacity/transient 429 errors
// retain their original error type and do not invent resets_at.
func TestResponsesProtocol_Transient429DoesNotInventReset(t *testing.T) {
	transientErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      http.ErrHandlerTimeout, // generic transient error
	}

	payload, errBuild := buildResponsesWebsocketErrorPayload(transientErr)
	if errBuild != nil {
		t.Fatalf("buildResponsesWebsocketErrorPayload failed: %v", errBuild)
	}

	doc := ojson.NewDoc(payload)
	if got := doc.Get("error.type").String(); got == "usage_limit_reached" {
		t.Fatalf("transient error must not be classified as usage_limit_reached: %s", payload)
	}
	if doc.Get("error.resets_at").Exists() {
		t.Fatalf("transient error must not invent resets_at: %s", payload)
	}
}
