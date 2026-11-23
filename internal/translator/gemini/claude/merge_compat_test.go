package claude

import (
	"fmt"
	"strings"
	"testing"

	gjson "github.com/MoeForge/moego/ojson"
)

func TestMergeCompatibilityToolResultRefsAndErrors(t *testing.T) {
	for _, content := range []string{
		`{"$ref":"#/definitions/tool"}`,
		`[{"type":"text","text":"schema","schema":{"$ref":"#/definitions/tool"}}]`,
	} {
		for _, failed := range []bool{false, true} {
			input := []byte(fmt.Sprintf(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","is_error":%t,"content":%s}]}]}`, failed, content))
			output, errTranslate := ConvertClaudeRequestToGemini("gemini-3.8-flash", input, false)
			if errTranslate != nil {
				t.Fatal(errTranslate)
			}
			if !gjson.ValidBytes(output) {
				t.Fatalf("invalid JSON: %s", output)
			}
			response := gjson.GetBytes(output, "contents.1.parts.0.functionResponse.response")
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
