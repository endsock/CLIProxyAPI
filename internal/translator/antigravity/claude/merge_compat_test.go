package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeCompatibilityToolResultRefsAndErrors(t *testing.T) {
	for _, content := range []string{
		`{"$ref":"#/definitions/tool"}`,
		`[{"type":"text","text":"schema","schema":{"$ref":"#/definitions/tool"}}]`,
		`[{"$ref":"#/definitions/one"},{"$ref":"#/definitions/two"}]`,
	} {
		for _, failed := range []bool{false, true} {
			input := []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","is_error":%t,"content":%s}]}]}`, failed, content))
			output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-6", input, false)
			if !gjson.ValidBytes(output) {
				t.Fatalf("invalid JSON: %s", output)
			}
			response := gjson.GetBytes(output, "request.contents.1.parts.0.functionResponse.response")
			key, absent := "result", "error"
			if failed {
				key, absent = "error", "result"
			}
			value := response.Get(key)
			if value.Type != gjson.String || !strings.Contains(value.String(), `"$ref"`) || response.Get(absent).Exists() {
				t.Fatalf("ref compatibility lost error routing: %s", response.Raw)
			}
		}
	}
}
