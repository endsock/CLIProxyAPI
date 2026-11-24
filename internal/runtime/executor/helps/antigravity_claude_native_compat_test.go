package helps

import (
	"bytes"
	"context"
	"testing"

	gjson "github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
			wantText := gjson.GetBytes(agyNativeSystemInstructionObject, "parts.0.text").String() + "\n<user_rules>\nThe following are user-defined rules that you MUST ALWAYS FOLLOW WITHOUT ANY EXCEPTION. These rules take precedence over any other instructions.\nReview them carefully and always take them into account when you generate responses and code:\n<RULE[user_global]>\nextra\n</RULE[user_global]>\n</user_rules>"
			if len(parts) != 1 || parts[0].Get("text").String() != wantText {
				t.Fatalf("native system or extra prompt changed: %s", out)
			}
			unmarked := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatOpenAI, cfg)
			if !bytes.Equal(RewriteRequestTools(unmarked, payload), payload) {
				t.Fatal("system rewrite affected a non-Claude client")
			}
		})
	}
}

func TestNativeToolCompatibilityPiSystemMatchesDefault(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"client system"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	for _, extra := range []string{"", "extra"} {
		cfg := &config.Config{Codex: config.CodexConfig{ExtraSystemPrompt: extra}}
		defaultCtx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg)
		piCtx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg, "pi (test)")
		if got, want := RewriteRequestTools(piCtx, payload), RewriteRequestTools(defaultCtx, payload); !bytes.Equal(got, want) {
			t.Fatalf("Pi system differs from default for extra %q: got %s, want %s", extra, got, want)
		}
	}
}

func TestNativeToolCompatibilityClaudeCodeSystemName(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Claude Code and CLAUDE CODE","cache_control":{"type":"ephemeral"}},{"text":"claude code and Claude code"}]},"contents":[]}}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "claude")
	out := RewriteRequestTools(ctx, payload)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "antigravity and antigravity" || parts[1].Get("text").String() != "antigravity and antigravity" || parts[0].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("unexpected Claude Code system parts: %s", out)
	}
	piCtx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "pi (test)")
	if got := gjson.GetBytes(RewriteRequestTools(piCtx, payload), "request.contents.0.parts.0.text").String(); got != "Claude Code and CLAUDE CODE" {
		t.Fatalf("Pi system text changed: %q", got)
	}
}

func TestNativeToolCompatibilityPiSystemRemovesIntro(t *testing.T) {
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Pi intro\n\n<tools>tools</tools>","cache_control":{"type":"ephemeral"}},{"text":"Second intro <rules>rules</rules>"}]},"contents":[]}}`)
	for _, piCtx := range []context.Context{
		AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil),
		AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "pi (test)"),
	} {
		out := RewriteRequestTools(piCtx, payload)
		parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("text").String() != "<tools>tools</tools>" || parts[1].Get("text").String() != "Second intro <rules>rules</rules>" || parts[0].Get("cache_control.type").String() != "ephemeral" {
			t.Fatalf("unexpected Pi system parts: %s", out)
		}
	}
	claudeCtx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "claude")
	if got := gjson.GetBytes(RewriteRequestTools(claudeCtx, payload), "request.contents.0.parts.0.text").String(); got != "Pi intro\n\n<tools>tools</tools>" {
		t.Fatalf("claude system intro changed: %q", got)
	}
}

func TestNativeToolCompatibilityWithoutExtraPrompt(t *testing.T) {
	payload := []byte(`{"request":{"contents":[]}}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	out := RewriteRequestTools(ctx, payload)
	if got := gjson.GetBytes(out, "request.systemInstruction").Raw; got != string(agyNativeSystemInstructionObject) {
		t.Fatalf("native system changed without extra prompt: %s", out)
	}
}

func TestNativeToolCompatibilityPreservesIndependentWebSearch(t *testing.T) {
	payload := []byte(`{"requestType":"web_search","request":{"systemInstruction":{"role":"user","parts":[{"text":"search instruction"}]},"tools":[{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}],"contents":[{"role":"user","parts":[{"text":"北京天气"}]}]}}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	out := RewriteRequestTools(ctx, payload)
	if !bytes.Equal(out, payload) {
		t.Fatalf("native web-search request was rewritten: %s", out)
	}
	if !gjson.GetBytes(out, "request.tools.0.googleSearch").Exists() {
		t.Fatal("googleSearch tool was removed from native web-search request")
	}
}

func TestNativeToolCompatibilityKeepsHistoryAndMCP(t *testing.T) {
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"PowerShell"},{"name":"mcp__test__call","parameters":{"type":"OBJECT"}}]}],"contents":[{"role":"model","parts":[{"functionCall":{"name":"Read","args":{"file_path":"/tmp/a"}}}]}]}}`)
	cfg := &config.Config{Codex: config.CodexConfig{ExtraSystemPrompt: "extra"}}
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg, "claude")
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
	if len(parts.Array()) != 1 {
		t.Fatal("extra prompt created a separate system part")
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
