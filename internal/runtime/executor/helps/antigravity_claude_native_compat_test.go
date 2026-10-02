package helps

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeToolCompatibilityKeepsHistoryAndMCP(t *testing.T) {
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"PowerShell"},{"name":"mcp__test__call","parameters":{"type":"OBJECT"}}]}],"contents":[{"role":"model","parts":[{"functionCall":{"name":"Read","args":{"file_path":"/tmp/a"}}}]}]}}`)
	cfg := &config.Config{Codex: config.CodexConfig{ExtraSystemPrompt: "extra"}}
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg)
	out := RewriteRequestTools(ctx, payload)
	if gjson.GetBytes(out, "request.contents").Raw != gjson.GetBytes(payload, "request.contents").Raw {
		t.Fatal("single-direction tool mapping rewrote history")
	}
	names := make(map[string]bool)
	gjson.GetBytes(out, "request.tools").ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, declaration gjson.Result) bool {
			names[declaration.Get("name").String()] = true
			return true
		})
		return true
	})
	if !names["mcp__test__call"] || !names["view_file"] {
		t.Fatal("native catalog or MCP declaration missing")
	}
	parts := gjson.GetBytes(out, "request.systemInstruction.parts")
	if parts.Get("1.text").String() != "extra" {
		t.Fatal("extra prompt missing after native system prompt")
	}
	response := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run_command","args":{"CommandLine":"Get-ChildItem"}}}]}}]}}`)
	mapped := MapResponseFunctionCalls(ctx, response)
	if gjson.GetBytes(mapped, "response.candidates.0.content.parts.0.functionCall.name").String() != "PowerShell" {
		t.Fatalf("PowerShell preference lost: %s", mapped)
	}
	unmarked := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatOpenAI, cfg)
	if !bytes.Equal(RewriteRequestTools(unmarked, payload), payload) || !bytes.Equal(MapResponseFunctionCalls(unmarked, response), response) {
		t.Fatal("native mapping affected a non-Claude client")
	}
}
