package executor

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestAntigravityNativeToolReplayRoundTrip binds mapped client IDs to native ledger calls.
func TestAntigravityNativeToolReplayRoundTrip(t *testing.T) {
	const model = "gemini-3.8-flash-high"
	for _, tt := range []struct{ name, nativeArgs, clientName, clientArgs string }{
		{"list_dir", `{"DirectoryPath":"/tmp/project"}`, "Bash", `{"command":"ls -la -- '/tmp/project'","description":"List directory contents"}`},
		{"view_file", `{"AbsolutePath":"/tmp/project/CLAUDE.MD"}`, "Read", `{"file_path":"/tmp/project/CLAUDE.MD"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cache.ClearAntigravityReasoningReplayCache()
			t.Cleanup(cache.ClearAntigravityReasoningReplayCache)
			item := []byte(fmt.Sprintf(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":%q,"call_id":"native-1","args":%s,"thoughtSignature":"sig-first"}`, tt.name, tt.nativeArgs))
			if !cache.CacheAntigravityReasoningReplayItems(model, "session:roundtrip-native", [][]byte{item}) {
				t.Fatal("cache write failed")
			}
			id := util.GeminiClaudeToolUseID("native-1", tt.clientName, tt.clientArgs)
			payload := []byte(fmt.Sprintf(`{"sessionId":"roundtrip-native","request":{"tools":[{"functionDeclarations":[{"name":"Read"},{"name":"Bash"}]}],"contents":[{"role":"user","parts":[{"text":"Read attached instructions"}]},{"role":"model","parts":[{"functionCall":{"id":%q,"name":%q,"args":%s}}]},{"role":"user","parts":[{"functionResponse":{"id":%q,"name":%q,"response":{"error":"EISDIR"}}}]}]}}`, id, tt.clientName, tt.clientArgs, id, tt.clientName))
			ctx := helps.AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
			out, _, err := prepareAntigravityGeminiReasoningReplayPayload(ctx, model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, payload)
			if err != nil {
				t.Fatal(err)
			}
			call := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall")
			response := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
			if call.Get("name").String() != tt.name || response.Get("name").String() != tt.name || call.Get("id").String() != "native-1" || response.Get("id").String() != "native-1" {
				t.Fatalf("native identity not restored: call=%s response=%s", call.Raw, response.Raw)
			}
			if response.Get("response.error").String() != "EISDIR" || response.Get("response.result").Exists() {
				t.Fatal("tool error was changed")
			}
			if err := signature.ValidateGeminiFunctionCallPairing(out); err != nil {
				t.Fatal(err)
			}
		})
	}
}
