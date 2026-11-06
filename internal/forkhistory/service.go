package forkhistory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

var (
	globalService   *Service
	globalServiceMu sync.RWMutex

	// ErrClickHouseDisabled is returned when ClickHouse is not configured.
	ErrClickHouseDisabled = errors.New("clickhouse disabled")
	// ErrInvalidRange is returned when time filters are invalid.
	ErrInvalidRange = errors.New("invalid time range: since must be before until")
)

// Service encapsulates usage history ingestion, querying, pricing, and quota inspection.
type Service struct {
	writer   *BackgroundWriter
	pricing  *PricingResolver
	stateDir string
}

// InitGlobalService initializes the process-wide history service.
func InitGlobalService(clickhouseURL string) *Service {
	globalServiceMu.Lock()
	defer globalServiceMu.Unlock()

	if globalService != nil {
		return globalService
	}

	stateDir := os.Getenv("OMARCHY_STATE_DIR")
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			stateDir = filepath.Join(home, ".local", "state", "omarchy", "agents", "usage")
		} else {
			stateDir = filepath.Join(os.TempDir(), "omarchy-state")
		}
	}

	overrideFile := os.Getenv("CLIPROXY_PRICING_OVERRIDES_PATH")
	pricing := NewPricingResolver("", overrideFile)

	writer := NewBackgroundWriter(clickhouseURL, "")

	globalService = &Service{
		writer:   writer,
		pricing:  pricing,
		stateDir: stateDir,
	}

	return globalService
}

// GetGlobalService returns the initialized service or initializes from environment.
func GetGlobalService() *Service {
	globalServiceMu.RLock()
	s := globalService
	globalServiceMu.RUnlock()

	if s != nil {
		return s
	}

	chURL := os.Getenv("CLIPROXY_CLICKHOUSE_URL")
	return InitGlobalService(chURL)
}

// Flush ensures all pending usage records in memory and spools are written.
func Flush(ctx context.Context) error {
	svc := GetGlobalService()
	if svc == nil || svc.writer == nil {
		return nil
	}
	return svc.writer.Flush(ctx)
}

// WriterStatus returns the operational metrics of the background writer.
func (s *Service) WriterStatus() WriterStatus {
	if s == nil || s.writer == nil {
		return WriterStatus{Enabled: false}
	}
	return s.writer.Status()
}

// RecordUsage transforms an incoming coreusage.Record and queues it for asynchronous persistence.
func (s *Service) RecordUsage(record coreusage.Record) {
	if s == nil || s.writer == nil || !s.writer.IsEnabled() {
		return
	}

	row := s.ConvertRecordToRow(record)
	s.writer.Enqueue(row)
}

// ConvertRecordToRow transforms a coreusage.Record into a canonical UsageRow.
func (s *Service) ConvertRecordToRow(record coreusage.Record) *UsageRow {
	reqID := record.RequestID
	if reqID == "" {
		reqID = uuid.NewString()
	}

	reqAt := record.RequestedAt.UTC()
	if reqAt.IsZero() {
		reqAt = time.Now().UTC()
	}

	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tb := detail.TokenBreakdown

	uncachedInput := tb.Input.UncachedTokens
	cacheRead := tb.Input.CacheReadTokens
	cacheWrite := tb.Input.CacheWriteTokens
	inputTokens := tb.Input.TotalTokens
	outputTokens := tb.Output.TotalTokens
	nonReasoningOutput := tb.Output.NonReasoningTokens
	reasoningTokens := tb.Output.ReasoningTokens
	totalTokens := tb.TotalTokens

	if uncachedInput < 0 {
		uncachedInput = 0
	}
	if uncachedInput == 0 && inputTokens > cacheRead && cacheRead > 0 {
		uncachedInput = inputTokens - cacheRead
	}

	var cacheWrite1h int64
	if record.Detail.CacheCreation1hTokens != nil {
		cacheWrite1h = *record.Detail.CacheCreation1hTokens
	}

	credKind := "api_key"
	if record.AuthType == "oauth" ||
		record.AccessTokenSHA256 != "" ||
		strings.Contains(strings.ToLower(record.AccessProvider), "oauth") ||
		(record.APIKey == "" && record.AuthIndex != "") {
		credKind = "oauth"
	}

	accountHash := s.hashIdentity(record.AuthID, record.AuthIndex, record.AccessTokenSHA256, record.APIKey)
	sessionHash := ""
	if record.SessionID != "" {
		sessionHash = hashSHA256(record.SessionID)
	} else if record.ParentSessionID != "" {
		sessionHash = hashSHA256(record.ParentSessionID)
	}

	inCost, outCost, crCost, cwCost, totCost, savings, source := s.pricing.CalculateRowCosts(
		record.Provider,
		record.Model,
		uncachedInput,
		cacheRead,
		cacheWrite,
		cacheWrite1h,
		outputTokens,
	)

	var streamVal uint8
	if record.Stream {
		streamVal = 1
	}
	var failedVal uint8
	if record.Failed {
		failedVal = 1
	}
	var isNativeKeyVal uint8
	if record.IsNativeKey {
		isNativeKeyVal = 1
	}

	serviceTier := record.ServiceTier
	if serviceTier == "" {
		serviceTier = record.EffectiveServiceTier
	}

	return &UsageRow{
		ID:                       reqID,
		TraceID:                  record.TraceID,
		SelectionID:              record.SelectionID,
		RequestedAt:              reqAt.Format("2006-01-02 15:04:05.000"),
		Provider:                 record.Provider,
		Model:                    record.Model,
		Alias:                    record.Alias,
		ExecutorType:             record.ExecutorType,
		CredentialKind:           credKind,
		AccountHash:              accountHash,
		SessionHash:              sessionHash,
		IsNativeKey:              isNativeKeyVal,
		ServiceTier:              serviceTier,
		Stream:                   streamVal,
		Failed:                   failedVal,
		FailureCode:              record.Fail.Code,
		LatencyMS:                record.Latency.Milliseconds(),
		TTFTMS:                   record.TTFT.Milliseconds(),
		InputTokens:              inputTokens,
		UncachedInputTokens:      uncachedInput,
		CacheReadTokens:          cacheRead,
		CacheCreationTokens:      cacheWrite,
		CacheCreation1hTokens:    cacheWrite1h,
		OutputTokens:             outputTokens,
		NonReasoningOutputTokens: nonReasoningOutput,
		ReasoningTokens:          reasoningTokens,
		TotalTokens:              totalTokens,
		InputCost:                inCost,
		OutputCost:               outCost,
		CacheReadCost:            crCost,
		CacheWriteCost:           cwCost,
		TotalCost:                totCost,
		CacheSavings:             savings,
		PricingSource:            source,
	}
}

func (s *Service) hashIdentity(authID, authIndex, tokenSHA, apiKey string) string {
	raw := ""
	if tokenSHA != "" {
		raw = "token_sha:" + tokenSHA
	} else if authID != "" {
		raw = "auth_id:" + authID
	} else if authIndex != "" {
		raw = "auth_index:" + authIndex
	} else if apiKey != "" {
		raw = "api_key:" + apiKey
	} else {
		raw = "anonymous"
	}
	return hashSHA256(raw)
}

func hashSHA256(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// QueryAggregates queries ClickHouse for UTC hourly aggregated metrics.
func (s *Service) QueryAggregates(ctx context.Context, filter RangeFilter) (*UsageSummaryResponse, error) {
	if s == nil || s.writer == nil || !s.writer.IsEnabled() {
		return nil, ErrClickHouseDisabled
	}

	if filter.Since.IsZero() {
		filter.Since = time.Now().UTC().Add(-24 * time.Hour)
	}
	if filter.Until.IsZero() {
		filter.Until = time.Now().UTC()
	}
	if filter.Since.After(filter.Until) {
		return nil, ErrInvalidRange
	}

	sinceStr := filter.Since.UTC().Format("2006-01-02 15:04:05")
	untilStr := filter.Until.UTC().Format("2006-01-02 15:04:05")

	query := fmt.Sprintf(`
SELECT
	toStartOfHour(requested_at) AS hour_bucket,
	provider,
	model,
	credential_kind,
	count() AS requests,
	sum(input_tokens) AS input_tokens,
	sum(uncached_input_tokens) AS uncached_input_tokens,
	sum(cache_read_tokens) AS cache_read_tokens,
	sum(cache_creation_tokens) AS cache_creation_tokens,
	sum(cache_creation_1h_tokens) AS cache_creation_1h_tokens,
	sum(output_tokens) AS output_tokens,
	sum(non_reasoning_output_tokens) AS non_reasoning_output_tokens,
	sum(reasoning_tokens) AS reasoning_tokens,
	sum(total_tokens) AS total_tokens,
	sum(if(total_cost IS NOT NULL, total_cost, 0)) AS total_cost_sum,
	sum(if(cache_savings IS NOT NULL, cache_savings, 0)) AS cache_savings_sum,
	countIf(total_cost IS NULL) AS unknown_price_count
FROM usage_history
WHERE requested_at >= '%s' AND requested_at <= '%s'
`, sinceStr, untilStr)

	if filter.Provider != "" {
		query += fmt.Sprintf(" AND provider = '%s'", sanitizeSQL(filter.Provider))
	}
	if filter.Model != "" {
		query += fmt.Sprintf(" AND model = '%s'", sanitizeSQL(filter.Model))
	}
	if filter.CredentialKind != "" {
		query += fmt.Sprintf(" AND credential_kind = '%s'", sanitizeSQL(filter.CredentialKind))
	}

	query += `
GROUP BY hour_bucket, provider, model, credential_kind
ORDER BY hour_bucket ASC, provider ASC, model ASC
FORMAT JSON
`

	reqURL, err := url.Parse(s.writer.clickhouseURL)
	if err != nil {
		return nil, err
	}
	q := reqURL.Query()
	q.Set("query", strings.TrimSpace(query))
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.writer.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("clickhouse error %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	type CHJSONResult struct {
		Data []struct {
			HourBucket            string  `json:"hour_bucket"`
			Provider              string  `json:"provider"`
			Model                 string  `json:"model"`
			CredentialKind        string  `json:"credential_kind"`
			Requests              int64   `json:"requests,string"`
			InputTokens           int64   `json:"input_tokens,string"`
			UncachedInputTokens   int64   `json:"uncached_input_tokens,string"`
			CacheReadTokens       int64   `json:"cache_read_tokens,string"`
			CacheCreationTokens   int64   `json:"cache_creation_tokens,string"`
			CacheCreation1hTokens int64   `json:"cache_creation_1h_tokens,string"`
			OutputTokens          int64   `json:"output_tokens,string"`
			NonReasoningOutput    int64   `json:"non_reasoning_output_tokens,string"`
			ReasoningTokens       int64   `json:"reasoning_tokens,string"`
			TotalTokens           int64   `json:"total_tokens,string"`
			TotalCostSum          float64 `json:"total_cost_sum,string"`
			CacheSavingsSum       float64 `json:"cache_savings_sum,string"`
			UnknownPriceCount     int64   `json:"unknown_price_count,string"`
		} `json:"data"`
	}

	var chResp CHJSONResult
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(bodyBytes, &chResp); err != nil {
		return nil, fmt.Errorf("unmarshal clickhouse response: %w", err)
	}

	var buckets []UsageAggregateBucket
	var totalRequests, totalTokens, unknownPriceTotal int64
	var totalCostSum, totalSavingsSum float64
	var hasAnyCost bool

	for _, d := range chResp.Data {
		totalRequests += d.Requests
		totalTokens += d.TotalTokens
		unknownPriceTotal += d.UnknownPriceCount

		var costPtr *float64
		if d.UnknownPriceCount == 0 || d.TotalCostSum > 0 {
			v := d.TotalCostSum
			costPtr = &v
			totalCostSum += v
			hasAnyCost = true
		}

		var savPtr *float64
		if d.CacheSavingsSum > 0 {
			v := d.CacheSavingsSum
			savPtr = &v
			totalSavingsSum += v
		}

		buckets = append(buckets, UsageAggregateBucket{
			HourBucket:               d.HourBucket,
			Provider:                 d.Provider,
			Model:                    d.Model,
			CredentialKind:           d.CredentialKind,
			Requests:                 d.Requests,
			InputTokens:              d.InputTokens,
			UncachedInputTokens:      d.UncachedInputTokens,
			CacheReadTokens:          d.CacheReadTokens,
			CacheCreationTokens:      d.CacheCreationTokens,
			CacheCreation1hTokens:    d.CacheCreation1hTokens,
			OutputTokens:             d.OutputTokens,
			NonReasoningOutputTokens: d.NonReasoningOutput,
			ReasoningTokens:          d.ReasoningTokens,
			TotalTokens:              d.TotalTokens,
			TotalCost:                costPtr,
			CacheSavings:             savPtr,
			UnknownPriceCount:        d.UnknownPriceCount,
		})
	}

	var finalTotalCost *float64
	if hasAnyCost {
		finalTotalCost = &totalCostSum
	}
	var finalSavings *float64
	if totalSavingsSum > 0 {
		finalSavings = &totalSavingsSum
	}

	return &UsageSummaryResponse{
		Since:             filter.Since.UTC().Format(time.RFC3339),
		Until:             filter.Until.UTC().Format(time.RFC3339),
		TotalRequests:     totalRequests,
		TotalTokens:       totalTokens,
		TotalCost:         finalTotalCost,
		TotalCacheSavings: finalSavings,
		UnknownPriceCount: unknownPriceTotal,
		Buckets:           buckets,
		WriterStatus:      s.writer.Status(),
	}, nil
}

func sanitizeSQL(in string) string {
	return strings.ReplaceAll(strings.ReplaceAll(in, "'", "''"), "\\", "\\\\")
}

// ReadQuotaSnapshot reads parent collector snapshot and bounded append-only monthly history.
func (s *Service) ReadQuotaSnapshot(limit int, accountHash string) (*QuotaResponse, error) {
	if s == nil {
		return nil, errors.New("service uninitialized")
	}

	cpaPath := filepath.Join(s.stateDir, "cpa.json")
	var accounts []SanitizedAccount
	var todayTokens int64
	var todayTokensByModel map[string]int64
	var recentDays []RecentDayUsage
	var modelUsage map[string]any
	var tierLabel string
	ready := false
	updatedAt := time.Now().UTC().Format(time.RFC3339)

	data, err := os.ReadFile(cpaPath)
	if err == nil {
		var raw struct {
			UpdatedAt          string             `json:"updatedAt"`
			Ready              bool               `json:"ready"`
			TierLabel          string             `json:"tierLabel"`
			Accounts           []SanitizedAccount `json:"accounts"`
			TodayTotalTokens   int64              `json:"todayTotalTokens"`
			TodayTokensByModel map[string]int64   `json:"todayTokensByModel"`
			RecentDays         []RecentDayUsage   `json:"recentDays"`
			ModelUsage         map[string]any     `json:"modelUsage"`
		}
		if json.Unmarshal(data, &raw) == nil {
			ready = raw.Ready
			tierLabel = raw.TierLabel
			updatedAt = raw.UpdatedAt
			todayTokens = raw.TodayTotalTokens
			todayTokensByModel = raw.TodayTokensByModel
			recentDays = raw.RecentDays
			modelUsage = raw.ModelUsage
			accounts = raw.Accounts
		}
	}

	pattern := filepath.Join(s.stateDir, "cpa-history*.jsonl")
	files, _ := filepath.Glob(pattern)
	sort.Strings(files)

	var historyEntries []QuotaHistoryEntry

	for _, fp := range files {
		f, err := os.Open(fp)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var entry QuotaHistoryEntry
			if json.Unmarshal(line, &entry) == nil {
				if accountHash == "" || entry.AccountHash == accountHash {
					historyEntries = append(historyEntries, entry)
				}
			}
		}
		_ = f.Close()
	}

	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if len(historyEntries) > limit {
		historyEntries = historyEntries[len(historyEntries)-limit:]
	}

	forecasts := s.computeAccountForecasts(accounts, historyEntries)

	return &QuotaResponse{
		UpdatedAt:          updatedAt,
		Ready:              ready,
		TierLabel:          tierLabel,
		Accounts:           accounts,
		History:            historyEntries,
		TodayTotalTokens:   todayTokens,
		TodayTokensByModel: todayTokensByModel,
		RecentDays:         recentDays,
		ModelUsage:         modelUsage,
		Forecasts:          forecasts,
	}, nil
}

func (s *Service) computeAccountForecasts(accounts []SanitizedAccount, history []QuotaHistoryEntry) map[string]AccountForecast {
	forecasts := make(map[string]AccountForecast)
	now := time.Now().UTC()

	historyByAccount := make(map[string][]QuotaHistoryEntry)
	for _, h := range history {
		if h.AccountHash != "" {
			historyByAccount[h.AccountHash] = append(historyByAccount[h.AccountHash], h)
		}
	}

	for _, acc := range accounts {
		hList := historyByAccount[acc.ID]

		var targetLimit *SanitizedLimit
		for _, lim := range acc.Limits {
			if strings.EqualFold(lim.Title, "Session") || strings.Contains(strings.ToLower(lim.Label), "session") || lim.Percent > 0 {
				l := lim
				targetLimit = &l
				break
			}
		}

		if targetLimit == nil || len(hList) < 3 {
			forecasts[acc.ID] = AccountForecast{
				AccountHash:    acc.ID,
				Status:         "insufficient_history",
				Message:        "Insufficient history data points for valid exhaustion projection",
				DataPointCount: len(hList),
			}
			continue
		}

		resetTime, err := time.Parse(time.RFC3339, targetLimit.ResetsAt)
		if err != nil || resetTime.Before(now) {
			forecasts[acc.ID] = AccountForecast{
				AccountHash:    acc.ID,
				Status:         "insufficient_history",
				Message:        "Window reset time invalid or already elapsed",
				DataPointCount: len(hList),
			}
			continue
		}

		firstEntry := hList[0]
		lastEntry := hList[len(hList)-1]

		tFirst, err1 := time.Parse(time.RFC3339, firstEntry.Timestamp)
		tLast, err2 := time.Parse(time.RFC3339, lastEntry.Timestamp)
		if err1 != nil || err2 != nil || tLast.Sub(tFirst).Seconds() < 60 {
			forecasts[acc.ID] = AccountForecast{
				AccountHash:    acc.ID,
				Status:         "insufficient_history",
				Message:        "History span too brief for meaningful rate",
				DataPointCount: len(hList),
			}
			continue
		}

		firstPercent := 0.0
		if len(firstEntry.Limits) > 0 {
			firstPercent = firstEntry.Limits[0].Percent
		}
		lastPercent := targetLimit.Percent

		deltaPercent := lastPercent - firstPercent
		deltaSeconds := tLast.Sub(tFirst).Seconds()

		if deltaPercent <= 0.001 || deltaSeconds <= 0 {
			forecasts[acc.ID] = AccountForecast{
				AccountHash:    acc.ID,
				Status:         "insufficient_history",
				Message:        "Zero or negative consumption rate in recorded window",
				DataPointCount: len(hList),
				CurrentPercent: targetLimit.Percent,
			}
			continue
		}

		ratePerSec := deltaPercent / deltaSeconds
		remainingFraction := 1.0 - lastPercent
		if remainingFraction <= 0 {
			zeroSec := int64(0)
			exhausts := true
			forecasts[acc.ID] = AccountForecast{
				AccountHash:         acc.ID,
				Status:              "calculated",
				Message:             "Quota fully exhausted",
				CurrentPercent:      lastPercent,
				BurnRatePerSec:      &ratePerSec,
				SecondsUntilExhaust: &zeroSec,
				ProjectedExhaustAt:  now.Format(time.RFC3339),
				WindowResetsAt:      resetTime.Format(time.RFC3339),
				ExhaustsBeforeReset: &exhausts,
				DataPointCount:      len(hList),
			}
			continue
		}

		secUntilExhaust := int64(remainingFraction / ratePerSec)
		projectedExhaust := now.Add(time.Duration(secUntilExhaust) * time.Second)
		exhaustsBeforeReset := projectedExhaust.Before(resetTime)

		forecasts[acc.ID] = AccountForecast{
			AccountHash:         acc.ID,
			Status:              "calculated",
			Message:             "Valid linear consumption forecast computed",
			CurrentPercent:      lastPercent,
			BurnRatePerSec:      &ratePerSec,
			SecondsUntilExhaust: &secUntilExhaust,
			ProjectedExhaustAt:  projectedExhaust.Format(time.RFC3339),
			WindowResetsAt:      resetTime.Format(time.RFC3339),
			ExhaustsBeforeReset: &exhaustsBeforeReset,
			DataPointCount:      len(hList),
		}
	}

	return forecasts
}
