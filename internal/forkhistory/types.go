package forkhistory

import "time"

// UsageRow represents a single partitioned usage event for ClickHouse.
type UsageRow struct {
	ID                       string   `json:"id"`
	TraceID                  string   `json:"trace_id"`
	SelectionID              string   `json:"selection_id"`
	RequestedAt              string   `json:"requested_at"`
	Provider                 string   `json:"provider"`
	Model                    string   `json:"model"`
	Alias                    string   `json:"alias"`
	ExecutorType             string   `json:"executor_type"`
	CredentialKind           string   `json:"credential_kind"`
	AccountHash              string   `json:"account_hash"`
	SessionHash              string   `json:"session_hash"`
	IsNativeKey              uint8    `json:"is_native_key"`
	ServiceTier              string   `json:"service_tier"`
	Stream                   uint8    `json:"stream"`
	Failed                   uint8    `json:"failed"`
	FailureCode              string   `json:"failure_code"`
	LatencyMS                int64    `json:"latency_ms"`
	TTFTMS                   int64    `json:"ttft_ms"`
	InputTokens              int64    `json:"input_tokens"`
	UncachedInputTokens      int64    `json:"uncached_input_tokens"`
	CacheReadTokens          int64    `json:"cache_read_tokens"`
	CacheCreationTokens      int64    `json:"cache_creation_tokens"`
	CacheCreation1hTokens    int64    `json:"cache_creation_1h_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	NonReasoningOutputTokens int64    `json:"non_reasoning_output_tokens"`
	ReasoningTokens          int64    `json:"reasoning_tokens"`
	TotalTokens              int64    `json:"total_tokens"`
	InputCost                *float64 `json:"input_cost"`
	OutputCost               *float64 `json:"output_cost"`
	CacheReadCost            *float64 `json:"cache_read_cost"`
	CacheWriteCost           *float64 `json:"cache_write_cost"`
	TotalCost                *float64 `json:"total_cost"`
	CacheSavings             *float64 `json:"cache_savings"`
	PricingSource            *string  `json:"pricing_source"`
}

// ModelPricing defines per-token rates in USD.
// Pointers indicate unknown pricing; a nil price must produce NULL costs rather than 0.
type ModelPricing struct {
	InputCostPerToken        *float64 `json:"input_cost_per_token"`
	OutputCostPerToken       *float64 `json:"output_cost_per_token"`
	CacheReadCostPerToken    *float64 `json:"cache_read_cost_per_token"`
	CacheWriteCostPerToken   *float64 `json:"cache_write_cost_per_token"`
	CacheWrite1hCostPerToken *float64 `json:"cache_write_1h_cost_per_token"`
	Source                   string   `json:"source"`
}

// UsageAggregateBucket represents a UTC hourly aggregated bucket.
type UsageAggregateBucket struct {
	HourBucket               string   `json:"hour_bucket"`
	Provider                 string   `json:"provider"`
	Model                    string   `json:"model"`
	CredentialKind           string   `json:"credential_kind"`
	Requests                 int64    `json:"requests"`
	InputTokens              int64    `json:"input_tokens"`
	UncachedInputTokens      int64    `json:"uncached_input_tokens"`
	CacheReadTokens          int64    `json:"cache_read_tokens"`
	CacheCreationTokens      int64    `json:"cache_creation_tokens"`
	CacheCreation1hTokens    int64    `json:"cache_creation_1h_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	NonReasoningOutputTokens int64    `json:"non_reasoning_output_tokens"`
	ReasoningTokens          int64    `json:"reasoning_tokens"`
	TotalTokens              int64    `json:"total_tokens"`
	TotalCost                *float64 `json:"total_cost"`
	CacheSavings             *float64 `json:"cache_savings"`
	UnknownPriceCount        int64    `json:"unknown_price_count"`
}

// WriterStatus reports operational metrics for background persistence.
type WriterStatus struct {
	Enabled        bool   `json:"enabled"`
	ClickHouseURL  string `json:"clickhouse_url,omitempty"`
	BacklogCount   int64  `json:"backlog_count"`
	SpoolSizeBytes int64  `json:"spool_size_bytes"`
	DroppedCount   int64  `json:"dropped_count"`
	TotalWritten   int64  `json:"total_written"`
	LastError      string `json:"last_error,omitempty"`
	LastErrorAt    string `json:"last_error_at,omitempty"`
}

// UsageSummaryResponse represents the response payload for the aggregation endpoint.
type UsageSummaryResponse struct {
	Since             string                 `json:"since"`
	Until             string                 `json:"until"`
	TotalRequests     int64                  `json:"total_requests"`
	TotalTokens       int64                  `json:"total_tokens"`
	TotalCost         *float64               `json:"total_cost"`
	TotalCacheSavings *float64               `json:"total_cache_savings"`
	UnknownPriceCount int64                  `json:"unknown_price_count"`
	Buckets           []UsageAggregateBucket `json:"buckets"`
	WriterStatus      WriterStatus           `json:"writer_status"`
}

// SanitizedLimit represents an account quota window.
type SanitizedLimit struct {
	Label    string  `json:"label"`
	Title    string  `json:"title"`
	Percent  float64 `json:"percent"`
	Kind     string  `json:"kind,omitempty"`
	ResetsAt string  `json:"resetsAt"`
}

// SanitizedAccount represents a credential account without secrets.
type SanitizedAccount struct {
	ID        string           `json:"id"`
	Label     string           `json:"label"`
	Email     string           `json:"email"`
	Plan      string           `json:"plan"`
	Active    bool             `json:"active"`
	Primary   bool             `json:"primary"`
	Limits    []SanitizedLimit `json:"limits"`
	Stale     bool             `json:"stale"`
	FetchedAt int64            `json:"fetchedAt"`
}

// QuotaHistoryEntry represents a snapshot from append-only monthly files.
type QuotaHistoryEntry struct {
	Timestamp   string           `json:"timestamp"`
	Month       string           `json:"month,omitempty"`
	AccountHash string           `json:"accountHash"`
	Tier        string           `json:"tier,omitempty"`
	Limits      []SanitizedLimit `json:"limits"`
}

// RecentDayUsage models daily token counters.
type RecentDayUsage struct {
	Date         string `json:"date"`
	MessageCount int64  `json:"messageCount"`
}

// AccountForecast contains quota exhaustion projection for an account.
type AccountForecast struct {
	AccountHash         string   `json:"account_hash"`
	Status              string   `json:"status"`
	Message             string   `json:"message"`
	CurrentPercent      float64  `json:"current_percent,omitempty"`
	BurnRatePerSec      *float64 `json:"burn_rate_per_sec,omitempty"`
	SecondsUntilExhaust *int64   `json:"seconds_until_exhaust,omitempty"`
	ProjectedExhaustAt  string   `json:"projected_exhaust_at,omitempty"`
	WindowResetsAt      string   `json:"window_resets_at,omitempty"`
	ExhaustsBeforeReset *bool    `json:"exhausts_before_reset,omitempty"`
	DataPointCount      int      `json:"data_point_count"`
}

// QuotaResponse represents the payload of the /v8/management/fork/quota endpoint.
type QuotaResponse struct {
	UpdatedAt          string                     `json:"updated_at"`
	Ready              bool                       `json:"ready"`
	TierLabel          string                     `json:"tier_label"`
	Accounts           []SanitizedAccount         `json:"accounts"`
	History            []QuotaHistoryEntry        `json:"history"`
	TodayTotalTokens   int64                      `json:"today_total_tokens"`
	TodayTokensByModel map[string]int64           `json:"today_tokens_by_model,omitempty"`
	RecentDays         []RecentDayUsage           `json:"recent_days,omitempty"`
	ModelUsage         map[string]any             `json:"model_usage,omitempty"`
	Forecasts          map[string]AccountForecast `json:"forecasts"`
}

// RangeFilter defines query parameter constraints for aggregation.
type RangeFilter struct {
	Since          time.Time
	Until          time.Time
	Provider       string
	Model          string
	CredentialKind string
}
