package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertClaudeRequestToGemini_ToolResultErrorRouting verifies that failed Claude tools remain failures upstream.
func TestConvertClaudeRequestToGemini_ToolResultErrorRouting(t *testing.T) {
	const (
		modelName    = "gemini-3-flash-preview"
		toolID       = "read-call-1"
		toolName     = "Read"
		toolResponse = "EISDIR: illegal operation on a directory, read '/tmp/workspace'"
	)

	tests := []struct {
		name          string
		isErrorField  string
		wantPath      string
		forbiddenPath string
	}{
		{
			name:          "failed tool result",
			isErrorField:  `,"is_error":true`,
			wantPath:      "response.error",
			forbiddenPath: "response.result",
		},
		{
			name:          "successful tool result",
			isErrorField:  `,"is_error":false`,
			wantPath:      "response.result",
			forbiddenPath: "response.error",
		},
		{
			name:          "omitted error flag",
			wantPath:      "response.result",
			forbiddenPath: "response.error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(fmt.Sprintf(`{
				"model": %q,
				"messages": [
					{
						"role": "assistant",
						"content": [
							{"type":"tool_use","id":%q,"name":%q,"input":{"file_path":"/tmp/workspace"}}
						]
					},
					{
						"role": "user",
						"content": [
							{"type":"tool_result","tool_use_id":%q,"content":%q%s}
						]
					}
				]
			}`, modelName, toolID, toolName, toolID, toolResponse, tt.isErrorField))

			output := ConvertClaudeRequestToGemini(modelName, inputJSON, false)
			functionResponse := gjson.GetBytes(output, "contents.1.parts.0.functionResponse")
			if !functionResponse.Exists() {
				t.Fatalf("functionResponse is missing: %s", output)
			}
			if got := functionResponse.Get("id").String(); got != toolID {
				t.Fatalf("functionResponse.id = %q, want %q", got, toolID)
			}
			if got := functionResponse.Get("name").String(); got != toolName {
				t.Fatalf("functionResponse.name = %q, want %q", got, toolName)
			}
			if got := functionResponse.Get(tt.wantPath).String(); got != toolResponse {
				t.Fatalf("%s = %q, want %q; output=%s", tt.wantPath, got, toolResponse, output)
			}
			if functionResponse.Get(tt.forbiddenPath).Exists() {
				t.Fatalf("%s must be absent; output=%s", tt.forbiddenPath, output)
			}
		})
	}
}
