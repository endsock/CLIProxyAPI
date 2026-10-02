package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityMergeCompatibilityHTTP(t *testing.T) {
	for _, model := range []string{"gemini-3.8-flash-high", "claude-sonnet-4-6"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", model, stream), func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					bodies <- body
					if !strings.HasPrefix(r.Header.Get("User-Agent"), "antigravity/cli/") {
						t.Errorf("native User-Agent missing: %q", r.Header.Get("User-Agent"))
					}
					response := fmt.Sprintf(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-list","name":"list_dir","args":{"DirectoryPath":"/tmp/project"}}}]},"finishReason":"STOP"}],"modelVersion":%q,"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}}`, model)
					if r.URL.Path == antigravityStreamPath {
						w.Header().Set("Content-Type", "text/event-stream")
						response = "data: " + response + "\n\n"
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					if _, err := io.WriteString(w, response); err != nil {
						t.Error(err)
					}
				}))
				defer server.Close()
				executor := NewAntigravityExecutor(&config.Config{Codex: config.CodexConfig{ExtraSystemPrompt: "merge-compatible prompt"}})
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{
					"access_token": "test-token", "project_id": "test-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
				}}
				input := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":1024,"messages":[{"role":"user","content":"List /tmp/project"}],"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},{"name":"mcp__test__call","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}]}`, model))
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				req := cliproxyexecutor.Request{Model: model, Payload: input}
				var output []byte
				if stream {
					result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
					var name string
					var arguments strings.Builder
					for _, line := range strings.Split(string(output), "\n") {
						if !strings.HasPrefix(line, "data:") {
							continue
						}
						event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
						if event.Get("content_block.type").String() == "tool_use" {
							name = event.Get("content_block.name").String()
						}
						if event.Get("delta.type").String() == "input_json_delta" {
							arguments.WriteString(event.Get("delta.partial_json").String())
						}
					}
					if name != "Bash" || gjson.Get(arguments.String(), "command").String() != "ls -la -- '/tmp/project'" {
						t.Fatalf("stream lost mapped tool: %s", output)
					}
					if !strings.Contains(string(output), `"type":"message_stop"`) {
						t.Fatalf("stream did not terminate: %s", output)
					}
				} else {
					result, err := executor.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = result.Payload
					tool := gjson.GetBytes(output, "content.0")
					if tool.Get("name").String() != "Bash" || tool.Get("input.command").String() != "ls -la -- '/tmp/project'" {
						t.Fatalf("nonstream lost mapped tool: %s", output)
					}
				}
				body := <-bodies
				if !strings.Contains(gjson.GetBytes(body, "request.tools").Raw, `"list_dir"`) || !strings.Contains(gjson.GetBytes(body, "request.tools").Raw, `"mcp__test__call"`) {
					t.Fatalf("upstream lost native tools or MCP: %s", body)
				}
				if gjson.GetBytes(body, "request.systemInstruction.parts.1.text").String() != "merge-compatible prompt" {
					t.Fatalf("upstream lost extra prompt: %s", body)
				}
			})
		}
	}
}
