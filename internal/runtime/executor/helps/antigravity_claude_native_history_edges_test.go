package helps

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestNativeHistoryRejectsAmbiguity prevents lossy aliases and duplicate pairs from choosing a record.
func TestNativeHistoryRejectsAmbiguity(t *testing.T) {
	const clientArgs = `{"command":"pwd","timeout":600000}`
	id := util.GeminiClaudeToolUseID("reused", "Bash", clientArgs)
	first := []byte(`{"type":"function_call_part","name":"run_command","call_id":"reused","args":{"CommandLine":"pwd","WaitMsBeforeAsync":600001}}`)
	second := []byte(`{"type":"function_call_part","name":"run_command","call_id":"reused","args":{"CommandLine":"pwd","WaitMsBeforeAsync":600002}}`)
	call := fmt.Sprintf(`{"functionCall":{"id":%q,"name":"Bash","args":%s}}`, id, clientArgs)
	result := fmt.Sprintf(`{"functionResponse":{"id":%q,"name":"Bash","response":{"result":"ok"}}}`, id)
	wrongResult := fmt.Sprintf(`{"functionResponse":{"id":%q,"name":"Read","response":{"result":"other"}}}`, id)
	for _, tt := range []struct {
		name, calls, results string
		items                [][]byte
	}{
		{"lossy aliases", call, result, [][]byte{first, second}},
		{"reversed aliases", call, result, [][]byte{second, first}},
		{"duplicate ledger", call, result, [][]byte{first, first}},
		{"third alias stays ambiguous", call, result, [][]byte{first, second, first}},
		{"duplicate calls", call + "," + call, result, [][]byte{first}},
		{"duplicate results", call, result + "," + result, [][]byte{first}},
		{"mismatched duplicate result", call, result + "," + wrongResult, [][]byte{first}},
		{"missing result", call, "", [][]byte{first}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(`{"request":{"contents":[{"role":"model","parts":[%s]},{"role":"user","parts":[%s]}]}}`, tt.calls, tt.results))
			ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
			out, err := RestoreClaudeNativeToolHistory(ctx, payload, tt.items)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != string(payload) {
				t.Fatal("ambiguous history was changed")
			}
		})
	}
}

// TestNativeHistorySurvivesShellChange uses recorded hashes, not today's shell preference.
func TestNativeHistorySurvivesShellChange(t *testing.T) {
	for _, historicalPowerShell := range []bool{false, true} {
		name, command, currentTool := "Bash", "ls -la -- '/tmp/project'", "PowerShell"
		if historicalPowerShell {
			name, command, currentTool = "PowerShell", "Get-ChildItem -Force -LiteralPath '/tmp/project'", "Bash"
		}
		t.Run(name, func(t *testing.T) {
			args, err := marshalArgsNoHTMLEscape(map[string]any{"command": command, "description": "List directory contents"})
			if err != nil {
				t.Fatal(err)
			}
			id := util.GeminiClaudeToolUseID("native-list", name, string(args))
			payload := []byte(fmt.Sprintf(`{"request":{"tools":[{"functionDeclarations":[{"name":%q}]}],"contents":[{"role":"model","parts":[{"functionCall":{"id":%q,"name":%q,"args":%s}}]},{"role":"user","parts":[{"functionResponse":{"id":%q,"name":%q,"response":{"result":"ok"}}}]}]}}`, currentTool, id, name, args, id, name))
			item := []byte(`{"type":"function_call_part","name":"list_dir","call_id":"native-list","args":{"DirectoryPath":"/tmp/project"}}`)
			ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
			out, err := RestoreClaudeNativeToolHistory(ctx, payload, [][]byte{item})
			if err != nil {
				t.Fatal(err)
			}
			call := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall")
			result := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse")
			if call.Get("name").String() != "list_dir" || result.Get("name").String() != "list_dir" || call.Get("id").String() != result.Get("id").String() {
				t.Fatal("historical shell change lost native pairing")
			}
			if result.Get("response.result").String() != "ok" {
				t.Fatal("success result changed")
			}
		})
	}
}
