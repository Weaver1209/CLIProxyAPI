package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestForkCooldownRuntimeReceivesActualResetHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	cooldown := coreauth.NewModelCooldownError("gemini-test", "antigravity", time.Hour)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.WriteErrorResponse(ctx, &interfaces.ErrorMessage{StatusCode: 429, Error: cooldown})
	reset, err := strconv.ParseInt(recorder.Header().Get("X-Codex-Primary-Reset-At"), 10, 64)
	if err != nil || reset < time.Now().Add(59*time.Minute).Unix() {
		t.Fatalf("Codex cannot schedule resume without an actual reset header: %v", recorder.Header())
	}
	if recorder.Header().Get("X-Codex-Primary-Window-Minutes") != "300" {
		t.Fatal("missing primary window duration")
	}
	if recorder.Header().Get("X-Codex-Secondary-Used-Percent") != "" {
		t.Fatal("must not invent a second exhausted window")
	}
	if ojson.GetBytes(recorder.Body.Bytes(), "error.plan_type").Exists() {
		t.Fatal("must not label an Antigravity account as ChatGPT Plus")
	}
}

func TestForkCooldownWithoutResetRemainsUnknown(t *testing.T) {
	body := BuildErrorResponseBodyWithError(429, `{"error":{"code":"model_cooldown"}}`, nil)
	if ojson.GetBytes(body, "error.resets_at").Exists() {
		t.Fatal("missing reset must not become an invented 60 second reset")
	}
}
