package claude

import (
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
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

			output, errTranslate := ConvertClaudeRequestToGemini(modelName, inputJSON, false)
			if errTranslate != nil {
				t.Fatal(errTranslate)
			}
			functionResponse := ojson.GetBytesNoCopy(output, "contents.1.parts.0.functionResponse")
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

func TestConvertClaudeRequestToGemini_PreservesToolResultErrors(t *testing.T) {
	tests := []struct {
		name         string
		fields       string
		wantResponse string
		wantImage    bool
	}{
		{
			name:         "success_without_flag",
			fields:       `"content":"permission denied"`,
			wantResponse: `{"result":"permission denied"}`,
		},
		{
			name:         "explicit_success",
			fields:       `"is_error":false,"content":{"ok":true}`,
			wantResponse: `{"result":{"ok":true}}`,
		},
		{
			name:         "failed_string",
			fields:       `"is_error":true,"content":"permission denied"`,
			wantResponse: `{"error":"permission denied"}`,
		},
		{
			name:         "failed_structured_result",
			fields:       `"is_error":true,"content":{"code":"EACCES","retryable":false}`,
			wantResponse: `{"error":{"code":"EACCES","retryable":false}}`,
		},
		{
			name:         "failed_text_and_image",
			fields:       `"is_error":true,"content":[{"type":"text","text":"permission denied"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]`,
			wantResponse: `{"error":{"type":"text","text":"permission denied"}}`,
			wantImage:    true,
		},
		{
			name:         "failed_multiple_blocks",
			fields:       `"is_error":true,"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]`,
			wantResponse: `{"error":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`,
		},
		{
			name:         "failed_without_content",
			fields:       `"is_error":true`,
			wantResponse: `{"error":""}`,
		},
		{
			name:         "failed_schema_ref_remains_opaque",
			fields:       `"is_error":true,"content":{"$ref":"#/definitions/Error"}`,
			wantResponse: `{"error":"{\"$ref\":\"#/definitions/Error\"}"}`,
		},
	}
	for _, entrypoint := range []struct {
		name      string
		translate func(string, []byte, bool) ([]byte, error)
	}{
		{"native", ConvertClaudeRequestToGemini},
		{"compat", ConvertClaudeRequestToGeminiWithCompat},
	} {
		for _, stream := range []bool{false, true} {
			for _, tt := range tests {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", entrypoint.name, stream, tt.name), func(t *testing.T) {
					input := []byte(fmt.Sprintf(`{"messages":[
						{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]},
						{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1",%s}]}
					]}`, tt.fields))
					output, errTranslate := entrypoint.translate("gemini-3-flash-preview", input, stream)
					if errTranslate != nil {
						t.Fatal(errTranslate)
					}
					if !ojson.ValidBytes(output) {
						t.Fatalf("invalid translated JSON: %s", output)
					}
					response := ojson.GetBytesNoCopy(output, "contents.1.parts.0.functionResponse")
					if response.Get("id").String() != "call_1" || response.Get("name").String() != "Read" {
						t.Fatalf("tool result lost its call identity: %s", response.Raw)
					}
					if got := response.Get("response").Raw; got != tt.wantResponse {
						t.Fatalf("functionResponse.response = %s, want %s", got, tt.wantResponse)
					}
					parts := ojson.GetBytesNoCopy(output, "contents.1.parts").Array()
					if len(parts) != 1 {
						t.Fatalf("tool result parts = %d, want 1: %s", len(parts), ojson.GetBytesNoCopy(output, "contents.1").Raw)
					}
					if tt.wantImage {
						if got := response.Get("parts.0.inlineData").Raw; got != `{"mimeType":"image/png","data":"aGVsbG8="}` {
							t.Fatalf("functionResponse.parts[0].inlineData = %s", got)
						}
					} else if response.Get("parts").Exists() {
						t.Fatalf("unexpected functionResponse.parts: %s", response.Get("parts").Raw)
					}
				})
			}
		}
	}
}
