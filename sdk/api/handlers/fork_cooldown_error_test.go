package handlers

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCheckModelCooldownPayload(t *testing.T) {
	cooldownErr := coreauth.NewModelCooldownError("gemini-3.8-flash", "antigravity", 120*time.Second)

	body, ok := CheckModelCooldownPayload(http.StatusTooManyRequests, "", cooldownErr)
	if !ok {
		t.Fatalf("expected CheckModelCooldownPayload to return true")
	}

	doc := ojson.NewDoc(body)
	if got := doc.Get("error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("expected error.type usage_limit_reached, got %s", got)
	}
	if got := doc.Get("error.code").String(); got != "model_cooldown" {
		t.Fatalf("expected error.code model_cooldown, got %s", got)
	}
	if got := doc.Get("error.resets_in_seconds").Int(); got != 120 {
		t.Fatalf("expected resets_in_seconds 120, got %d", got)
	}
	resetsAt := doc.Get("error.resets_at").Int()
	nowUnix := time.Now().Unix()
	if resetsAt < nowUnix+115 || resetsAt > nowUnix+125 {
		t.Fatalf("resets_at out of expected range: %d (now=%d)", resetsAt, nowUnix)
	}
}

func TestBuildErrorResponseBodyWithError_ModelCooldown(t *testing.T) {
	cooldownErr := coreauth.NewModelCooldownError("gemini-3.8-flash", "antigravity", 60*time.Second)
	body := BuildErrorResponseBodyWithError(http.StatusTooManyRequests, "", cooldownErr)

	doc := ojson.NewDoc(body)
	if got := doc.Get("error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("expected error.type usage_limit_reached, got %s", got)
	}
	if got := doc.Get("error.code").String(); got != "model_cooldown" {
		t.Fatalf("expected error.code model_cooldown, got %s", got)
	}
	if got := doc.Get("error.resets_at").Int(); got <= 0 {
		t.Fatalf("expected resets_at > 0, got %d", got)
	}
}

func TestBuildErrorResponseBodyWithError_Transient429(t *testing.T) {
	// A generic 429 without cooldown must stay rate_limit_exceeded and not invent reset
	transientErr := errors.New("rate limit reached")
	body := BuildErrorResponseBodyWithError(http.StatusTooManyRequests, "rate limit reached", transientErr)

	doc := ojson.NewDoc(body)
	if got := doc.Get("error.type").String(); got != "rate_limit_error" {
		t.Fatalf("expected error.type rate_limit_error, got %s", got)
	}
	if got := doc.Get("error.code").String(); got != "rate_limit_exceeded" {
		t.Fatalf("expected error.code rate_limit_exceeded, got %s", got)
	}
	if doc.Get("error.resets_at").Exists() {
		t.Fatalf("transient 429 must not invent resets_at")
	}
}

func TestBuildOpenAIResponsesStreamFailedChunk_ModelCooldown(t *testing.T) {
	cooldownErr := coreauth.NewModelCooldownError("gemini-3.8-flash", "antigravity", 45*time.Second)
	cooldownBody, _ := CheckModelCooldownPayload(http.StatusTooManyRequests, "", cooldownErr)

	chunk := BuildOpenAIResponsesStreamFailedChunk(http.StatusTooManyRequests, string(cooldownBody), 1)
	doc := ojson.NewDoc(chunk)

	if got := doc.Get("type").String(); got != "response.failed" {
		t.Fatalf("expected type response.failed, got %s", got)
	}
	if got := doc.Get("response.error.type").String(); got != "usage_limit_reached" {
		t.Fatalf("expected response.error.type usage_limit_reached, got %s", got)
	}
	if got := doc.Get("response.error.resets_at").Int(); got <= 0 {
		t.Fatalf("expected response.error.resets_at > 0, got %d", got)
	}
}
