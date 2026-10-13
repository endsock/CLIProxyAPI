package helps

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeToolCompatibilityPrependsSystemAsUser(t *testing.T) {
	const history = `[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"ok"}]}]`
	for _, tc := range []struct {
		name       string
		system     string
		contents   string
		wantSystem bool
	}{
		{"multiple text blocks", `{"role":"user","parts":[{"text":"First rule"},{"text":"Second rule"}]}`, history, true},
		{"no history", `{"parts":[{"text":"First rule"},{"text":"Second rule"}]}`, `[]`, true},
		{"missing system", `null`, history, false},
		{"empty system", `{"parts":[{}, {"text":""}]}`, history, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"request":{"systemInstruction":` + tc.system + `,"contents":` + tc.contents + `}}`)
			cfg := &config.Config{Codex: config.CodexConfig{ExtraSystemPrompt: "extra"}}
			ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg)
			out := RewriteRequestTools(ctx, payload)
			contents := gjson.GetBytes(out, "request.contents").Array()
			original := gjson.Parse(tc.contents).Array()
			offset := 0
			if tc.wantSystem {
				offset = 1
				if len(contents) == 0 || contents[0].Get("role").String() != "user" {
					t.Fatalf("system was not prepended as user: %s", out)
				}
				parts := contents[0].Get("parts").Array()
				if len(parts) != 2 || parts[0].Get("text").String() != "First rule" || parts[1].Get("text").String() != "Second rule" {
					t.Fatalf("system text blocks changed: %s", out)
				}
			}
			if len(contents) != len(original)+offset {
				t.Fatalf("unexpected contents count: %s", out)
			}
			for i, item := range original {
				if contents[i+offset].Raw != item.Raw {
					t.Fatalf("history item %d changed: %s", i, out)
				}
			}
			parts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
			if len(parts) != 2 || parts[0].Raw != gjson.GetBytes(agyNativeSystemInstructionObject, "parts.0").Raw || parts[1].Get("text").String() != "extra" {
				t.Fatalf("native system or extra prompt changed: %s", out)
			}
			unmarked := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatOpenAI, cfg)
			if !bytes.Equal(RewriteRequestTools(unmarked, payload), payload) {
				t.Fatal("system rewrite affected a non-Claude client")
			}
		})
	}
}

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
