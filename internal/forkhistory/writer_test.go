package forkhistory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriterFullQueueDurableSpool(t *testing.T) {
	spoolDir := t.TempDir()

	// Writer without server, disabled DB url
	w := &BackgroundWriter{
		enabled:   true,
		queue:     make(chan *UsageRow, 2), // Tiny queue of capacity 2
		spoolPath: filepath.Join(spoolDir, "history_spool.jsonl"),
	}

	row1 := &UsageRow{ID: "req-1", Model: "test-model"}
	row2 := &UsageRow{ID: "req-2", Model: "test-model"}
	row3 := &UsageRow{ID: "req-3", Model: "test-model"} // should overflow into spool
	row4 := &UsageRow{ID: "req-4", Model: "test-model"} // should overflow into spool

	// These should NEVER block
	w.Enqueue(row1)
	w.Enqueue(row2)
	w.Enqueue(row3)
	w.Enqueue(row4)

	if len(w.queue) != 2 {
		t.Fatalf("expected 2 items in memory queue, got %d", len(w.queue))
	}

	// Verify spool file was written with 2 items
	data, err := os.ReadFile(w.spoolPath)
	if err != nil {
		t.Fatalf("expected spool file to exist: %v", err)
	}

	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Fatalf("expected 2 spooled lines, got %d", lines)
	}
}

func TestWriterDBOutageAndRetryFlush(t *testing.T) {
	var dbStatus atomic.Int32
	dbStatus.Store(http.StatusInternalServerError) // Initially failing

	var receivedCount atomic.Int64
	var receivedMu sync.Mutex
	var receivedIDs []string

	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		status := int(dbStatus.Load())
		if status != http.StatusOK {
			http.Error(rw, "DB unavailable", status)
			return
		}

		body, _ := io.ReadAll(req.Body)
		lines := splitLines(body)
		for _, l := range lines {
			if len(l) == 0 {
				continue
			}
			var r UsageRow
			if json.Unmarshal(l, &r) == nil {
				receivedMu.Lock()
				receivedIDs = append(receivedIDs, r.ID)
				receivedMu.Unlock()
				receivedCount.Add(1)
			}
		}
		rw.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	spoolDir := t.TempDir()
	w := NewBackgroundWriter(ts.URL, spoolDir)
	defer w.Close()

	// Enqueue rows while DB is down
	w.Enqueue(&UsageRow{ID: "msg-1"})
	w.Enqueue(&UsageRow{ID: "msg-2"})

	// Give background worker time to attempt write and fail to spool
	time.Sleep(700 * time.Millisecond)

	// Recover DB
	dbStatus.Store(http.StatusOK)

	// Flush with recovered DB
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := w.Flush(ctx); err != nil {
		t.Fatalf("expected successful flush after DB recovered, got %v", err)
	}

	if receivedCount.Load() != 2 {
		t.Fatalf("expected 2 delivered rows, got %d", receivedCount.Load())
	}

	// Verify spool file was cleaned up on successful flush
	fi, err := os.Stat(w.spoolPath)
	if err == nil && fi.Size() > 0 {
		t.Fatalf("expected spool to be empty, got size %d", fi.Size())
	}
}

func splitLines(data []byte) [][]byte {
	var res [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			res = append(res, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		res = append(res, data[start:])
	}
	return res
}
