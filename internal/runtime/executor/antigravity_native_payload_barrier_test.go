package executor

import (
	"context"
	"io"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityNativeToolsRespectPayloadBarrier(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-*"}},
			Params: map[string]any{"systemInstruction.parts.0.text": "configured system"},
		}},
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-*"}},
			Params: []string{"tools", "contents.0"},
		}},
	}}
	body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"original system"}]},"tools":[{"functionDeclarations":[{"name":"Bash"}]}],"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	req := cliproxyexecutor.Request{Model: "gemini-3.8-flash-high", Payload: body}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-test"}}
	executor := NewAntigravityExecutor(cfg)
	for _, stream := range []bool{false, true} {
		name := "unary"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
			ctx := helps.AnnotateClaudeNativeTools(context.Background(), opts.SourceFormat, cfg)
			ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", body, req, opts))
			request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, body, stream, "", "https://example.invalid")
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			wire, errRead := io.ReadAll(request.Body)
			if errClose := request.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if errRead != nil {
				t.Fatal(errRead)
			}
			if request.Header.Get("User-Agent") != "antigravity/cli/1.1.22 (aidev_client; os_type=windows; arch=amd64; cl=971564011; auth_method=consumer)" {
				t.Fatal("native-tool compatibility was not activated")
			}
			if ojson.GetBytesNoCopy(wire, "request.tools").Exists() {
				t.Fatalf("native tools restored a filtered field: %s", wire)
			}
			if got := ojson.GetBytesNoCopy(wire, "request.systemInstruction.parts.0.text").String(); got != "configured system" {
				t.Fatalf("native prompt overwrote configured system: %s", wire)
			}
			if ojson.GetBytesNoCopy(wire, "request.contents.#").Int() != 1 || ojson.GetBytesNoCopy(wire, "request.contents.0.parts.0.text").String() != "hello" {
				t.Fatalf("contents filter did not run exactly once after native rewriting: %s", wire)
			}
		})
	}
}
