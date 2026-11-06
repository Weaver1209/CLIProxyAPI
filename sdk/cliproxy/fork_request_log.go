package cliproxy

import (
	"context"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/forkhistory"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type forkUsagePlugin struct {
	svc *forkhistory.Service
}

func (p *forkUsagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p.svc != nil {
		p.svc.RecordUsage(record)
	}
}

func init() {
	chURL := os.Getenv("CLIPROXY_CLICKHOUSE_URL")
	svc := forkhistory.InitGlobalService(chURL)
	coreusage.RegisterNamedPlugin("fork:clickhouse-history", &forkUsagePlugin{svc: svc})
}

// Flush flushes all in-memory usage records and durable disk spools to ClickHouse.
func Flush(ctx context.Context) error {
	return forkhistory.Flush(ctx)
}
