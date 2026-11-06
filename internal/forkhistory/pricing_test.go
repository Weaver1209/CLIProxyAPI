package forkhistory

import (
	"math"
	"testing"
)

func TestPricingNullForUnknownModel(t *testing.T) {
	pr := NewPricingResolver(t.TempDir(), "")

	inCost, outCost, crCost, cwCost, totCost, savings, source := pr.CalculateRowCosts(
		"unknown_provider",
		"gemini-unknown-model-xyz",
		1000, 500, 200, 0, 500,
	)

	if inCost != nil {
		t.Fatalf("expected nil inputCost for unknown model, got %v", *inCost)
	}
	if outCost != nil {
		t.Fatalf("expected nil outputCost for unknown model, got %v", *outCost)
	}
	if crCost != nil {
		t.Fatalf("expected nil cacheReadCost for unknown model, got %v", *crCost)
	}
	if cwCost != nil {
		t.Fatalf("expected nil cacheWriteCost for unknown model, got %v", *cwCost)
	}
	if totCost != nil {
		t.Fatalf("expected nil totalCost for unknown model, got %v", *totCost)
	}
	if savings != nil {
		t.Fatalf("expected nil savings for unknown model, got %v", *savings)
	}
	if source != nil {
		t.Fatalf("expected nil source for unknown model, got %v", *source)
	}
}

func TestPricingClaude1hCacheWritesAndSavings(t *testing.T) {
	pr := NewPricingResolver(t.TempDir(), "")

	inRate := 0.000003   // $3 / MTok
	outRate := 0.000015  // $15 / MTok
	crRate := 0.0000003  // $0.30 / MTok (10%)
	cwRate := 0.00000375 // $3.75 / MTok (1.25x)
	cw1hRate := 0.000006 // $6.00 / MTok (2.0x)

	pr.SetLocalOverride("claude-3-7-sonnet", ModelPricing{
		InputCostPerToken:        &inRate,
		OutputCostPerToken:       &outRate,
		CacheReadCostPerToken:    &crRate,
		CacheWriteCostPerToken:   &cwRate,
		CacheWrite1hCostPerToken: &cw1hRate,
	})

	uncachedInput := int64(10_000)
	cacheRead := int64(20_000)
	cacheWrite := int64(5_000)
	cacheWrite1h := int64(2_000) // 2k 1h write, 3k standard write
	outputTokens := int64(1_000)

	inCost, outCost, crCost, cwCost, totCost, savings, source := pr.CalculateRowCosts(
		"anthropic",
		"claude-3-7-sonnet",
		uncachedInput,
		cacheRead,
		cacheWrite,
		cacheWrite1h,
		outputTokens,
	)

	if inCost == nil || math.Abs(*inCost-0.03) > 1e-9 {
		t.Fatalf("expected inCost 0.03, got %v", inCost)
	}
	if outCost == nil || math.Abs(*outCost-0.015) > 1e-9 {
		t.Fatalf("expected outCost 0.015, got %v", outCost)
	}
	if crCost == nil || math.Abs(*crCost-0.006) > 1e-9 {
		t.Fatalf("expected crCost 0.006, got %v", crCost)
	}

	expectedCw := (3000.0 * cwRate) + (2000.0 * cw1hRate)
	if cwCost == nil || math.Abs(*cwCost-expectedCw) > 1e-9 {
		t.Fatalf("expected cwCost %v, got %v", expectedCw, cwCost)
	}

	expectedTot := 0.03 + 0.015 + 0.006 + expectedCw
	if totCost == nil || math.Abs(*totCost-expectedTot) > 1e-9 {
		t.Fatalf("expected totCost %v, got %v", expectedTot, totCost)
	}

	expectedSavings := 0.054
	if savings == nil || math.Abs(*savings-expectedSavings) > 1e-9 {
		t.Fatalf("expected savings %v, got %v", expectedSavings, savings)
	}

	if source == nil || *source != "local_override" {
		t.Fatalf("expected source local_override, got %v", source)
	}
}
