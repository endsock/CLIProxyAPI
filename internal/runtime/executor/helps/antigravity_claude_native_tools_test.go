package helps

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestRewriteRequestToolsReplacesClaudeBuiltins(t *testing.T) {
	payload := []byte(`{
		"request":{
			"tools":[
				{"functionDeclarations":[{"name":"Read","parameters":{"type":"OBJECT"}}]},
				{"functionDeclarations":[{"name":"Bash","parameters":{"type":"OBJECT"}}]},
				{"functionDeclarations":[{"name":"Skill","parameters":{"type":"OBJECT"}}]},
				{"functionDeclarations":[{"name":"mcp__foo__bar","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}}}}]}
			],
			"contents":[{"role":"model","parts":[{"functionCall":{"name":"Read","args":{"file_path":"/x"}}}]}]
		}
	}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	got := RewriteRequestTools(ctx, payload)
	names := toolDeclNames(got)
	if containsName(names, "Read") || containsName(names, "Bash") || containsName(names, "Skill") {
		t.Fatalf("claude builtins still present: %v", names)
	}
	if !containsName(names, "view_file") || !containsName(names, "run_command") {
		t.Fatalf("agy catalog missing: %v", names)
	}
	if !containsName(names, "mcp__foo__bar") {
		t.Fatalf("mcp declaration dropped: %v", names)
	}
	mcp := findDeclByName(got, "mcp__foo__bar")
	orig := findDeclByName(payload, "mcp__foo__bar")
	if mcp.Raw != orig.Raw {
		t.Fatalf("mcp declaration mutated\n got %s\nwant %s", mcp.Raw, orig.Raw)
	}
	if gjson.GetBytes(got, "request.contents.0.parts.0.functionCall.name").String() != "Read" {
		t.Fatalf("history functionCall rewritten: %s", gjson.GetBytes(got, "request.contents.0.parts.0.functionCall").Raw)
	}
}

func TestRewriteRequestToolsCatalogOnly(t *testing.T) {
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"Read"}]}]}}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	got := RewriteRequestTools(ctx, payload)
	names := toolDeclNames(got)
	want := []string{
		"view_file", "run_command", "send_message", "invoke_subagent",
		"write_to_file", "replace_file_content", "read_url_content", "search_web",
		"find_by_name", "grep_search", "list_dir", "ask_question",
	}
	if len(names) != 12 {
		t.Fatalf("tool count = %d (%v), want 12", len(names), names)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("tools[%d]=%q, want %q (%v)", i, names[i], name, names)
		}
	}
	for _, dropped := range []string{"schedule", "manage_task", "manage_subagents", "define_subagent", "generate_image"} {
		if containsName(names, dropped) {
			t.Fatalf("excluded tool %q present", dropped)
		}
	}
	required := findDeclByName(got, "replace_file_content").Get("parameters.required")
	for _, key := range []string{"StartLine", "EndLine"} {
		found := false
		required.ForEach(func(_, v gjson.Result) bool {
			if v.String() == key {
				found = true
				return false
			}
			return true
		})
		if found {
			t.Fatalf("replace_file_content required still has %s: %s", key, required.Raw)
		}
	}
}

func TestRewriteRequestToolsOverwritesSystemInstruction(t *testing.T) {
	payload := []byte(`{
		"request":{
			"systemInstruction":{"role":"user","parts":[{"text":"YOU ARE CLAUDE. Keep this."},{"text":"second part"}]},
			"tools":[{"functionDeclarations":[{"name":"Read"}]}]
		}
	}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	got := RewriteRequestTools(ctx, payload)
	si := gjson.GetBytes(got, "request.systemInstruction")
	if si.Get("role").String() != "user" {
		t.Fatalf("role=%s", si.Get("role").Raw)
	}
	parts := si.Get("parts")
	if len(parts.Array()) != 1 {
		t.Fatalf("parts not replaced: %s", parts.Raw)
	}
	text := parts.Get("0.text").String()
	if !bytes.Contains([]byte(text), []byte("<identity>")) {
		t.Fatalf("missing AGY identity prompt")
	}
	if bytes.Contains([]byte(text), []byte("YOU ARE CLAUDE")) || bytes.Contains(got, []byte("second part")) {
		t.Fatalf("original systemInstruction was appended instead of replaced")
	}
}

func TestRewriteRequestToolsAppendsExtraSystemPrompt(t *testing.T) {
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"Read"}]}]}}`)
	cfg := &config.Config{}
	cfg.Codex.ExtraSystemPrompt = "  extra from config  "
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, cfg)
	got := RewriteRequestTools(ctx, payload)
	parts := gjson.GetBytes(got, "request.systemInstruction.parts")
	if len(parts.Array()) != 2 {
		t.Fatalf("parts=%s", parts.Raw)
	}
	if !bytes.Contains([]byte(parts.Get("0.text").String()), []byte("<identity>")) {
		t.Fatalf("part0 missing AGY prompt")
	}
	if parts.Get("1.text").String() != "extra from config" {
		t.Fatalf("part1=%s", parts.Get("1.text").Raw)
	}
}

func TestRewriteRequestToolsNoOpWithoutAnnotation(t *testing.T) {
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"Read"}]}]}}`)
	got := RewriteRequestTools(context.Background(), payload)
	if !bytes.Equal(got, payload) {
		t.Fatalf("unmarked ctx mutated payload")
	}
}

func TestRewriteRequestToolsInjectsCatalogWhenToolsEmpty(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	got := RewriteRequestTools(ctx, payload)
	names := toolDeclNames(got)
	if len(names) != 12 {
		t.Fatalf("tool count = %d (%v), want 12", len(names), names)
	}
	if !containsName(names, "view_file") || !containsName(names, "run_command") {
		t.Fatalf("agy catalog missing: %v", names)
	}
	si := gjson.GetBytes(got, "request.systemInstruction")
	if !bytes.Contains([]byte(si.Get("parts.0.text").String()), []byte("<identity>")) {
		t.Fatalf("missing AGY identity prompt in systemInstruction: %s", si.Raw)
	}
}

func TestMapResponseFunctionCallsArgs(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	tests := []struct {
		name     string
		body     string
		wantName string
		check    func(*testing.T, gjson.Result)
	}{
		{
			name:     "view_file range",
			body:     functionCallBody("view_file", `{"AbsolutePath":"/a.go","StartLine":10,"EndLine":20,"ContentOffset":4}`),
			wantName: "Read",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("file_path").String() != "/a.go" || args.Get("offset").Int() != 10 || args.Get("limit").Int() != 11 {
					t.Fatalf("args=%s", args.Raw)
				}
				if args.Get("ContentOffset").Exists() {
					t.Fatalf("ContentOffset leaked")
				}
			},
		},
		{
			name:     "view_file end only",
			body:     functionCallBody("view_file", `{"AbsolutePath":"/a.go","EndLine":8}`),
			wantName: "Read",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("offset").Int() != 1 || args.Get("limit").Int() != 8 {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "list_dir",
			body:     functionCallBody("list_dir", `{"DirectoryPath":"/tmp"}`),
			wantName: "Bash",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("command").String() != "ls -la -- '/tmp'" || args.Get("file_path").Exists() {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "write_to_file",
			body:     functionCallBody("write_to_file", `{"TargetFile":"/a.go","CodeContent":"x","Overwrite":true,"Description":"d"}`),
			wantName: "Write",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("file_path").String() != "/a.go" || args.Get("content").String() != "x" {
					t.Fatalf("args=%s", args.Raw)
				}
				if args.Get("Overwrite").Exists() || args.Get("Description").Exists() {
					t.Fatalf("dropped fields leaked: %s", args.Raw)
				}
			},
		},
		{
			name:     "replace_file_content",
			body:     functionCallBody("replace_file_content", `{"TargetFile":"/a.go","TargetContent":"old","ReplacementContent":"new","AllowMultiple":true,"StartLine":1,"EndLine":2,"Instruction":"i"}`),
			wantName: "Edit",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("file_path").String() != "/a.go" || args.Get("old_string").String() != "old" || args.Get("new_string").String() != "new" || !args.Get("replace_all").Bool() {
					t.Fatalf("args=%s", args.Raw)
				}
				if args.Get("StartLine").Exists() || args.Get("Instruction").Exists() {
					t.Fatalf("dropped fields leaked: %s", args.Raw)
				}
			},
		},
		{
			name:     "run_command bash cwd",
			body:     functionCallBody("run_command", `{"CommandLine":"ls","Cwd":"/proj","WaitMsBeforeAsync":100,"toolAction":"Listing files"}`),
			wantName: "Bash",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("command").String() != "cd /proj && ls" {
					t.Fatalf("command=%s", args.Get("command").Raw)
				}
				if !args.Get("run_in_background").Bool() || args.Get("timeout").Exists() {
					t.Fatalf("args=%s", args.Raw)
				}
				if args.Get("description").String() != "Listing files" {
					t.Fatalf("description=%s", args.Get("description").Raw)
				}
			},
		},
		{
			name:     "run_command timeout cap",
			body:     functionCallBody("run_command", `{"CommandLine":"sleep 1","WaitMsBeforeAsync":900000}`),
			wantName: "Bash",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("timeout").Int() != 600000 {
					t.Fatalf("timeout=%s", args.Get("timeout").Raw)
				}
			},
		},
		{
			name:     "find_by_name",
			body:     functionCallBody("find_by_name", `{"Pattern":"*.go","SearchDirectory":"/src","MaxDepth":2}`),
			wantName: "Glob",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("pattern").String() != "*.go" || args.Get("path").String() != "/src" {
					t.Fatalf("args=%s", args.Raw)
				}
				if args.Get("MaxDepth").Exists() {
					t.Fatalf("MaxDepth leaked")
				}
			},
		},
		{
			name:     "find_by_name extensions",
			body:     functionCallBody("find_by_name", `{"Pattern":"","Extensions":["go","ts"]}`),
			wantName: "Glob",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("pattern").String() != "*.{go,ts}" || args.Get("path").Exists() {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "grep_search",
			body:     functionCallBody("grep_search", `{"Query":"foo.bar","IsRegex":false,"SearchPath":"/src","Includes":["*.go","*.ts"],"CaseInsensitive":true,"MatchPerLine":true}`),
			wantName: "Grep",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("pattern").String() != `foo\.bar` || args.Get("path").String() != "/src" || args.Get("glob").String() != "*.go" {
					t.Fatalf("args=%s", args.Raw)
				}
				if !args.Get("-i").Bool() || args.Get("output_mode").String() != "content" {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "search_web",
			body:     functionCallBody("search_web", `{"query":"hello","domain":"example.com"}`),
			wantName: "WebSearch",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("query").String() != "hello" || args.Get("allowed_domains.0").String() != "example.com" {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "read_url_content",
			body:     functionCallBody("read_url_content", `{"Url":"https://example.com"}`),
			wantName: "WebFetch",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("url").String() != "https://example.com" || args.Get("prompt").String() != "Extract the main content as markdown." {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
		{
			name:     "ask_question",
			body:     functionCallBody("ask_question", `{"questions":[{"question":"Which library should we use for dates?","options":["a","b","c"],"is_multi_select":true}]}`),
			wantName: "AskUserQuestion",
			check: func(t *testing.T, args gjson.Result) {
				q := args.Get("questions.0")
				if q.Get("question").String() != "Which library should we use for dates?" || q.Get("header").String() != "Which librar" {
					t.Fatalf("question=%s", q.Raw)
				}
				if q.Get("options.0.label").String() != "a" || q.Get("options.0.description").String() != "a" || !q.Get("multiSelect").Bool() {
					t.Fatalf("options=%s", q.Raw)
				}
			},
		},
		{
			name:     "send_message",
			body:     functionCallBody("send_message", `{"Recipient":"researcher","Message":"hi","toolSummary":"Ping agent"}`),
			wantName: "SendMessage",
			check: func(t *testing.T, args gjson.Result) {
				if args.Get("to").String() != "researcher" || args.Get("message").String() != "hi" || args.Get("summary").String() != "Ping agent" {
					t.Fatalf("args=%s", args.Raw)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MapResponseFunctionCalls(ctx, []byte(tt.body))
			fc := gjson.GetBytes(got, "response.candidates.0.content.parts.0.functionCall")
			if fc.Get("name").String() != tt.wantName {
				t.Fatalf("name=%q want %q body=%s", fc.Get("name").String(), tt.wantName, got)
			}
			tt.check(t, fc.Get("args"))
		})
	}
}

func TestMapResponseFunctionCallsRunCommandPowerShell(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"PowerShell"}]}]}}`)
	_ = RewriteRequestTools(ctx, payload)
	got := MapResponseFunctionCalls(ctx, []byte(functionCallBody("run_command", `{"CommandLine":"Get-ChildItem","Cwd":"D:\\proj"}`)))
	fc := gjson.GetBytes(got, "response.candidates.0.content.parts.0.functionCall")
	if fc.Get("name").String() != "PowerShell" {
		t.Fatalf("name=%s", fc.Get("name").Raw)
	}
	if fc.Get("args.command").String() != `Set-Location D:\proj; Get-ChildItem` {
		t.Fatalf("command=%s", fc.Get("args.command").Raw)
	}
}

func TestMapResponseFunctionCallsInvokeSubagentSplit(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	body := []byte(`{"response":{"candidates":[{"content":{"parts":[{
		"thoughtSignature":"sig1",
		"functionCall":{"name":"invoke_subagent","args":{"Subagents":[
			{"TypeName":"Explore","Prompt":"a","Role":"Researcher","Model":"inherit"},
			{"TypeName":"general-purpose","Prompt":"b","Role":"Worker","Model":"pro","Workspace":"branch"}
		]}}
	}]}}]}}`)
	got := MapResponseFunctionCalls(ctx, body)
	parts := gjson.GetBytes(got, "response.candidates.0.content.parts")
	if len(parts.Array()) != 2 {
		t.Fatalf("parts=%s", parts.Raw)
	}
	first := parts.Get("0")
	second := parts.Get("1")
	if first.Get("functionCall.name").String() != "Agent" || second.Get("functionCall.name").String() != "Agent" {
		t.Fatalf("names not mapped: %s", parts.Raw)
	}
	if first.Get("thoughtSignature").String() != "sig1" {
		t.Fatalf("signature not kept on first: %s", first.Raw)
	}
	if second.Get("thoughtSignature").Exists() {
		t.Fatalf("signature copied to second: %s", second.Raw)
	}
	a0 := first.Get("functionCall.args")
	if a0.Get("subagent_type").String() != "Explore" || a0.Get("prompt").String() != "a" || a0.Get("description").String() != "Researcher" {
		t.Fatalf("first args=%s", a0.Raw)
	}
	if a0.Get("model").Exists() || a0.Get("isolation").Exists() || !a0.Get("run_in_background").Bool() {
		t.Fatalf("first extras=%s", a0.Raw)
	}
	a1 := second.Get("functionCall.args")
	if a1.Get("model").String() != "sonnet" || a1.Get("isolation").String() != "worktree" {
		t.Fatalf("second args=%s", a1.Raw)
	}
}

func TestMapResponseFunctionCallsPassthrough(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	for _, body := range []string{
		functionCallBody("Read", `{"file_path":"/x"}`),
		functionCallBody("mcp__foo__bar", `{"q":"1"}`),
		functionCallBody("ask_question", `{"questions":[{"question":"only one?","options":["yes"]}]}`),
		functionCallBody("invoke_subagent", `{"toolAction":"Launch"}`),
	} {
		in := []byte(body)
		got := MapResponseFunctionCalls(ctx, in)
		if !bytes.Equal(got, in) {
			t.Fatalf("passthrough mutated\n got %s\nwant %s", got, in)
		}
	}
}

func TestMapResponseFunctionCallsIncompleteArgs(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	in := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"view_file","args":"partial"}}]}}]}}`)
	got := MapResponseFunctionCalls(ctx, in)
	if !bytes.Equal(got, in) {
		t.Fatalf("incomplete args rewritten: %s", got)
	}
}

func TestApplyRequestHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "original")
	ApplyRequestHeaders(context.Background(), req)
	if req.Header.Get("User-Agent") != "original" {
		t.Fatalf("unmarked ctx overwrote UA: %s", req.Header.Get("User-Agent"))
	}
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
	ApplyRequestHeaders(ctx, req)
	if req.Header.Get("User-Agent") != antigravityClaudeNativeUserAgent {
		t.Fatalf("UA=%q", req.Header.Get("User-Agent"))
	}
}

func TestAnnotateClaudeNativeToolsIgnoresOtherFormats(t *testing.T) {
	ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatOpenAI, nil)
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"Read"}]}]}}`)
	got := RewriteRequestTools(ctx, payload)
	if !bytes.Equal(got, payload) {
		t.Fatalf("openai source mutated tools")
	}
}

func functionCallBody(name, args string) string {
	return `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + name + `","args":` + args + `}}]}}]}}`
}

func toolDeclNames(payload []byte) []string {
	var names []string
	gjson.GetBytes(payload, "request.tools").ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
			names = append(names, decl.Get("name").String())
			return true
		})
		return true
	})
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func findDeclByName(payload []byte, name string) gjson.Result {
	var found gjson.Result
	gjson.GetBytes(payload, "request.tools").ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
			if decl.Get("name").String() == name {
				found = decl
				return false
			}
			return true
		})
		return found.Raw == ""
	})
	return found
}
