package api

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestShutdownDrainDuration(t *testing.T) {
	t.Setenv("CLIPROXY_SHUTDOWN_DRAIN", "")
	if got := ShutdownDrainDuration(); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}

	t.Setenv("CLIPROXY_SHUTDOWN_DRAIN", "invalid")
	if got := ShutdownDrainDuration(); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}

	t.Setenv("CLIPROXY_SHUTDOWN_DRAIN", "5s")
	if got := ShutdownDrainDuration(); got != 5*time.Second {
		t.Fatalf("expected 5s, got %v", got)
	}
}

func TestStopServerWithOptionalDrain(t *testing.T) {
	s := &Server{
		server: &http.Server{},
	}
	t.Setenv("CLIPROXY_SHUTDOWN_DRAIN", "100ms")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := s.stopServerWithOptionalDrain(ctx); err != nil {
		t.Fatalf("stopServerWithOptionalDrain error: %v", err)
	}
}
