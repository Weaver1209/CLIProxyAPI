# Fork Notes: Antigravity Hub Extras

This document details the fork extras implemented in CLIProxyAPI for Antigravity, T3 Code, and Omarchy environment compatibility.

## 1. Graceful Shutdown Drain (`CLIPROXY_SHUTDOWN_DRAIN`)
- **Motivation**: Prevent in-flight WebSocket streams and HTTP responses from being brutally dropped upon `SIGTERM` / shutdown during process restarts or container recycling.
- **Behavior**:
  - Gated by the `CLIPROXY_SHUTDOWN_DRAIN` environment variable (e.g. `CLIPROXY_SHUTDOWN_DRAIN="15s"`).
  - When set to a positive duration, the API server stops accepting new connections on its listeners and gracefully drains active connections for up to the configured duration using `http.Server.Shutdown(ctx)`.
  - When unset or zero, standard immediate shutdown behavior (`server.Close()`) is preserved.
- **Implementation**:
  - `internal/api/fork_drain.go`
  - Hooked into `(*Server).Stop()` in `internal/api/server.go`.

## 2. Truthful Responses Terminal Limit Error (`usage_limit_reached` with `resets_at`)
- **Motivation**: When all usable Antigravity credentials in a pool are exhausted, downstream Codex CLI instances (and T3 Code auto-resumption) must receive an explicit, truthful `usage_limit_reached` terminal error containing an exact `resets_at` epoch timestamp and `resets_in_seconds`. Without this, clients may either disconnect blindly, retry in a tight 429 loop, or miss auto-resumption windows.
- **Behavior**:
  - Evaluated **only after** all eligible Antigravity credentials in the pool are exhausted and cooling down (`model_cooldown`).
  - Terminal response body surfaces `error.type: "usage_limit_reached"`, `error.code: "model_cooldown"`, and truthful `resets_at` derived from the earliest cooldown recovery.
  - Safe headers retain `Retry-After` (in seconds) and emit `X-Codex-Primary-Used-Percent: 100` and `X-Codex-Secondary-Used-Percent: 100` for clients tracking quota signals.
  - Short transient 429 errors (such as turn rate limits or capacity constraints) stay classified as `rate_limit_error` / `rate_limit_exceeded` and never invent false reset deadlines.
- **Implementation**:
  - `sdk/api/handlers/fork_cooldown_error.go`
  - Integrated into `BuildErrorResponseBodyWithError()` in `sdk/api/handlers/handlers.go`, error response header synthesis in `sdk/api/handlers/handlers_errors.go`, and WebSocket terminal error dispatch in `sdk/api/handlers/openai/openai_responses_websocket_forward.go`.

## 3. Advanced Routing Extras
### 3.1 Routing Strategy: `reset-soonest`
- **Config**: `routing.strategy: "reset-soonest"` (or `"soonest-reset"`).
- **Behavior**:
  - Evaluates active candidates against real weekly reset metadata (e.g. `weekly_quota_reset_at` in credential metadata or quota signals).
  - Routes traffic to the credential whose quota resets earliest, optimizing weekly rolling window utilization across multi-account pools.
  - If reset metadata is absent or unparseable, falls back gracefully to standard round-robin / fill-first without fabricating fake timestamps.
- **Implementation**:
  - `sdk/cliproxy/auth/fork_routing_extras.go` (`ResetSoonestSelector`).
  - Config wired in `sdk/cliproxy/service_config.go`.

### 3.2 Session Affinity Reclaim (`session-affinity-reclaim`)
- **Config**: `routing.session-affinity-reclaim: true`.
- **Behavior**:
  - Under session affinity, if an established session previously fell over to a lower-priority credential because a higher-priority account was cooling down, it reclaims the session back to the higher-priority credential once that credential becomes available.
  - Default remains `false` to preserve strict stickiness when not opted in.
- **Implementation**:
  - `sdk/cliproxy/auth/fork_routing_extras.go` (`ReclaimAffinityIfHigherPriorityAvailable`).
  - Hooked in `(*SessionAffinitySelector).Pick()` in `sdk/cliproxy/auth/selector.go`.
  - Config opt-in added in `internal/config/config_types.go` and `sdk/cliproxy/service_config.go`.

## 4. Test Verification
All new features have targeted unit tests:
- `internal/api/fork_drain_test.go`
- `sdk/api/handlers/fork_cooldown_error_test.go`
- `sdk/api/handlers/openai/fork_responses_protocol_test.go`
- `sdk/cliproxy/auth/fork_routing_extras_test.go`

All tests pass deterministically without wall-clock sleeps or network flakiness.
