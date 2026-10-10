package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Pi mappings are upstream-to-client only; unsupported native tools pass through.
var agyPiFunctionMappers = map[string]agyClaudeCallMapper{
	"view_file":            mapPiViewFile,
	"write_to_file":        mapPiWriteToFile,
	"replace_file_content": mapPiReplaceFileContent,
	"run_command":          mapPiRunCommand,
	"list_dir":             mapPiListDir,
	"find_by_name":         mapPiFindByName,
	"grep_search":          mapPiGrepSearch,
	"ask_question":         mapPiAskQuestion,
}

func mapPiViewFile(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall {
	if args.Get("ContentOffset").Int() != 0 {
		return nil
	}
	calls := mapViewFile(args, state)
	calls[0].name = "read"
	if path, ok := calls[0].args["file_path"]; ok {
		calls[0].args["path"] = path
		delete(calls[0].args, "file_path")
	}
	return calls
}

func mapPiWriteToFile(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall {
	calls := mapWriteToFile(args, state)
	calls[0].name = "write"
	if path, ok := calls[0].args["file_path"]; ok {
		calls[0].args["path"] = path
		delete(calls[0].args, "file_path")
	}
	return calls
}

func mapPiReplaceFileContent(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	// Pi exact edits cannot express replacement of multiple occurrences.
	if args.Get("AllowMultiple").Bool() {
		return nil
	}
	out := map[string]any{}
	if path := args.Get("TargetFile"); path.Exists() {
		out["path"] = path.String()
	}
	edit := map[string]any{}
	if old := args.Get("TargetContent"); old.Exists() {
		edit["oldText"] = old.String()
	}
	if replacement := args.Get("ReplacementContent"); replacement.Exists() {
		edit["newText"] = replacement.String()
	}
	out["edits"] = []any{edit}
	return []mappedClaudeCall{{name: "edit", args: out}}
}

func mapPiRunCommand(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	out := map[string]any{}
	command := args.Get("CommandLine")
	if command.Exists() {
		cmd := command.String()
		if cwd := args.Get("Cwd").String(); strings.TrimSpace(cwd) != "" {
			cmd = "cd -- " + quoteAntigravityShellLiteral(cwd, false) + " && " + cmd
		}
		out["command"] = cmd
	}
	// WaitMsBeforeAsync is an async handoff delay, not an execution timeout.
	return []mappedClaudeCall{{name: "bash", args: out}}
}

func mapPiListDir(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	calls := mapListDir(args, nil)
	if len(calls) == 0 {
		return nil
	}
	calls[0].name = "bash"
	delete(calls[0].args, "description")
	return calls
}

func mapPiFindByName(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	command := "fd --glob --max-results 50"
	if depth := args.Get("MaxDepth"); depth.Exists() {
		command += " --max-depth " + quoteAntigravityShellLiteral(depth.Raw, false)
	}
	switch args.Get("Type").String() {
	case "file":
		command += " --type f"
	case "directory":
		command += " --type d"
	}
	if args.Get("FullPath").Bool() {
		command += " --full-path"
	}
	for _, extension := range args.Get("Extensions").Array() {
		command += " --extension " + quoteAntigravityShellLiteral(strings.TrimPrefix(extension.String(), "."), false)
	}
	for _, exclude := range args.Get("Excludes").Array() {
		command += " --exclude " + quoteAntigravityShellLiteral(exclude.String(), false)
	}
	command += " -- " + quoteAntigravityShellLiteral(args.Get("Pattern").String(), false)
	if path := args.Get("SearchDirectory"); path.Exists() {
		command += " " + quoteAntigravityShellLiteral(path.String(), false)
	}
	return []mappedClaudeCall{{name: "bash", args: map[string]any{"command": command}}}
}

func mapPiGrepSearch(args gjson.Result, _ *claudeNativeToolsState) []mappedClaudeCall {
	command := "rg"
	if args.Get("MatchPerLine").Bool() {
		command += " -n"
	} else {
		command += " -l"
	}
	if isRegex := args.Get("IsRegex"); isRegex.Exists() && !isRegex.Bool() {
		command += " -F"
	}
	if args.Get("CaseInsensitive").Bool() {
		command += " -i"
	}
	for _, include := range args.Get("Includes").Array() {
		command += " -g " + quoteAntigravityShellLiteral(include.String(), false)
	}
	command += " -- " + quoteAntigravityShellLiteral(args.Get("Query").String(), false)
	if path := args.Get("SearchPath"); path.Exists() {
		command += " " + quoteAntigravityShellLiteral(path.String(), false)
	}
	return []mappedClaudeCall{{name: "bash", args: map[string]any{"command": command}}}
}

func mapPiAskQuestion(args gjson.Result, state *claudeNativeToolsState) []mappedClaudeCall {
	calls := mapAskQuestion(args, state)
	if len(calls) > 0 {
		calls[0].name = "ask_user_question"
	}
	return calls
}
