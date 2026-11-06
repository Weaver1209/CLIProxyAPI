package auth

import (
	"context"
	"sort"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"strconv"
)

// ResetSoonestSelector implements the "reset-soonest" strategy:
// It selects the credential whose weekly quota window resets earliest (soonest).
// If weekly reset metadata is unavailable or credentials lack reset timestamps,
// it falls back to the provided fallback selector (or RoundRobinSelector).
type ResetSoonestSelector struct {
	Fallback Selector
}

// Pick selects the available auth whose weekly quota resets soonest.
func (s *ResetSoonestSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no available auth candidates"}
	}
	if len(available) == 1 {
		return available[0], nil
	}

	// Filter candidates with real weekly reset metadata
	type resetCandidate struct {
		auth    *Auth
		resetAt time.Time
	}
	var withResets []resetCandidate
	for _, a := range available {
		if t, ok := extractWeeklyResetTime(a, now); ok {
			withResets = append(withResets, resetCandidate{auth: a, resetAt: t})
		}
	}

	if len(withResets) > 0 {
		sort.SliceStable(withResets, func(i, j int) bool {
			return withResets[i].resetAt.Before(withResets[j].resetAt)
		})
		return withResets[0].auth, nil
	}

	fallback := s.Fallback
	if fallback == nil {
		fallback = &RoundRobinSelector{}
	}
	return fallback.Pick(ctx, provider, model, opts, available)
}

// extractWeeklyResetTime attempts to extract the weekly quota reset time from real credential metadata or signals.
func extractWeeklyResetTime(a *Auth, now time.Time) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}

	// 1. Check Quota.Signals
	if a.Quota.Signals != nil {
		for _, key := range []string{"weekly_quota_reset_at", "weekly_reset_at", "weekly-reset-at", "X-Codex-Secondary-Reset-At", "reset_at"} {
			if val, ok := a.Quota.Signals[key]; ok && strings.TrimSpace(val) != "" {
				if t, err := time.Parse(time.RFC3339, strings.TrimSpace(val)); err == nil && t.After(now) {
					return t, true
				}
				if ts, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64); err == nil && ts > 0 {
					t := time.Unix(ts, 0)
					if t.After(now) {
						return t, true
					}
				}
			}
		}
	}

	// 2. Check Metadata
	if a.Metadata != nil {
		for _, key := range []string{"weekly_quota_reset_at", "weekly_reset_at", "weekly-reset-at", "reset_time", "resetTime"} {
			if val, ok := a.Metadata[key]; ok {
				if strVal, isStr := val.(string); isStr && strings.TrimSpace(strVal) != "" {
					if t, err := time.Parse(time.RFC3339, strings.TrimSpace(strVal)); err == nil && t.After(now) {
						return t, true
					}
				}
			}
		}
	}

	return time.Time{}, false
}

// ReclaimAffinityIfHigherPriorityAvailable checks if an established session binding on boundAuthID
// should be reclaimed because a higher priority credential has recovered and become available.
func ReclaimAffinityIfHigherPriorityAvailable(boundAuthID string, available []*Auth) (*Auth, bool) {
	if boundAuthID == "" || len(available) == 0 {
		return nil, false
	}
	var currentBound *Auth
	for _, a := range available {
		if a != nil && a.ID == boundAuthID {
			currentBound = a
			break
		}
	}

	highest := highestPriorityAuths(available)
	if len(highest) == 0 {
		return nil, false
	}
	bestPriority := authPriority(highest[0])

	if currentBound == nil {
		// Bound auth is not even available; choose from highest available
		return highest[0], true
	}

	if authPriority(currentBound) < bestPriority {
		// Current bound auth is lower priority than available candidate; reclaim!
		return highest[0], true
	}

	return nil, false
}
