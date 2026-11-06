package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// ShutdownDrainDuration reads CLIPROXY_SHUTDOWN_DRAIN from the environment.
// It returns 0 if unset, empty, or invalid.
func ShutdownDrainDuration() time.Duration {
	val := strings.TrimSpace(os.Getenv("CLIPROXY_SHUTDOWN_DRAIN"))
	if val == "" {
		return 0
	}
	d, err := time.ParseDuration(val)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// stopServerWithOptionalDrain closes listeners and either gracefully drains or immediately closes the HTTP server
// based on CLIPROXY_SHUTDOWN_DRAIN.
func (s *Server) stopServerWithOptionalDrain(ctx context.Context) error {
	drainDuration := ShutdownDrainDuration()

	var errCloseServer error
	if s.server != nil {
		if drainDuration > 0 {
			drainCtx := ctx
			var cancel context.CancelFunc
			if drainCtx == nil {
				drainCtx, cancel = context.WithTimeout(context.Background(), drainDuration)
			} else {
				drainCtx, cancel = context.WithTimeout(drainCtx, drainDuration)
			}
			defer cancel()

			log.Infof("Gracefully draining API server connections for up to %s...", drainDuration)
			errCloseServer = s.server.Shutdown(drainCtx)
			if errors.Is(errCloseServer, http.ErrServerClosed) || errors.Is(errCloseServer, net.ErrClosed) || errors.Is(errCloseServer, context.Canceled) {
				errCloseServer = nil
			}
		} else {
			errCloseServer = s.server.Close()
			if errors.Is(errCloseServer, http.ErrServerClosed) || errors.Is(errCloseServer, net.ErrClosed) {
				errCloseServer = nil
			}
		}
	}
	return errCloseServer
}
