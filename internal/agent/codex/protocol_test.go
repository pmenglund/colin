package codex

import "testing"

func TestExtractUsagePrefersTotalTokenUsageOverLastTokenUsage(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"usage": map[string]any{
				"last_token_usage": map[string]any{
					"input_tokens":  100,
					"output_tokens": 25,
					"total_tokens":  125,
				},
				"total_token_usage": map[string]any{
					"input_tokens":  1200,
					"output_tokens": 300,
					"total_tokens":  1500,
				},
			},
		},
	}

	got := extractUsage(msg)
	if got == nil {
		t.Fatal("extractUsage() returned nil")
	}
	if got["input_tokens"] != 1200 {
		t.Fatalf("input_tokens = %d, want 1200", got["input_tokens"])
	}
	if got["output_tokens"] != 300 {
		t.Fatalf("output_tokens = %d, want 300", got["output_tokens"])
	}
	if got["total_tokens"] != 1500 {
		t.Fatalf("total_tokens = %d, want 1500", got["total_tokens"])
	}
}

func TestExtractUsageIgnoresLastTokenUsageWithoutTotals(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"usage": map[string]any{
				"last_token_usage": map[string]any{
					"input_tokens":  100,
					"output_tokens": 25,
					"total_tokens":  125,
				},
			},
		},
	}

	if got := extractUsage(msg); got != nil {
		t.Fatalf("extractUsage() = %#v, want nil", got)
	}
}

func TestExtractUsageFallsBackToDirectTokenTotals(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"method": "thread/tokenUsage/updated",
		"params": map[string]any{
			"input_tokens":  900,
			"output_tokens": 100,
			"total_tokens":  1000,
		},
	}

	got := extractUsage(msg)
	if got == nil {
		t.Fatal("extractUsage() returned nil")
	}
	if got["input_tokens"] != 900 {
		t.Fatalf("input_tokens = %d, want 900", got["input_tokens"])
	}
	if got["output_tokens"] != 100 {
		t.Fatalf("output_tokens = %d, want 100", got["output_tokens"])
	}
	if got["total_tokens"] != 1000 {
		t.Fatalf("total_tokens = %d, want 1000", got["total_tokens"])
	}
}
