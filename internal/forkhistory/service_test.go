package forkhistory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestServiceUncachedInputInvariantAndIdentityHashing(t *testing.T) {
	svc := &Service{
		pricing: NewPricingResolver(t.TempDir(), ""),
	}

	record := coreusage.Record{
		RequestID: "req-123",
		Provider:  "openai",
		Model:     "gpt-4o",
		APIKey:    "sk-proj-secret-key-12345",
		AuthIndex: "auth-idx-1",
		SessionID: "sess-abc-789",
		Detail: coreusage.Detail{
			InputTokens:     1000,
			CacheReadTokens: 400,
			OutputTokens:    200,
			TotalTokens:     1200,
		},
	}

	row := svc.ConvertRecordToRow(record)

	// Verify uncached input invariant: uncached_input = 1000 - 400 = 600
	if row.UncachedInputTokens != 600 {
		t.Fatalf("expected UncachedInputTokens = 600, got %d", row.UncachedInputTokens)
	}
	if row.CacheReadTokens != 400 {
		t.Fatalf("expected CacheReadTokens = 400, got %d", row.CacheReadTokens)
	}
	if row.InputTokens != 1000 {
		t.Fatalf("expected InputTokens = 1000, got %d", row.InputTokens)
	}

	// Invariant check: uncached + cacheRead == inputTokens
	if row.UncachedInputTokens+row.CacheReadTokens != row.InputTokens {
		t.Fatalf("invariant violated: %d + %d != %d", row.UncachedInputTokens, row.CacheReadTokens, row.InputTokens)
	}

	// Verify identity hashing: raw key or session must never appear in row
	if strings.Contains(row.AccountHash, "sk-proj-secret") {
		t.Fatalf("account hash leaked raw secret key: %s", row.AccountHash)
	}
	if strings.Contains(row.SessionHash, "sess-abc-789") {
		t.Fatalf("session hash leaked raw session ID: %s", row.SessionHash)
	}
	if len(row.AccountHash) != 64 {
		t.Fatalf("expected 64-char sha256 account hash, got len %d", len(row.AccountHash))
	}
	if len(row.SessionHash) != 64 {
		t.Fatalf("expected 64-char sha256 session hash, got len %d", len(row.SessionHash))
	}
}

func TestServiceRangeValidation(t *testing.T) {
	svc := &Service{
		writer: &BackgroundWriter{enabled: true},
	}

	now := time.Now().UTC()
	invalidFilter := RangeFilter{
		Since: now,
		Until: now.Add(-1 * time.Hour), // Since is AFTER until
	}

	_, err := svc.QueryAggregates(context.Background(), invalidFilter)
	if err != ErrInvalidRange {
		t.Fatalf("expected ErrInvalidRange, got %v", err)
	}
}

func TestServiceQueryAggregatesClickHouseResponse(t *testing.T) {
	mockCH := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		resp := map[string]any{
			"data": []map[string]any{
				{
					"hour_bucket":                 "2026-11-06 04:00:00",
					"provider":                    "anthropic",
					"model":                       "claude-3-7-sonnet",
					"credential_kind":             "oauth",
					"requests":                    "10",
					"input_tokens":                "50000",
					"uncached_input_tokens":       "30000",
					"cache_read_tokens":           "20000",
					"cache_creation_tokens":       "5000",
					"cache_creation_1h_tokens":    "0",
					"output_tokens":               "15000",
					"non_reasoning_output_tokens": "10000",
					"reasoning_tokens":            "5000",
					"total_tokens":                "65000",
					"total_cost_sum":              "0.315",
					"cache_savings_sum":           "0.054",
					"unknown_price_count":         "0",
				},
			},
		}
		_ = json.NewEncoder(rw).Encode(resp)
	}))
	defer mockCH.Close()

	svc := &Service{
		writer: &BackgroundWriter{
			clickhouseURL: mockCH.URL,
			enabled:       true,
			httpClient:    mockCH.Client(),
		},
	}

	filter := RangeFilter{
		Since: time.Now().Add(-2 * time.Hour),
		Until: time.Now(),
	}

	summary, err := svc.QueryAggregates(context.Background(), filter)
	if err != nil {
		t.Fatalf("unexpected query error: %v", err)
	}

	if summary.TotalRequests != 10 {
		t.Fatalf("expected 10 total requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 65000 {
		t.Fatalf("expected 65000 total tokens, got %d", summary.TotalTokens)
	}
	if summary.TotalCost == nil || *summary.TotalCost != 0.315 {
		t.Fatalf("expected total cost 0.315, got %v", summary.TotalCost)
	}
	if len(summary.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(summary.Buckets))
	}
}

func TestServiceQuotaSnapshotAndForecast(t *testing.T) {
	stateDir := t.TempDir()

	// 1. Write mock cpa.json
	cpaData := map[string]any{
		"schemaVersion": 1,
		"id":            "cpa",
		"updatedAt":     "2026-11-06T04:45:00Z",
		"ready":         true,
		"tierLabel":     "Pro",
		"accounts": []map[string]any{
			{
				"id":    "acc-hash-1",
				"label": "Antigravity (1)",
				"email": "u***@g***.com",
				"plan":  "Pro",
				"limits": []map[string]any{
					{
						"label":    "Session (5-hour)",
						"title":    "Session",
						"percent":  0.40,
						"resetsAt": time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339),
					},
				},
			},
		},
	}
	cpaBytes, _ := json.Marshal(cpaData)
	_ = os.WriteFile(filepath.Join(stateDir, "cpa.json"), cpaBytes, 0o600)

	// 2. Write mock cpa-history-2026-11.jsonl with 3 historical entries
	now := time.Now().UTC()
	hist1 := map[string]any{
		"timestamp":   now.Add(-30 * time.Minute).Format(time.RFC3339),
		"accountHash": "acc-hash-1",
		"limits":      []map[string]any{{"percent": 0.10}},
	}
	hist2 := map[string]any{
		"timestamp":   now.Add(-15 * time.Minute).Format(time.RFC3339),
		"accountHash": "acc-hash-1",
		"limits":      []map[string]any{{"percent": 0.25}},
	}
	hist3 := map[string]any{
		"timestamp":   now.Format(time.RFC3339),
		"accountHash": "acc-hash-1",
		"limits":      []map[string]any{{"percent": 0.40}},
	}

	hPath := filepath.Join(stateDir, "cpa-history-2026-11.jsonl")
	hf, _ := os.OpenFile(hPath, os.O_CREATE|os.O_WRONLY, 0o600)
	b1, _ := json.Marshal(hist1)
	b2, _ := json.Marshal(hist2)
	b3, _ := json.Marshal(hist3)
	_, _ = hf.Write(append(b1, '\n'))
	_, _ = hf.Write(append(b2, '\n'))
	_, _ = hf.Write(append(b3, '\n'))
	_ = hf.Close()

	svc := &Service{
		stateDir: stateDir,
	}

	resp, err := svc.ReadQuotaSnapshot(50, "")
	if err != nil {
		t.Fatalf("ReadQuotaSnapshot failed: %v", err)
	}

	if len(resp.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(resp.Accounts))
	}
	if len(resp.History) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(resp.History))
	}

	forecast, ok := resp.Forecasts["acc-hash-1"]
	if !ok {
		t.Fatalf("expected forecast for acc-hash-1")
	}

	if forecast.Status != "calculated" {
		t.Fatalf("expected calculated status, got %s (message: %s)", forecast.Status, forecast.Message)
	}
	if forecast.BurnRatePerSec == nil || *forecast.BurnRatePerSec <= 0 {
		t.Fatalf("expected positive burn rate")
	}
	if forecast.SecondsUntilExhaust == nil || *forecast.SecondsUntilExhaust <= 0 {
		t.Fatalf("expected positive seconds until exhaust")
	}
}

func TestServiceQuotaForecastInsufficientHistory(t *testing.T) {
	stateDir := t.TempDir()

	cpaData := map[string]any{
		"schemaVersion": 1,
		"id":            "cpa",
		"updatedAt":     "2026-11-06T04:45:00Z",
		"accounts": []map[string]any{
			{
				"id": "acc-hash-2",
				"limits": []map[string]any{
					{
						"title":    "Session",
						"percent":  0.05,
						"resetsAt": time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339),
					},
				},
			},
		},
	}
	cpaBytes, _ := json.Marshal(cpaData)
	_ = os.WriteFile(filepath.Join(stateDir, "cpa.json"), cpaBytes, 0o600)

	// Only 1 history entry (fewer than 3 points)
	hist1 := map[string]any{
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"accountHash": "acc-hash-2",
		"limits":      []map[string]any{{"percent": 0.05}},
	}
	hPath := filepath.Join(stateDir, "cpa-history-2026-11.jsonl")
	b1, _ := json.Marshal(hist1)
	_ = os.WriteFile(hPath, append(b1, '\n'), 0o600)

	svc := &Service{stateDir: stateDir}
	resp, err := svc.ReadQuotaSnapshot(50, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	forecast := resp.Forecasts["acc-hash-2"]
	if forecast.Status != "insufficient_history" {
		t.Fatalf("expected insufficient_history, got %s", forecast.Status)
	}
}
