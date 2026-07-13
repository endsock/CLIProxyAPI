package claude

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var claudeResponseModelTests = []struct {
	name      string
	model     string
	wantModel string
}{
	{
		name:      "gpt-5.6-sol uses fable",
		model:     "gpt-5.6-sol",
		wantModel: "claude-fable-5",
	},
	{
		name:      "other model uses opus",
		model:     "gpt-5.6-terra",
		wantModel: "claude-opus-4-7",
	},
}

func TestConvertCodexResponseToClaude_ResponseCreatedModel(t *testing.T) {
	for _, tt := range claudeResponseModelTests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			rawJSON := []byte(`data: {"type":"response.created","response":{"id":"resp_123","model":"` + tt.model + `"}}`)

			result := ConvertCodexResponseToClaude(nil, "", nil, nil, rawJSON, &param)
			if len(result) != 1 || !strings.Contains(result[0], `"model":"`+tt.wantModel+`"`) {
				t.Fatalf("response = %q, want model %q", result, tt.wantModel)
			}
		})
	}
}

func TestConvertCodexResponseToClaudeNonStream_Model(t *testing.T) {
	for _, tt := range claudeResponseModelTests {
		t.Run(tt.name, func(t *testing.T) {
			rawJSON := []byte(`{"type":"response.completed","response":{"id":"resp_123","model":"` + tt.model + `","output":[]}}`)

			result := ConvertCodexResponseToClaudeNonStream(nil, "", nil, nil, rawJSON, nil)
			if got := gjson.Get(result, "model").String(); got != tt.wantModel {
				t.Fatalf("model = %q, want %q; response = %s", got, tt.wantModel, result)
			}
		})
	}
}
