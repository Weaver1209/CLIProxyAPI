package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupTestForkRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v8 := r.Group("/v8/management")
	registerForkHistoryRoutes(v8)
	return r
}

func TestForkHistoryRoutes503WhenClickHouseDisabled(t *testing.T) {
	router := setupTestForkRouter()

	req, _ := http.NewRequest(http.MethodGet, "/v8/management/fork/usage", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	// In test environment, CLIPROXY_CLICKHOUSE_URL is unset, so it must return 503
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %d (body: %s)", resp.Code, resp.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if body["error"] != "clickhouse_disabled" {
		t.Fatalf("expected error 'clickhouse_disabled', got %v", body["error"])
	}
}

func TestForkHistoryRoutesInvalidRange(t *testing.T) {
	router := setupTestForkRouter()

	now := time.Now().UTC()
	since := now.Format(time.RFC3339)
	until := now.Add(-1 * time.Hour).Format(time.RFC3339) // since is after until

	url := "/v8/management/fork/usage?since=" + since + "&until=" + until
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid range, got %d", resp.Code)
	}
}

func TestForkQuotaRoutesSnapshot(t *testing.T) {
	router := setupTestForkRouter()

	req, _ := http.NewRequest(http.MethodGet, "/v8/management/fork/quota?limit=10", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for quota snapshot, got %d", resp.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Verify required keys
	if _, ok := body["accounts"]; !ok {
		t.Fatalf("expected 'accounts' in quota response")
	}
	if _, ok := body["history"]; !ok {
		t.Fatalf("expected 'history' in quota response")
	}
	if _, ok := body["forecasts"]; !ok {
		t.Fatalf("expected 'forecasts' in quota response")
	}
}

func TestForkStatusRoute(t *testing.T) {
	router := setupTestForkRouter()

	req, _ := http.NewRequest(http.MethodGet, "/v8/management/fork/status", nil)
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for fork status, got %d", resp.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if _, ok := body["writer"]; !ok {
		t.Fatalf("expected 'writer' in status response")
	}
}
