package helps

import (
	"bytes"
	"context"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityPiToolsMapping(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "client pi (test)")
	for _, tc := range []struct {
		name, args, wantName, wantArgs string
	}{
		{"view_file", `{"AbsolutePath":"/tmp/a","StartLine":3,"EndLine":5}`, "read", `{"path":"/tmp/a","offset":3,"limit":3}`},
		{"view_file", `{"AbsolutePath":"/tmp/a","EndLine":5}`, "read", `{"path":"/tmp/a","offset":1,"limit":5}`},
		{"write_to_file", `{"TargetFile":"/tmp/a","CodeContent":"hello"}`, "write", `{"path":"/tmp/a","content":"hello"}`},
		{"replace_file_content", `{"TargetFile":"/tmp/a","TargetContent":"old","ReplacementContent":"new","AllowMultiple":false}`, "edit", `{"path":"/tmp/a","edits":[{"oldText":"old","newText":"new"}]}`},
		{"run_command", `{"CommandLine":"echo hello","Cwd":"/tmp/a'b","WaitMsBeforeAsync":1000}`, "bash", `{"command":"cd -- '/tmp/a'\"'\"'b' && echo hello"}`},
		{"list_dir", `{"DirectoryPath":"/tmp/a"}`, "bash", `{"command":"ls -la -- '/tmp/a'"}`},
		{"find_by_name", `{"Pattern":"*.go","SearchDirectory":"/tmp/a","MaxDepth":2,"Type":"file","Excludes":["vendor"],"Extensions":["go"],"FullPath":true}`, "bash", `{"command":"fd --glob --max-results 50 --max-depth '2' --type f --full-path --extension 'go' --exclude 'vendor' -- '*.go' '/tmp/a'"}`},
		{"grep_search", `{"Query":"a.b","SearchPath":"/tmp/a","IsRegex":false,"CaseInsensitive":true,"MatchPerLine":true,"Includes":["*.go","!vendor/**"]}`, "bash", `{"command":"rg -n -F -i -g '*.go' -g '!vendor/**' -- 'a.b' '/tmp/a'"}`},
		{"ask_question", `{"questions":[{"question":"Choose?","options":["A","B"],"is_multi_select":true}]}`, "ask_user_question", `{"questions":[{"question":"Choose?","header":"Choose?","options":[{"label":"A","description":"A"},{"label":"B","description":"B"}],"multiSelect":true}]}`},
	} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			body := []byte(`{"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"signature","functionCall":{"id":"native-id","name":"` + tc.name + `","args":` + tc.args + `}}]}}]}}`)
			out := MapResponseFunctionCalls(ctx, body)
			part := gjson.GetBytes(out, "response.candidates.0.content.parts.0")
			call := part.Get("functionCall")
			if call.Get("name").String() != tc.wantName || call.Get("id").String() != "native-id" || part.Get("thoughtSignature").String() != "signature" {
				t.Fatalf("unexpected mapped identity: %s", out)
			}
			actual, _ := marshalArgsNoHTMLEscape(call.Get("args").Value())
			expected, _ := marshalArgsNoHTMLEscape(gjson.Parse(tc.wantArgs).Value())
			if !bytes.Equal(actual, expected) {
				t.Fatalf("args = %s, want %s", actual, expected)
			}
		})
	}
}

func TestAntigravityPiToolsSelectionAndSingleDirection(t *testing.T) {
	for _, agent := range []string{"", "claude", "pi", "PI (test)", "pi (test)", "prefix pi (test)"} {
		ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, agent)
		body := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"view_file","args":{"AbsolutePath":"/tmp/a"}}}]}}]}`)
		want := "Read"
		if agent == "pi (test)" || agent == "prefix pi (test)" {
			want = "read"
		}
		if got := gjson.GetBytes(MapResponseFunctionCalls(ctx, body), "candidates.0.content.parts.0.functionCall.name").String(); got != want {
			t.Fatalf("agent %q: name = %q, want %q", agent, got, want)
		}
	}
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil, "pi (test)")
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"read"}]}],"contents":[{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"/tmp/a"}}}]}]}}`)
	if out := RewriteRequestTools(ctx, payload); gjson.GetBytes(out, "request.contents").Raw != gjson.GetBytes(payload, "request.contents").Raw {
		t.Fatal("Pi mapping rewrote request history")
	}
	if out, err := RestoreClaudeNativeToolHistory(ctx, payload, nil); err != nil || !bytes.Equal(out, payload) {
		t.Fatal("Pi mapping restored request history")
	}
	for _, call := range []string{
		`{"name":"mcp__test__call","args":{}}`,
		`{"name":"search_web","args":{"query":"test"}}`,
		`{"name":"read_url_content","args":{"Url":"https://example.com"}}`,
		`{"name":"invoke_subagent","args":{"Subagents":[]}}`,
		`{"name":"send_message","args":{}}`,
		`{"name":"view_file"}`,
		`{"name":"view_file","args":{"ContentOffset":100}}`,
		`{"name":"replace_file_content","args":{"AllowMultiple":true}}`,
	} {
		body := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":` + call + `}]}}]}`)
		if !bytes.Equal(MapResponseFunctionCalls(ctx, body), body) {
			t.Fatalf("unsupported call changed: %s", call)
		}
	}
	unmarked := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatOpenAI, nil, "pi (test)")
	if claudeNativeToolsFromCtx(unmarked) != nil {
		t.Fatal("Pi header activated mapping for a non-Claude protocol")
	}
}
