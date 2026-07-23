package llm

import (
	"fmt"
	"math"
	"strings"
)

type UsageReport struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	DurationMs       int64   `json:"duration_ms"`
}

func CalculateCost(modelID string, usage UsageReport) UsageReport {
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheWriteTokens + usage.CacheReadTokens

	if modelID == "" {
		return usage
	}

	if strings.HasSuffix(strings.ToLower(modelID), ":free") {
		usage.TotalCostUSD = 0.0
		return usage
	}

	_, provider := extractProvider(modelID)

	if provider == "ollama" {
		usage.TotalCostUSD = 0.0
		return usage
	}

	meta := GetModelMetadata(modelID)
	if meta == nil {
		return usage
	}

	inputCost := (float64(usage.InputTokens) / 1_000_000) * meta.InputCostPerM
	outputCost := (float64(usage.OutputTokens) / 1_000_000) * meta.OutputCostPerM
	cacheWriteCost := (float64(usage.CacheWriteTokens) / 1_000_000) * meta.CacheWriteCostPerM
	cacheReadCost := (float64(usage.CacheReadTokens) / 1_000_000) * meta.CacheReadCostPerM

	usage.TotalCostUSD = roundTo(inputCost+outputCost+cacheWriteCost+cacheReadCost, 8)

	return usage
}

func CostFromOpenRouter(cost float64, usage UsageReport) UsageReport {
	usage.TotalCostUSD = cost
	return usage
}

func extractProvider(modelID string) (string, string) {
	meta := GetModelMetadata(modelID)
	if meta != nil {
		return meta.ID, meta.Provider
	}
	return modelID, ""
}

func EnforceFreeModelOverride(modelID string, totalCostUSD float64) float64 {
	if modelID == "" {
		return totalCostUSD
	}
	if strings.HasSuffix(strings.ToLower(modelID), ":free") {
		return 0.0
	}
	_, provider := extractProvider(modelID)
	if provider == "ollama" {
		return 0.0
	}
	return totalCostUSD
}

func FormatCost(costUSD float64) string {
	if costUSD == 0.0 {
		return "$free"
	}
	return fmt.Sprintf("$%.4f", costUSD)
}

func roundTo(v float64, decimals int) float64 {
	pow := math.Pow10(decimals)
	return math.Round(v*pow) / pow
}
