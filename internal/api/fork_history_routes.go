package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/forkhistory"
)

// registerForkHistoryRoutes registers custom v8 management routes for usage history and quota tracking.
func registerForkHistoryRoutes(v8 *gin.RouterGroup) {
	if v8 == nil {
		return
	}

	fork := v8.Group("/fork")
	{
		fork.GET("/usage", handleForkUsageAggregation)
		fork.GET("/usage/summary", handleForkUsageAggregation)
		fork.GET("/quota", handleForkQuotaSnapshot)
		fork.GET("/status", handleForkStatus)
	}
}

func handleForkUsageAggregation(c *gin.Context) {
	svc := forkhistory.GetGlobalService()
	if svc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "clickhouse_disabled",
			"message": "Usage history tracking is uninitialized",
		})
		return
	}

	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	until := now

	if sStr := strings.TrimSpace(c.Query("since")); sStr != "" {
		parsed, err := parseTimestamp(sStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_range",
				"message": "invalid 'since' timestamp format, expected RFC3339 or unix seconds",
			})
			return
		}
		since = parsed
	}

	if uStr := strings.TrimSpace(c.Query("until")); uStr != "" {
		parsed, err := parseTimestamp(uStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_range",
				"message": "invalid 'until' timestamp format, expected RFC3339 or unix seconds",
			})
			return
		}
		until = parsed
	}

	if since.After(until) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_range",
			"message": "since must be before until",
		})
		return
	}

	filter := forkhistory.RangeFilter{
		Since:          since,
		Until:          until,
		Provider:       strings.TrimSpace(c.Query("provider")),
		Model:          strings.TrimSpace(c.Query("model")),
		CredentialKind: strings.TrimSpace(c.Query("credential_kind")),
	}

	summary, err := svc.QueryAggregates(c.Request.Context(), filter)
	if err != nil {
		if errors.Is(err, forkhistory.ErrClickHouseDisabled) {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "clickhouse_disabled",
				"message": "Usage history tracking is disabled (CLIPROXY_CLICKHOUSE_URL unset)",
			})
			return
		}
		if errors.Is(err, forkhistory.ErrInvalidRange) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_range",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "query_failed",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, summary)
}

func handleForkQuotaSnapshot(c *gin.Context) {
	svc := forkhistory.GetGlobalService()
	if svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "service uninitialized",
		})
		return
	}

	limit := 200
	if lStr := strings.TrimSpace(c.Query("limit")); lStr != "" {
		if parsed, err := strconv.Atoi(lStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	accountHash := strings.TrimSpace(c.Query("account_hash"))
	snapshot, err := svc.ReadQuotaSnapshot(limit, accountHash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "read_failed",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, snapshot)
}

func handleForkStatus(c *gin.Context) {
	svc := forkhistory.GetGlobalService()
	if svc == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
		})
		return
	}
	resp, err := svc.ReadQuotaSnapshot(1, "")
	quotaReady := err == nil && resp != nil && resp.Ready

	c.JSON(http.StatusOK, gin.H{
		"writer":      svc.WriterStatus(),
		"quota_ready": quotaReady,
	})
}

func parseTimestamp(s string) (time.Time, error) {
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(sec, 0).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t.UTC(), nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t.UTC(), nil
	}
	t, err = time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t.UTC(), nil
	}
	return time.Parse("2006-01-02", s)
}
