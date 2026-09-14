package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestNativeDirectoryHTTPRoundTrip captures the real executor HTTP boundary on two turns.
func TestNativeDirectoryHTTPRoundTrip(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "error"
		}
		t.Run(name, func(t *testing.T) { testNativeDirectoryHTTPRoundTrip(t, failed) })
	}
}

// testNativeDirectoryHTTPRoundTrip checks both result routes through the same adapter path.
func testNativeDirectoryHTTPRoundTrip(t *testing.T, failed bool) {
	t.Helper()
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)
	bodies := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		response := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-list","name":"list_dir","args":{"DirectoryPath":"/tmp/project"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}` + "\n\n"
		if _, err := io.WriteString(w, response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	executor := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "native-http-test", Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{
		"access_token": "dummy-token", "project_id": "test-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	const model = "gemini-3.8-flash-high"
	request := map[string]any{
		"model": model, "max_tokens": 1024,
		"tools":    []any{map[string]any{"name": "Bash", "description": "Run shell command", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"command"}}}},
		"messages": []any{map[string]any{"role": "user", "content": "List /tmp/project"}},
	}
	run := func() []byte {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: body}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: body, Stream: true, Headers: http.Header{"X-Claude-Code-Session-Id": []string{"native-http-test"}}})
		if err != nil {
			t.Fatal(err)
		}
		var output []byte
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			output = append(output, chunk.Payload...)
		}
		return output
	}
	first := run()
	upstreamFirst := <-bodies
	if !strings.Contains(gjson.GetBytes(upstreamFirst, "request.tools").Raw, `"list_dir"`) {
		t.Fatal("native catalog was removed")
	}
	var id, name string
	var arguments strings.Builder
	for _, line := range strings.Split(string(first), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		if event.Get("content_block.type").String() == "tool_use" {
			id, name = event.Get("content_block.id").String(), event.Get("content_block.name").String()
		}
		if event.Get("delta.type").String() == "input_json_delta" {
			arguments.WriteString(event.Get("delta.partial_json").String())
		}
	}
	if id == "" || name != "Bash" {
		t.Fatalf("client call id=%q name=%q", id, name)
	}
	if gjson.Get(arguments.String(), "command").String() != "ls -la -- '/tmp/project'" {
		t.Fatalf("client command=%s", arguments.String())
	}
	var args any
	if err := json.Unmarshal([]byte(arguments.String()), &args); err != nil {
		t.Fatal(err)
	}
	resultText, resultKey, absentKey := "CLAUDE.MD\n.hidden", "result", "error"
	if failed {
		resultText, resultKey, absentKey = "permission denied", "error", "result"
	}
	request["messages"] = append(request["messages"].([]any),
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": args}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": failed, "content": resultText}}},
	)
	run()
	second := <-bodies
	var call, result gjson.Result
	gjson.GetBytes(second, "request.contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall").Exists() {
				call = part.Get("functionCall")
			}
			if part.Get("functionResponse").Exists() {
				result = part.Get("functionResponse")
			}
			return true
		})
		return true
	})
	if call.Get("name").String() != "list_dir" || result.Get("name").String() != "list_dir" || call.Get("id").String() != "native-list" || result.Get("id").String() != "native-list" {
		t.Fatalf("final HTTP identity mismatch: call=%s response=%s", call.Raw, result.Raw)
	}
	if result.Get("response."+resultKey).String() != resultText || result.Get("response."+absentKey).Exists() {
		t.Fatalf("final HTTP body changed tool %s: %s", resultKey, result.Raw)
	}
}
