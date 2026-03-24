package thinking

import "testing"

func TestNormalizeCodexModelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-5.4-high", "gpt-5.4"},
		{"gpt-5.4-low", "gpt-5.4"},
		{"gpt-5.4-medium", "gpt-5.4"},
		{"gpt-5.4-xhigh", "gpt-5.4"},
		{"gpt-5.4", "gpt-5.4"},
		{"gpt-5.4(high)", "gpt-5.4"},
		{"gpt-5.4(8192)", "gpt-5.4"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"custom-model-high", "custom-model"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeCodexModelName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeCodexModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
