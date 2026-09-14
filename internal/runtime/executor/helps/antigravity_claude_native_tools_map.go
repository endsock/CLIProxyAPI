package helps

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type mappedClaudeCall struct {
	name string
	args map[string]any
}

type agyClaudeCallMapper func(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall

var agyClaudeFunctionMappers = map[string]agyClaudeCallMapper{
	"view_file":            mapViewFile,
	"list_dir":             mapListDir,
	"write_to_file":        mapWriteToFile,
	"replace_file_content": mapReplaceFileContent,
	"run_command":          mapRunCommand,
	"find_by_name":         mapFindByName,
	"grep_search":          mapGrepSearch,
	"search_web":           mapSearchWeb,
	"read_url_content":     mapReadURLContent,
	"ask_question":         mapAskQuestion,
	"invoke_subagent":      mapInvokeSubagent,
	"send_message":         mapSendMessage,
}

func mapResponseParts(parts gjson.Result, state *claudeNativeToolsState) ([]byte, bool) {
	if !parts.IsArray() {
		return nil, false
	}
	items := parts.Array()
	out := make([][]byte, 0, len(items))
	changed := false
	for _, part := range items {
		fc := part.Get("functionCall")
		if !fc.Exists() {
			out = append(out, []byte(part.Raw))
			continue
		}
		mapped := mapFunctionCall(fc, state)
		if mapped == nil {
			out = append(out, []byte(part.Raw))
			continue
		}
		changed = true
		for i, fcRaw := range mapped {
			newPart := []byte(part.Raw)
			newPart, _ = sjson.SetRawBytes(newPart, "functionCall", fcRaw)
			if i > 0 {
				newPart, _ = sjson.DeleteBytes(newPart, "thoughtSignature")
				newPart, _ = sjson.DeleteBytes(newPart, "thought_signature")
			}
			out = append(out, newPart)
		}
	}
	if !changed {
		return nil, false
	}
	return JoinRawJSONArray(out), true
}

func mapFunctionCall(fc gjson.Result, state *claudeNativeToolsState) [][]byte {
	name := fc.Get("name").String()
	if name == "" || IsClaudeMCPToolName(name) {
		return nil
	}
	mapper := agyClaudeFunctionMappers[name]
	if mapper == nil {
		return nil
	}
	args := fc.Get("args")
	if !args.Exists() || !args.IsObject() {
		return nil
	}
	calls := mapper(args, state)
	if len(calls) == 0 {
		return nil
	}
	return encodeMappedCalls(fc, calls)
}

func encodeMappedCalls(original gjson.Result, calls []mappedClaudeCall) [][]byte {
	out := make([][]byte, 0, len(calls))
	for _, call := range calls {
		raw, errMarshal := marshalArgsNoHTMLEscape(call.args)
		if errMarshal != nil {
			return nil
		}
		fc := []byte(original.Raw)
		fc, _ = sjson.SetBytes(fc, "name", call.name)
		fc, _ = sjson.SetRawBytes(fc, "args", raw)
		out = append(out, fc)
	}
	return out
}

func marshalArgsNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func mapViewFile(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if v := args.Get("AbsolutePath"); v.Exists() {
		out["file_path"] = v.String()
	}
	start := args.Get("StartLine")
	end := args.Get("EndLine")
	if start.Exists() {
		out["offset"] = start.Int()
		if end.Exists() {
			out["limit"] = end.Int() - start.Int() + 1
		}
	} else if end.Exists() {
		out["offset"] = int64(1)
		out["limit"] = end.Int()
	}
	return []mappedClaudeCall{{name: "Read", args: out}}
}

// mapListDir preserves directory enumeration instead of asking Read to open a directory.
func mapListDir(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall {
	path := args.Get("DirectoryPath")
	if path.Type != gjson.String || strings.TrimSpace(path.String()) == "" {
		return nil
	}
	name, command := "Bash", "ls -la -- "+quoteAntigravityShellLiteral(path.String(), false)
	if state != nil && state.preferPowerShell {
		name = "PowerShell"
		command = "Get-ChildItem -Force -LiteralPath " + quoteAntigravityShellLiteral(path.String(), true)
	}
	return []mappedClaudeCall{{name: name, args: map[string]any{
		"command":     command,
		"description": "List directory contents",
	}}}
}

// quoteAntigravityShellLiteral keeps model-supplied paths from becoming shell syntax.
func quoteAntigravityShellLiteral(value string, powerShell bool) string {
	if powerShell {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func mapWriteToFile(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if v := args.Get("TargetFile"); v.Exists() {
		out["file_path"] = v.String()
	}
	if v := args.Get("CodeContent"); v.Exists() {
		out["content"] = v.String()
	}
	return []mappedClaudeCall{{name: "Write", args: out}}
}

func mapReplaceFileContent(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if v := args.Get("TargetFile"); v.Exists() {
		out["file_path"] = v.String()
	}
	if v := args.Get("TargetContent"); v.Exists() {
		out["old_string"] = v.String()
	}
	if v := args.Get("ReplacementContent"); v.Exists() {
		out["new_string"] = v.String()
	}
	if v := args.Get("AllowMultiple"); v.Exists() {
		out["replace_all"] = v.Bool()
	}
	return []mappedClaudeCall{{name: "Edit", args: out}}
}

func mapRunCommand(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall {
	name := "Bash"
	if state != nil && state.preferPowerShell {
		name = "PowerShell"
	}
	out := map[string]any{}
	cmd := ""
	if v := args.Get("CommandLine"); v.Exists() {
		cmd = v.String()
	}
	if cwd := strings.TrimSpace(args.Get("Cwd").String()); cwd != "" {
		if name == "PowerShell" {
			cmd = "Set-Location " + cwd + "; " + cmd
		} else {
			cmd = "cd " + cwd + " && " + cmd
		}
	}
	if args.Get("CommandLine").Exists() || strings.TrimSpace(args.Get("Cwd").String()) != "" {
		out["command"] = cmd
	}
	if wait := args.Get("WaitMsBeforeAsync"); wait.Exists() {
		ms := wait.Int()
		if ms <= 500 {
			out["run_in_background"] = true
		} else {
			if ms > 600000 {
				ms = 600000
			}
			out["timeout"] = ms
		}
	}
	if action := args.Get("toolAction"); action.Exists() {
		if desc := truncateRunes(action.String(), 200); desc != "" {
			out["description"] = desc
		}
	}
	return []mappedClaudeCall{{name: name, args: out}}
}

func mapFindByName(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	pattern := ""
	if v := args.Get("Pattern"); v.Exists() {
		pattern = v.String()
	}
	if pattern == "" {
		if exts := args.Get("Extensions"); exts.IsArray() {
			parts := make([]string, 0, len(exts.Array()))
			for _, ext := range exts.Array() {
				if s := strings.TrimPrefix(ext.String(), "."); s != "" {
					parts = append(parts, s)
				}
			}
			switch len(parts) {
			case 1:
				pattern = "*." + parts[0]
			default:
				if len(parts) > 1 {
					pattern = "*.{" + strings.Join(parts, ",") + "}"
				}
			}
		}
	}
	if pattern != "" {
		out["pattern"] = pattern
	}
	if dir := args.Get("SearchDirectory"); dir.Exists() {
		if s := dir.String(); s != "" {
			out["path"] = s
		}
	}
	return []mappedClaudeCall{{name: "Glob", args: out}}
}

func mapGrepSearch(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if query := args.Get("Query"); query.Exists() {
		pattern := query.String()
		if isRegex := args.Get("IsRegex"); isRegex.Exists() && !isRegex.Bool() {
			pattern = regexp.QuoteMeta(pattern)
		}
		out["pattern"] = pattern
	}
	if p := args.Get("SearchPath"); p.Exists() {
		out["path"] = p.String()
	}
	if includes := args.Get("Includes"); includes.IsArray() {
		arr := includes.Array()
		if len(arr) > 0 {
			out["glob"] = arr[0].String()
		}
	}
	if ci := args.Get("CaseInsensitive"); ci.Exists() && ci.Bool() {
		out["-i"] = true
	}
	if mpl := args.Get("MatchPerLine"); mpl.Exists() {
		if mpl.Bool() {
			out["output_mode"] = "content"
		} else {
			out["output_mode"] = "files_with_matches"
		}
	}
	return []mappedClaudeCall{{name: "Grep", args: out}}
}

func mapSearchWeb(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if v := args.Get("query"); v.Exists() {
		out["query"] = v.String()
	}
	if v := args.Get("domain"); v.Exists() {
		if domain := v.String(); domain != "" {
			out["allowed_domains"] = []any{domain}
		}
	}
	return []mappedClaudeCall{{name: "WebSearch", args: out}}
}

func mapReadURLContent(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{
		"prompt": "Extract the main content as markdown.",
	}
	if v := args.Get("Url"); v.Exists() {
		out["url"] = v.String()
	}
	return []mappedClaudeCall{{name: "WebFetch", args: out}}
}

func mapAskQuestion(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	questions := args.Get("questions")
	if !questions.IsArray() {
		return nil
	}
	arr := questions.Array()
	if len(arr) == 0 {
		return nil
	}
	if len(arr) > 4 {
		arr = arr[:4]
	}
	outQuestions := make([]any, 0, len(arr))
	for _, q := range arr {
		opts := q.Get("options")
		if !opts.IsArray() {
			return nil
		}
		optArr := opts.Array()
		if len(optArr) < 2 {
			return nil
		}
		if len(optArr) > 4 {
			optArr = optArr[:4]
		}
		claudeOpts := make([]any, 0, len(optArr))
		for _, opt := range optArr {
			label := opt.String()
			claudeOpts = append(claudeOpts, map[string]any{
				"label":       label,
				"description": label,
			})
		}
		question := q.Get("question").String()
		item := map[string]any{
			"question": question,
			"header":   truncateRunes(question, 12),
			"options":  claudeOpts,
		}
		if ms := q.Get("is_multi_select"); ms.Exists() {
			item["multiSelect"] = ms.Bool()
		}
		outQuestions = append(outQuestions, item)
	}
	return []mappedClaudeCall{{name: "AskUserQuestion", args: map[string]any{"questions": outQuestions}}}
}

func mapInvokeSubagent(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	subagents := args.Get("Subagents")
	if !subagents.IsArray() {
		return nil
	}
	arr := subagents.Array()
	if len(arr) == 0 {
		return nil
	}
	calls := make([]mappedClaudeCall, 0, len(arr))
	for _, sa := range arr {
		m := map[string]any{"run_in_background": true}
		if v := sa.Get("TypeName"); v.Exists() {
			m["subagent_type"] = v.String()
		}
		if v := sa.Get("Prompt"); v.Exists() {
			m["prompt"] = v.String()
		}
		if v := sa.Get("Role"); v.Exists() {
			m["description"] = v.String()
		}
		switch sa.Get("Model").String() {
		case "flash_lite", "flash":
			m["model"] = "haiku"
		case "pro":
			m["model"] = "sonnet"
		}
		switch sa.Get("Workspace").String() {
		case "branch", "share":
			m["isolation"] = "worktree"
		}
		calls = append(calls, mappedClaudeCall{name: "Agent", args: m})
	}
	return calls
}

func mapSendMessage(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	if v := args.Get("Recipient"); v.Exists() {
		out["to"] = v.String()
	}
	if v := args.Get("Message"); v.Exists() {
		out["message"] = v.String()
	}
	summary := "Send message"
	if v := args.Get("toolSummary"); v.Exists() {
		if s := strings.TrimSpace(v.String()); s != "" {
			summary = s
		}
	}
	out["summary"] = summary
	return []mappedClaudeCall{{name: "SendMessage", args: out}}
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
