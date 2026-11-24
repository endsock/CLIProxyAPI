package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityRequestIDFromSessionHeader(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- body
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"test"}}`))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "test-auth",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			"project_id":   "test-project",
		},
	}
	const sessionID = "e061e982-b6ae-4518-acb6-d9437bc684b0"
	var fixedUUID string
	for _, tc := range []struct {
		name   string
		model  string
		stream bool
	}{
		{name: "non-stream", model: "gemini-3-flash"},
		{name: "non-stream via stream", model: "gemini-3-pro"},
		{name: "stream", model: "gemini-3-flash", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: []byte(`{"temperature":0.5,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAI,
				Headers:      http.Header{"Session-Id": []string{sessionID}},
			}
			before := time.Now().UnixMilli()
			if tc.stream {
				_, _ = executor.ExecuteStream(context.Background(), auth, request, opts)
			} else {
				_, _ = executor.Execute(context.Background(), auth, request, opts)
			}
			after := time.Now().UnixMilli()
			body := <-bodies
			parts := strings.Split(ojson.GetBytes(body, "requestId").String(), "/")
			if len(parts) != 5 || parts[0] != "agent" || parts[1] != sessionID {
				t.Fatalf("unexpected requestId: %s", ojson.GetBytes(body, "requestId").String())
			}
			timestamp, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil || timestamp < before || timestamp > after {
				t.Fatalf("timestamp = %q, expected between %d and %d", parts[2], before, after)
			}
			if _, err := uuid.Parse(parts[3]); err != nil {
				t.Fatalf("invalid derived UUID %q: %v", parts[3], err)
			}
			if fixedUUID == "" {
				fixedUUID = parts[3]
			} else if parts[3] != fixedUUID {
				t.Fatalf("derived UUID = %q, want %q", parts[3], fixedUUID)
			}
			count := ojson.GetBytes(body, "request.contents.#").Int()
			if parts[4] != strconv.FormatInt(count, 10) {
				t.Fatalf("requestId count = %q, actual contents count = %d", parts[4], count)
			}
			labels := ojson.GetBytes(body, "request.labels")
			for key, want := range map[string]string{
				"cascade_id": sessionID, "root_cascade_id": sessionID,
				"trajectory_id": parts[3], "request_id": parts[3] + "-" + strconv.FormatInt(count, 10),
				"model_enum": "MODEL_PLACEHOLDER_M526", "used_claude": "false",
				"used_claude_conservative": "false", "used_non_gemini_model": "false",
			} {
				if got := labels.Get(key).String(); got != want {
					t.Errorf("labels.%s = %q, want %q", key, got, want)
				}
			}
			if labels.Get("last_execution_id").Exists() || labels.Get("last_step_index").Exists() {
				t.Errorf("unexpected execution or step label: %s", labels.Raw)
			}
			generation := ojson.GetBytes(body, "request.generationConfig")
			if generation.Get("maxOutputTokens").Int() != 262144 || !generation.Get("thinkingConfig.includeThoughts").Bool() || generation.Get("thinkingConfig.thinkingLevel").String() != "MEDIUM" || generation.Get("temperature").Exists() {
				t.Errorf("unexpected generationConfig: %s", generation.Raw)
			}
		})
	}
}

func TestAntigravityRequestIDClaudeMetadata(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- body
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "claude-session-test",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			"project_id":   "test-project",
		},
	}
	const sessionID = "e061e982-b6ae-4518-acb6-d9437bc684b0"
	const claudePayload = `{"metadata":{"user_id":"{\"session_id\":\"` + sessionID + `\"}"},"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`
	const legacyPayload = `{"metadata":{"user_id":"user_abc_session_` + sessionID + `"},"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`
	const noMetadata = `{"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`

	for _, tc := range []struct {
		name     string
		model    string
		payload  string
		original string
		header   string
		want     string
		stream   bool
	}{
		{name: "metadata non-stream", model: "gemini-3-flash", payload: claudePayload, want: sessionID},
		{name: "metadata overrides header", model: "gemini-3-pro", payload: claudePayload, header: "header-session", want: sessionID},
		{name: "legacy metadata stream", model: "gemini-3-flash", payload: legacyPayload, want: sessionID, stream: true},
		{name: "original metadata", model: "gemini-3-flash", payload: noMetadata, original: claudePayload, header: "header-session", want: sessionID},
		{name: "header fallback", model: "gemini-3-flash", payload: noMetadata, header: "header-session", want: "header-session"},
		{name: "missing session", model: "gemini-3-flash", payload: noMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload)}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(tc.original),
				Headers:         http.Header{"Session-Id": []string{tc.header}},
			}
			if tc.stream {
				_, _ = executor.ExecuteStream(context.Background(), auth, req, opts)
			} else {
				_, _ = executor.Execute(context.Background(), auth, req, opts)
			}
			var body []byte
			select {
			case body = <-bodies:
			case <-time.After(time.Second):
				t.Fatal("upstream request not received")
			}
			requestID := ojson.GetBytes(body, "requestId").String()
			parts := strings.Split(requestID, "/")
			if tc.want == "" {
				count := ojson.GetBytes(body, "request.contents.#").Int()
				if len(parts) != 5 || parts[0] != "agent" {
					t.Fatalf("requestId = %q, want agent/<uuid>/<ts>/<uuid>/<count>", requestID)
				}
				if _, err := uuid.Parse(parts[1]); err != nil {
					t.Fatalf("invalid random session UUID %q: %v", parts[1], err)
				}
				if _, err := uuid.Parse(parts[3]); err != nil {
					t.Fatalf("invalid random trajectory UUID %q: %v", parts[3], err)
				}
				if parts[1] == parts[3] {
					t.Fatalf("expected distinct random UUIDs, got %q", requestID)
				}
				labels := ojson.GetBytes(body, "request.labels")
				if labels.Get("cascade_id").String() != parts[1] || labels.Get("root_cascade_id").String() != parts[1] || labels.Get("trajectory_id").String() != parts[3] || labels.Get("request_id").String() != parts[3]+"-"+strconv.FormatInt(count, 10) {
					t.Fatalf("unexpected labels for random session: %s", labels.Raw)
				}
				return
			}
			if len(parts) != 5 || parts[0] != "agent" || parts[1] != tc.want || parts[3] != uuid.NewSHA1(uuid.NameSpaceOID, []byte(tc.want)).String() {
				t.Fatalf("requestId = %q, want session %q and its derived UUID", requestID, tc.want)
			}
		})
	}
}

func TestAntigravityRequestIDExceptions(t *testing.T) {
	executor := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test-project"}}
	for _, tc := range []struct {
		name      string
		model     string
		payload   string
		sessionID string
		prefix    string
	}{
		{name: "image", model: "gemini-3.1-flash-image", payload: `{"request":{"contents":[]}}`, sessionID: "test-session", prefix: "image_gen/"},
		{name: "web search", model: "gemini-3-flash", payload: `{"requestType":"web_search","request":{"contents":[]}}`, sessionID: "test-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withAntigravityRequestSessionID(context.Background(), tc.sessionID)
			doc := ojson.NewDoc([]byte(tc.payload))
			_, body, err := executor.buildRequestDoc(ctx, auth, "token", tc.model, doc, false, "", "https://example.com")
			if err != nil {
				t.Fatal(err)
			}
			requestID := ojson.GetBytes(body, "requestId").String()
			if ojson.GetBytes(body, "request.labels.cascade_id").Exists() {
				t.Errorf("unexpected cascade_id in excluded request: %s", body)
			}
			if tc.prefix == "" && requestID != "" {
				t.Fatalf("requestId = %q, want empty", requestID)
			}
			if tc.prefix != "" && !strings.HasPrefix(requestID, tc.prefix) {
				t.Fatalf("requestId = %q, want prefix %q", requestID, tc.prefix)
			}
		})
	}
}
