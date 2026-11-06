package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestResetSoonestSelector_RealMetadata(t *testing.T) {
	now := time.Now()
	t1 := now.Add(2 * time.Hour).Format(time.RFC3339)
	t2 := now.Add(5 * time.Hour).Format(time.RFC3339)

	authA := &Auth{
		ID:       "auth-later",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"weekly_quota_reset_at": t2},
	}
	authB := &Auth{
		ID:       "auth-sooner",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"weekly_quota_reset_at": t1},
	}

	selector := &ResetSoonestSelector{Fallback: &RoundRobinSelector{}}
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	picked, err := selector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if picked.ID != "auth-sooner" {
		t.Fatalf("expected auth-sooner, got %s", picked.ID)
	}
}

func TestResetSoonestSelector_FallbackWhenAbsent(t *testing.T) {
	authA := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	authB := &Auth{
		ID:       "auth-2",
		Provider: "antigravity",
		Status:   StatusActive,
	}

	selector := &ResetSoonestSelector{Fallback: &FillFirstSelector{}}
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	picked, err := selector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{authA, authB})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if picked.ID != "auth-1" {
		t.Fatalf("expected FillFirst fallback to pick auth-1, got %s", picked.ID)
	}
}

func TestSessionAffinitySelector_ReclaimHigherPriority(t *testing.T) {
	highAuth := &Auth{
		ID:         "high-auth",
		Provider:   "antigravity",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "10"},
	}
	lowAuth := &Auth{
		ID:         "low-auth",
		Provider:   "antigravity",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "1"},
	}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"test-reclaim-session"}},
	}
	ctx := context.Background()

	// 1. Selector without reclaim
	noReclaimSelector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		Reclaim:  false,
	})
	// Bind to lowAuth first
	_, _ = noReclaimSelector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{lowAuth})

	// When highAuth is available, no-reclaim stays with lowAuth
	pickedNoReclaim, err := noReclaimSelector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{lowAuth, highAuth})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if pickedNoReclaim.ID != "low-auth" {
		t.Fatalf("expected sticky low-auth, got %s", pickedNoReclaim.ID)
	}

	// 2. Selector with reclaim
	reclaimSelector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		Reclaim:  true,
	})
	// Bind to lowAuth first
	_, _ = reclaimSelector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{lowAuth})

	// When highAuth recovers, reclaimSelector reclaims to highAuth
	pickedReclaimed, err := reclaimSelector.Pick(ctx, "antigravity", "gemini-3.8-flash", opts, []*Auth{lowAuth, highAuth})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if pickedReclaimed.ID != "high-auth" {
		t.Fatalf("expected reclaimed to high-auth, got %s", pickedReclaimed.ID)
	}
}
