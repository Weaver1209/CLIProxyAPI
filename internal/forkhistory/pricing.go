package forkhistory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PricingResolver resolves token pricing across local overrides and cached model catalogs.
type PricingResolver struct {
	mu           sync.RWMutex
	cacheDir     string
	overrideFile string

	overrides map[string]ModelPricing
	cached    map[string]ModelPricing
}

// NewPricingResolver creates an initialized resolver.
func NewPricingResolver(cacheDir, overrideFile string) *PricingResolver {
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			cacheDir = filepath.Join(home, ".cache", "cliproxy")
		} else {
			cacheDir = filepath.Join(os.TempDir(), "cliproxy-cache")
		}
	}
	_ = os.MkdirAll(cacheDir, 0o755)

	pr := &PricingResolver{
		cacheDir:     cacheDir,
		overrideFile: overrideFile,
		overrides:    make(map[string]ModelPricing),
		cached:       make(map[string]ModelPricing),
	}

	pr.loadOverrides()
	pr.loadCachedCatalogs()
	return pr
}

func (pr *PricingResolver) loadOverrides() {
	if pr.overrideFile == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			defaultPath := filepath.Join(home, ".cli-proxy-api", "pricing_overrides.json")
			if _, err := os.Stat(defaultPath); err == nil {
				pr.overrideFile = defaultPath
			}
		}
	}
	if pr.overrideFile == "" {
		return
	}

	data, err := os.ReadFile(pr.overrideFile)
	if err != nil {
		return
	}

	var raw map[string]struct {
		InputCostPerToken        *float64 `json:"input_cost_per_token"`
		OutputCostPerToken       *float64 `json:"output_cost_per_token"`
		CacheReadCostPerToken    *float64 `json:"cache_read_cost_per_token"`
		CacheWriteCostPerToken   *float64 `json:"cache_write_cost_per_token"`
		CacheWrite1hCostPerToken *float64 `json:"cache_write_1h_cost_per_token"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()
	for k, v := range raw {
		pr.overrides[strings.ToLower(k)] = ModelPricing{
			InputCostPerToken:        v.InputCostPerToken,
			OutputCostPerToken:       v.OutputCostPerToken,
			CacheReadCostPerToken:    v.CacheReadCostPerToken,
			CacheWriteCostPerToken:   v.CacheWriteCostPerToken,
			CacheWrite1hCostPerToken: v.CacheWrite1hCostPerToken,
			Source:                   "local_override",
		}
	}
}

func (pr *PricingResolver) loadCachedCatalogs() {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	paths := []string{
		filepath.Join(pr.cacheDir, "models_dev_pricing.json"),
		filepath.Join(pr.cacheDir, "litellm_pricing.json"),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var catalog map[string]ModelPricing
		if json.Unmarshal(data, &catalog) == nil {
			for k, v := range catalog {
				pr.cached[strings.ToLower(k)] = v
			}
		}
	}
}

// SetLocalOverride directly registers an in-memory override.
func (pr *PricingResolver) SetLocalOverride(model string, pricing ModelPricing) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pricing.Source = "local_override"
	pr.overrides[strings.ToLower(model)] = pricing
}

// ResolveModelPricing resolves pricing by consulting local overrides then cached catalogs.
// If unknown, returned pointers are nil. Unknown models are NULL, never 0.
func (pr *PricingResolver) ResolveModelPricing(provider, model string) (*ModelPricing, bool) {
	if model == "" {
		return nil, false
	}
	canonical := strings.ToLower(strings.TrimSpace(model))

	pr.mu.RLock()
	defer pr.mu.RUnlock()

	if p, ok := pr.overrides[canonical]; ok {
		return &p, true
	}
	if slash := strings.Index(canonical, "/"); slash != -1 {
		short := canonical[slash+1:]
		if p, ok := pr.overrides[short]; ok {
			return &p, true
		}
	}

	if p, ok := pr.cached[canonical]; ok {
		return &p, true
	}
	if slash := strings.Index(canonical, "/"); slash != -1 {
		short := canonical[slash+1:]
		if p, ok := pr.cached[short]; ok {
			return &p, true
		}
	}

	return nil, false
}

// CalculateRowCosts prices token breakdown according to resolved model pricing.
// Returns nil pointers (NULL) if model pricing is not found or component pricing is unknown.
func (pr *PricingResolver) CalculateRowCosts(provider, model string, uncachedInput, cacheRead, cacheWrite, cacheWrite1h, outputTokens int64) (
	inputCost *float64,
	outputCost *float64,
	cacheReadCost *float64,
	cacheWriteCost *float64,
	totalCost *float64,
	cacheSavings *float64,
	pricingSource *string,
) {
	pricing, found := pr.ResolveModelPricing(provider, model)
	if !found || pricing == nil {
		return nil, nil, nil, nil, nil, nil, nil
	}

	pricingSource = &pricing.Source

	write1hRate := pricing.CacheWrite1hCostPerToken
	if write1hRate == nil && pricing.InputCostPerToken != nil && (strings.Contains(strings.ToLower(model), "claude") || strings.EqualFold(provider, "anthropic")) {
		val := *pricing.InputCostPerToken * 2.0
		write1hRate = &val
	}

	var hasFullPricing = true

	if pricing.InputCostPerToken != nil {
		val := float64(uncachedInput) * (*pricing.InputCostPerToken)
		inputCost = &val
	} else if uncachedInput > 0 {
		hasFullPricing = false
	}

	if pricing.OutputCostPerToken != nil {
		val := float64(outputTokens) * (*pricing.OutputCostPerToken)
		outputCost = &val
	} else if outputTokens > 0 {
		hasFullPricing = false
	}

	if pricing.CacheReadCostPerToken != nil {
		val := float64(cacheRead) * (*pricing.CacheReadCostPerToken)
		cacheReadCost = &val
	} else if cacheRead > 0 {
		if pricing.InputCostPerToken != nil && (strings.Contains(strings.ToLower(model), "claude") || strings.EqualFold(provider, "anthropic")) {
			val := float64(cacheRead) * (*pricing.InputCostPerToken * 0.10)
			cacheReadCost = &val
		} else {
			hasFullPricing = false
		}
	}

	standardWriteTokens := cacheWrite - cacheWrite1h
	if standardWriteTokens < 0 {
		standardWriteTokens = 0
	}

	var standardWriteCostVal float64
	var write1hCostVal float64
	var writePriced = true

	if standardWriteTokens > 0 {
		if pricing.CacheWriteCostPerToken != nil {
			standardWriteCostVal = float64(standardWriteTokens) * (*pricing.CacheWriteCostPerToken)
		} else if pricing.InputCostPerToken != nil && (strings.Contains(strings.ToLower(model), "claude") || strings.EqualFold(provider, "anthropic")) {
			standardWriteCostVal = float64(standardWriteTokens) * (*pricing.InputCostPerToken * 1.25)
		} else {
			writePriced = false
		}
	}

	if cacheWrite1h > 0 {
		if write1hRate != nil {
			write1hCostVal = float64(cacheWrite1h) * (*write1hRate)
		} else {
			writePriced = false
		}
	}

	if writePriced && (standardWriteTokens > 0 || cacheWrite1h > 0) {
		val := standardWriteCostVal + write1hCostVal
		cacheWriteCost = &val
	} else if (standardWriteTokens > 0 || cacheWrite1h > 0) && !writePriced {
		hasFullPricing = false
	}

	if hasFullPricing {
		var sum float64
		if inputCost != nil {
			sum += *inputCost
		}
		if outputCost != nil {
			sum += *outputCost
		}
		if cacheReadCost != nil {
			sum += *cacheReadCost
		}
		if cacheWriteCost != nil {
			sum += *cacheWriteCost
		}
		totalCost = &sum
	}

	if cacheRead > 0 && pricing.InputCostPerToken != nil && cacheReadCost != nil {
		nominalCost := float64(cacheRead) * (*pricing.InputCostPerToken)
		savings := nominalCost - *cacheReadCost
		if savings >= 0 {
			cacheSavings = &savings
		} else {
			zero := 0.0
			cacheSavings = &zero
		}
	}

	return inputCost, outputCost, cacheReadCost, cacheWriteCost, totalCost, cacheSavings, pricingSource
}
