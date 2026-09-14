package helps

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestNativeHistoryBridgeRequiresProvenance rejects detached, edited and unrecorded calls.
func TestNativeHistoryBridgeRequiresProvenance(t *testing.T) {
	const clientArgs = `{"file_path":"/tmp/a"}`
	id := util.GeminiClaudeToolUseID("native-read", "Read", clientArgs)
	item := []byte(`{"type":"function_call_part","name":"view_file","call_id":"native-read","args":{"AbsolutePath":"/tmp/a"}}`)
	for _, tt := range []struct {
		name, args, responseName string
		missingLedger, disabled  bool
		wantRestore              bool
	}{
		{name: "paired", args: clientArgs, responseName: "Read", wantRestore: true},
		{name: "edited arguments", args: `{"file_path":"/tmp/b"}`, responseName: "Read"},
		{name: "wrong result name", args: clientArgs, responseName: "Bash"},
		{name: "missing ledger", args: clientArgs, responseName: "Read", missingLedger: true},
		{name: "unrelated provider", args: clientArgs, responseName: "Read", disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":%q,"name":"Read","args":%s}}]},{"role":"user","parts":[{"functionResponse":{"id":%q,"name":%q,"response":{"error":"permission denied"}}}]}]}}`, id, tt.args, id, tt.responseName))
			ctx := context.Background()
			if !tt.disabled {
				ctx = AnnotateClaudeNativeTools(ctx, sdktranslator.FormatClaude, nil)
			}
			items := [][]byte{item}
			if tt.missingLedger {
				items = nil
			}
			out, err := RestoreClaudeNativeToolHistory(ctx, payload, items)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.wantRestore {
				if string(out) != string(payload) {
					t.Fatal("unproven history was changed")
				}
				return
			}
			call := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall")
			result := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse")
			if call.Get("name").String() != "view_file" || result.Get("name").String() != "view_file" || call.Get("id").String() != result.Get("id").String() {
				t.Fatal("native pairing lost")
			}
			if result.Get("response.error").String() != "permission denied" {
				t.Fatal("error lost")
			}
		})
	}
}
