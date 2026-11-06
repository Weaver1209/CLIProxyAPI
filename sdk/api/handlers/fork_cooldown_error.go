package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type modelCooldownResetInfo struct {
	IsCooldown   bool
	ResetSeconds int
	ResetsAt     int64
	Model        string
	Message      string
	Provider     string
}

// ExtractModelCooldownInfo extracts cooldown reset timing from err if it is a model_cooldown error.
func ExtractModelCooldownInfo(err error, now time.Time) (info modelCooldownResetInfo) {
	if err == nil {
		return
	}
	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var mcm modelCooldownMarker
	isCooldown := errors.As(err, &mcm) && mcm != nil && mcm.IsModelCooldown()

	var authErr *coreauth.Error
	if !isCooldown && errors.As(err, &authErr) && authErr != nil && strings.EqualFold(strings.TrimSpace(authErr.Code), "model_cooldown") {
		isCooldown = true
	}

	errStr := err.Error()
	if !isCooldown && ojson.Valid(errStr) {
		code := ojson.Get(errStr, "error.code").String()
		errType := ojson.Get(errStr, "error.type").String()
		if strings.EqualFold(code, "model_cooldown") || strings.EqualFold(errType, "model_cooldown") {
			isCooldown = true
		}
	}

	if !isCooldown {
		return
	}

	info.IsCooldown = true

	// Check if err provides SafeResponseHeaders for Retry-After
	headers := coreauth.SafeResponseHeaders(err)
	if headers != nil {
		if ra := headers.Get("Retry-After"); ra != "" {
			if sec, errParse := strconv.Atoi(strings.TrimSpace(ra)); errParse == nil && sec >= 0 {
				info.ResetSeconds = sec
			}
		}
	}

	if ojson.Valid(errStr) {
		if sec := ojson.Get(errStr, "error.reset_seconds").Int(); sec > 0 {
			info.ResetSeconds = int(sec)
		}
		if model := ojson.Get(errStr, "error.model").String(); model != "" {
			info.Model = model
		}
		if provider := ojson.Get(errStr, "error.provider").String(); provider != "" {
			info.Provider = provider
		}
		if msg := ojson.Get(errStr, "error.message").String(); msg != "" {
			info.Message = msg
		}
	}

	if info.ResetSeconds <= 0 {
		info.IsCooldown = false
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	info.ResetsAt = now.Add(time.Duration(info.ResetSeconds) * time.Second).Unix()
	return
}

// BuildModelCooldownUsageLimitError builds a truthful usage_limit_reached JSON payload with resets_at.
func BuildModelCooldownUsageLimitError(info modelCooldownResetInfo) []byte {
	message := info.Message
	if strings.TrimSpace(message) == "" {
		if info.Model != "" {
			message = "All credentials for model " + info.Model + " are cooling down"
		} else {
			message = "The usage limit has been reached"
		}
	} else if info.Model != "" && !strings.Contains(message, info.Model) {
		message = "All credentials for model " + info.Model + " are cooling down: " + message
	}

	doc := ojson.NewDoc([]byte(`{"error":{}}`))
	_ = doc.Set("error.type", "usage_limit_reached")
	_ = doc.Set("error.code", "model_cooldown")
	_ = doc.Set("error.message", message)

	_ = doc.Set("error.resets_at", info.ResetsAt)
	_ = doc.Set("error.resets_in_seconds", info.ResetSeconds)
	_ = doc.Set("error.reset_seconds", info.ResetSeconds)
	if info.Model != "" {
		_ = doc.Set("error.model", info.Model)
	}
	return doc.Bytes()
}

// CheckModelCooldownPayload transforms a model_cooldown error into a usage_limit_reached error payload if applicable.
func CheckModelCooldownPayload(status int, errText string, err error) ([]byte, bool) {
	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable && status != 0 {
		return nil, false
	}
	info := ExtractModelCooldownInfo(err, time.Now())
	if !info.IsCooldown {
		if ojson.Valid(errText) {
			code := ojson.Get(errText, "error.code").String()
			errType := ojson.Get(errText, "error.type").String()
			if strings.EqualFold(code, "model_cooldown") || strings.EqualFold(errType, "model_cooldown") {
				info.IsCooldown = true
				if sec := ojson.Get(errText, "error.reset_seconds").Int(); sec > 0 {
					info.ResetSeconds = int(sec)
				}
				if model := ojson.Get(errText, "error.model").String(); model != "" {
					info.Model = model
				}
				if msg := ojson.Get(errText, "error.message").String(); msg != "" {
					info.Message = msg
				}
				if info.ResetSeconds <= 0 {
					return nil, false
				}
				info.ResetsAt = time.Now().Add(time.Duration(info.ResetSeconds) * time.Second).Unix()
			}
		}
	}
	if !info.IsCooldown {
		return nil, false
	}
	if info.Message == "" && strings.TrimSpace(errText) != "" && !ojson.Valid(errText) {
		info.Message = strings.TrimSpace(errText)
	}
	return BuildModelCooldownUsageLimitError(info), true
}
