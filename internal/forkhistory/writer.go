package forkhistory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BackgroundWriter handles async writing of UsageRow items to ClickHouse with durable disk spooling.
type BackgroundWriter struct {
	mu            sync.Mutex
	clickhouseURL string
	enabled       bool
	queue         chan *UsageRow
	spoolPath     string
	spoolMu       sync.Mutex

	totalWritten atomic.Int64
	droppedCount atomic.Int64

	lastError   string
	lastErrorAt time.Time
	httpClient  *http.Client

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewBackgroundWriter creates an async writer. If clickhouseURL is empty, writer is disabled.
func NewBackgroundWriter(clickhouseURL string, spoolDir string) *BackgroundWriter {
	enabled := strings.TrimSpace(clickhouseURL) != ""

	if spoolDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			spoolDir = filepath.Join(home, ".local", "state", "cliproxy")
		} else {
			spoolDir = filepath.Join(os.TempDir(), "cliproxy-spool")
		}
	}
	_ = os.MkdirAll(spoolDir, 0o700)
	spoolPath := filepath.Join(spoolDir, "history_spool.jsonl")

	w := &BackgroundWriter{
		clickhouseURL: strings.TrimSpace(clickhouseURL),
		enabled:       enabled,
		queue:         make(chan *UsageRow, 8192),
		spoolPath:     spoolPath,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}

	if enabled {
		go w.run()
	} else {
		close(w.doneCh)
	}

	return w
}

// IsEnabled returns true if ClickHouse URL is configured.
func (w *BackgroundWriter) IsEnabled() bool {
	return w != nil && w.enabled
}

// Enqueue delivers a row asynchronously. It NEVER blocks inbound requests.
// If the memory channel is saturated, it persists directly to the durable disk spool.
func (w *BackgroundWriter) Enqueue(row *UsageRow) {
	if w == nil || !w.enabled || row == nil {
		return
	}

	select {
	case w.queue <- row:
	default:
		w.spoolMu.Lock()
		w.appendSpoolLocked([]*UsageRow{row})
		w.spoolMu.Unlock()
	}
}

// Status returns operational metrics.
func (w *BackgroundWriter) Status() WriterStatus {
	if w == nil || !w.enabled {
		return WriterStatus{Enabled: false}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	var spoolSize int64
	w.spoolMu.Lock()
	if fi, err := os.Stat(w.spoolPath); err == nil {
		spoolSize = fi.Size()
	}
	w.spoolMu.Unlock()

	var lastErrStr, lastErrAtStr string
	if w.lastError != "" {
		lastErrStr = w.lastError
		lastErrAtStr = w.lastErrorAt.UTC().Format(time.RFC3339)
	}

	return WriterStatus{
		Enabled:        true,
		ClickHouseURL:  w.clickhouseURL,
		BacklogCount:   int64(len(w.queue)),
		SpoolSizeBytes: spoolSize,
		DroppedCount:   w.droppedCount.Load(),
		TotalWritten:   w.totalWritten.Load(),
		LastError:      lastErrStr,
		LastErrorAt:    lastErrAtStr,
	}
}

func (w *BackgroundWriter) run() {
	defer close(w.doneCh)

	_ = w.ensureTable()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var batch []*UsageRow

	for {
		select {
		case <-w.stopCh:
			for {
				select {
				case r := <-w.queue:
					batch = append(batch, r)
				default:
					goto drained
				}
			}
		drained:
			if len(batch) > 0 {
				_ = w.flushBatch(batch)
			}
			return

		case r := <-w.queue:
			batch = append(batch, r)
			if len(batch) >= 200 {
				if err := w.flushBatch(batch); err != nil {
					w.spoolMu.Lock()
					w.appendSpoolLocked(batch)
					w.spoolMu.Unlock()
				}
				batch = nil
			}

		case <-ticker.C:
			w.replaySpool()

			if len(batch) > 0 {
				if err := w.flushBatch(batch); err != nil {
					w.spoolMu.Lock()
					w.appendSpoolLocked(batch)
					w.spoolMu.Unlock()
				}
				batch = nil
			}
		}
	}
}

// Close stops the background worker.
func (w *BackgroundWriter) Close() {
	if w == nil || !w.enabled {
		return
	}
	w.mu.Lock()
	select {
	case <-w.stopCh:
		w.mu.Unlock()
		return
	default:
		close(w.stopCh)
	}
	w.mu.Unlock()
	<-w.doneCh
}

// Flush writes all in-memory queued rows and durable disk spools to ClickHouse.
func (w *BackgroundWriter) Flush(ctx context.Context) error {
	if w == nil || !w.enabled {
		return nil
	}

	var batch []*UsageRow
	for {
		select {
		case r := <-w.queue:
			batch = append(batch, r)
		default:
			goto memoryDrained
		}
	}

memoryDrained:
	if len(batch) > 0 {
		if err := w.flushBatch(batch); err != nil {
			w.spoolMu.Lock()
			w.appendSpoolLocked(batch)
			w.spoolMu.Unlock()
			return fmt.Errorf("flush memory queue: %w", err)
		}
	}

	return w.replaySpoolWithContext(ctx)
}

func (w *BackgroundWriter) appendSpoolLocked(rows []*UsageRow) {
	if len(rows) == 0 {
		return
	}
	f, err := os.OpenFile(w.spoolPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		w.droppedCount.Add(int64(len(rows)))
		return
	}
	defer f.Close()

	for _, r := range rows {
		b, err := json.Marshal(r)
		if err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
}

func (w *BackgroundWriter) replaySpool() {
	_ = w.replaySpoolWithContext(context.Background())
}

func (w *BackgroundWriter) replaySpoolWithContext(ctx context.Context) error {
	w.spoolMu.Lock()
	defer w.spoolMu.Unlock()

	fi, err := os.Stat(w.spoolPath)
	if err != nil || fi.Size() == 0 {
		return nil
	}

	f, err := os.Open(w.spoolPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var batch []*UsageRow
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var unwritten []*UsageRow

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var row UsageRow
		if json.Unmarshal(line, &row) == nil {
			batch = append(batch, &row)
		}
		if len(batch) >= 200 {
			if err := w.flushBatch(batch); err != nil {
				unwritten = append(unwritten, batch...)
				for scanner.Scan() {
					var remaining UsageRow
					if json.Unmarshal(scanner.Bytes(), &remaining) == nil {
						unwritten = append(unwritten, &remaining)
					}
				}
				break
			}
			batch = nil
		}
	}

	if len(batch) > 0 {
		if err := w.flushBatch(batch); err != nil {
			unwritten = append(unwritten, batch...)
		}
	}

	_ = f.Close()

	if len(unwritten) == 0 {
		_ = os.Remove(w.spoolPath)
		return nil
	}

	tmp := w.spoolPath + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		for _, r := range unwritten {
			b, _ := json.Marshal(r)
			_, _ = tf.Write(append(b, '\n'))
		}
		_ = tf.Close()
		_ = os.Rename(tmp, w.spoolPath)
	}

	return errors.New("spool partially written; clickhouse retry pending")
}

func (w *BackgroundWriter) flushBatch(batch []*UsageRow) error {
	if len(batch) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for _, row := range batch {
		data, err := json.Marshal(row)
		if err != nil {
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	reqURL, err := url.Parse(w.clickhouseURL)
	if err != nil {
		w.recordError(err)
		return err
	}

	q := reqURL.Query()
	q.Set("query", "INSERT INTO usage_history FORMAT JSONEachRow")
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodPost, reqURL.String(), &buf)
	if err != nil {
		w.recordError(err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.recordError(err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		err := fmt.Errorf("clickhouse error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		w.recordError(err)
		return err
	}

	w.totalWritten.Add(int64(len(batch)))
	w.clearError()
	return nil
}

func (w *BackgroundWriter) ensureTable() error {
	createSQL := `
CREATE TABLE IF NOT EXISTS usage_history (
	id String,
	trace_id String,
	selection_id String,
	requested_at DateTime64(3, 'UTC'),
	provider LowCardinality(String),
	model LowCardinality(String),
	alias LowCardinality(String),
	executor_type LowCardinality(String),
	credential_kind LowCardinality(String),
	account_hash FixedString(64),
	session_hash String,
	is_native_key UInt8,
	service_tier LowCardinality(String),
	stream UInt8,
	failed UInt8,
	failure_code LowCardinality(String),
	latency_ms Int64,
	ttft_ms Int64,
	input_tokens Int64,
	uncached_input_tokens Int64,
	cache_read_tokens Int64,
	cache_creation_tokens Int64,
	cache_creation_1h_tokens Int64,
	output_tokens Int64,
	non_reasoning_output_tokens Int64,
	reasoning_tokens Int64,
	total_tokens Int64,
	input_cost Nullable(Float64),
	output_cost Nullable(Float64),
	cache_read_cost Nullable(Float64),
	cache_write_cost Nullable(Float64),
	total_cost Nullable(Float64),
	cache_savings Nullable(Float64),
	pricing_source Nullable(String)
) ENGINE = MergeTree()
ORDER BY (provider, model, toStartOfHour(requested_at), id)
`
	reqURL, err := url.Parse(w.clickhouseURL)
	if err != nil {
		return err
	}
	q := reqURL.Query()
	q.Set("query", strings.TrimSpace(createSQL))
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.recordError(err)
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (w *BackgroundWriter) recordError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastError = err.Error()
	w.lastErrorAt = time.Now()
}

func (w *BackgroundWriter) clearError() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastError = ""
}
