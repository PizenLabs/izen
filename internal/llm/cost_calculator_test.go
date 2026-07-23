package llm

import (
	"math"
	"testing"
)

func round(v float64, decimals int) float64 {
	pow := math.Pow10(decimals)
	return math.Round(v*pow) / pow
}

func TestCalculateCostClaudeSonnetNoCache(t *testing.T) {
	usage := UsageReport{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	result := CalculateCost("claude-3-5-sonnet-20241022", usage)
	if result.TotalTokens != 1500 {
		t.Errorf("TotalTokens = %d, want 1500", result.TotalTokens)
	}
	// (1000/1e6)*3 + (500/1e6)*15 = 0.003 + 0.0075 = 0.0105
	want := round((1000.0/1e6)*3+(500.0/1e6)*15, 8)
	if result.TotalCostUSD != want {
		t.Errorf("TotalCostUSD = %f, want %f", result.TotalCostUSD, want)
	}
}

func TestCalculateCostClaudeSonnetWithCache(t *testing.T) {
	usage := UsageReport{
		InputTokens:      1000,
		OutputTokens:     500,
		CacheWriteTokens: 2000,
		CacheReadTokens:  3000,
	}
	result := CalculateCost("claude-3-5-sonnet-20241022", usage)
	if result.TotalTokens != 6500 {
		t.Errorf("TotalTokens = %d, want 6500", result.TotalTokens)
	}
	want := round(
		(1000.0/1e6)*3+ // input
			(500.0/1e6)*15+ // output
			(2000.0/1e6)*3.75+ // cache write
			(3000.0/1e6)*0.30, // cache read
		8,
	)
	if result.TotalCostUSD != want {
		t.Errorf("TotalCostUSD = %f, want %f", result.TotalCostUSD, want)
	}
}

func TestCalculateCostOllamaForceZero(t *testing.T) {
	usage := UsageReport{
		InputTokens:  5000,
		OutputTokens: 2000,
	}
	result := CalculateCost("qwen2.5-coder:7b", usage)
	if result.TotalCostUSD != 0.0 {
		t.Errorf("Ollama TotalCostUSD = %f, want 0.0", result.TotalCostUSD)
	}
	if result.TotalTokens != 7000 {
		t.Errorf("TotalTokens = %d, want 7000", result.TotalTokens)
	}
}

func TestCalculateCostGPT4o(t *testing.T) {
	usage := UsageReport{
		InputTokens:  2000,
		OutputTokens: 1000,
	}
	result := CalculateCost("gpt-4o", usage)
	want := round((2000.0/1e6)*2.50+(1000.0/1e6)*10, 8)
	if result.TotalCostUSD != want {
		t.Errorf("TotalCostUSD = %f, want %f", result.TotalCostUSD, want)
	}
}

func TestCalculateCostDeepSeekChat(t *testing.T) {
	usage := UsageReport{
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	result := CalculateCost("deepseek-chat", usage)
	want := round((10000.0/1e6)*0.27+(5000.0/1e6)*1.10, 8)
	if result.TotalCostUSD != want {
		t.Errorf("TotalCostUSD = %f, want %f", result.TotalCostUSD, want)
	}
}

func TestCalculateCostGeminiPro(t *testing.T) {
	usage := UsageReport{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	result := CalculateCost("gemini-1.5-pro", usage)
	want := round((1000.0/1e6)*1.25+(500.0/1e6)*5, 8)
	if result.TotalCostUSD != want {
		t.Errorf("TotalCostUSD = %f, want %f", result.TotalCostUSD, want)
	}
}

func TestCalculateCostUnknownModel(t *testing.T) {
	usage := UsageReport{
		InputTokens:  100,
		OutputTokens: 50,
	}
	result := CalculateCost("nonexistent-model-v42", usage)
	if result.TotalCostUSD != 0.0 {
		t.Errorf("Unknown model TotalCostUSD = %f, want 0.0", result.TotalCostUSD)
	}
	if result.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", result.TotalTokens)
	}
}

func TestCalculateCostEmptyModelID(t *testing.T) {
	usage := UsageReport{
		InputTokens:  100,
		OutputTokens: 50,
	}
	result := CalculateCost("", usage)
	if result.TotalCostUSD != 0.0 {
		t.Errorf("Empty model TotalCostUSD = %f, want 0.0", result.TotalCostUSD)
	}
}

func TestCalculateCostOpenRouterDynamic(t *testing.T) {
	usage := UsageReport{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	orCost := 0.0085
	result := CostFromOpenRouter(orCost, usage)
	if result.TotalCostUSD != orCost {
		t.Errorf("OpenRouter TotalCostUSD = %f, want %f", result.TotalCostUSD, orCost)
	}
}

func TestCalculateCostZeroTokens(t *testing.T) {
	usage := UsageReport{}
	result := CalculateCost("gpt-4o", usage)
	if result.TotalCostUSD != 0.0 {
		t.Errorf("Zero tokens TotalCostUSD = %f, want 0.0", result.TotalCostUSD)
	}
	if result.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0", result.TotalTokens)
	}
}

func TestGetModelMetadataFound(t *testing.T) {
	meta := GetModelMetadata("gpt-4o")
	if meta == nil {
		t.Fatal("GetModelMetadata returned nil for gpt-4o")
	}
	if meta.InputCostPerM != 2.50 {
		t.Errorf("InputCostPerM = %f, want 2.50", meta.InputCostPerM)
	}
	if meta.OutputCostPerM != 10 {
		t.Errorf("OutputCostPerM = %f, want 10", meta.OutputCostPerM)
	}
}

func TestGetModelMetadataNotFound(t *testing.T) {
	meta := GetModelMetadata("custom-model-42")
	if meta != nil {
		t.Errorf("GetModelMetadata should return nil for unknown, got %+v", meta)
	}
}

func TestUsageReportFormats(t *testing.T) {
	usage := CalculateCost("claude-3-5-sonnet-20241022", UsageReport{
		InputTokens:  100,
		OutputTokens: 50,
	})
	if usage.TotalCostUSD <= 0 {
		t.Errorf("Expected positive cost, got %f", usage.TotalCostUSD)
	}
}
