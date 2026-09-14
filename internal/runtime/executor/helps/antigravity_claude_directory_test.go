package helps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestNativeDirectoryMapping preserves listing semantics and literal path arguments.
func TestNativeDirectoryMapping(t *testing.T) {
	for _, tt := range []struct {
		name       string
		path       string
		powerShell bool
		want       string
	}{
		{"posix", "/tmp/project", false, "ls -la -- '/tmp/project'"},
		{"posix quotes", "/tmp/a'b $(whoami);x", false, "ls -la -- '/tmp/a'\"'\"'b $(whoami);x'"},
		{"powershell", "C:\\a'b [x] $HOME", true, "Get-ChildItem -Force -LiteralPath 'C:\\a''b [x] $HOME'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"DirectoryPath": tt.path})
			if err != nil {
				t.Fatal(err)
			}
			state := &claudeNativeToolsState{preferPowerShell: tt.powerShell}
			calls := mapListDir(gjson.ParseBytes(args), state)
			wantName := "Bash"
			if tt.powerShell {
				wantName = "PowerShell"
			}
			if len(calls) != 1 || calls[0].name != wantName || calls[0].args["command"] != tt.want {
				t.Fatalf("mapping = %#v, want %s %q", calls, wantName, tt.want)
			}
		})
	}
}

// TestNativeDirectoryListingExecutes validates the mapped command, not a model's self-report.
func TestNativeDirectoryListingExecutes(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "a'b $(printf injected); [x]")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]string{"DirectoryPath": dir})
	if err != nil {
		t.Fatal(err)
	}
	calls := mapListDir(gjson.ParseBytes(args), nil)
	output, err := exec.Command("sh", "-c", calls[0].args["command"].(string)).CombinedOutput()
	if err != nil {
		t.Fatalf("listing failed: %v: %s", err, output)
	}
	for _, name := range []string{"visible.txt", ".hidden"} {
		if !strings.Contains(string(output), name) {
			t.Fatalf("missing %s in listing", name)
		}
	}
}

// TestNativeDirectoryResponseEnvelopes covers the nested and direct response shapes.
func TestNativeDirectoryResponseEnvelopes(t *testing.T) {
	for _, nested := range []bool{false, true} {
		body := []byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"signed-part","functionCall":{"id":"native-list","name":"list_dir","args":{"DirectoryPath":"/tmp"}}}]}}]}`)
		path := "candidates.0.content.parts.0"
		if nested {
			body = append(append([]byte(`{"response":`), body...), '}')
			path = "response." + path
		}
		ctx := AnnotateClaudeNativeTools(context.Background(), sdktranslator.FormatClaude, nil)
		out := MapResponseFunctionCalls(ctx, body)
		part := gjson.GetBytes(out, path)
		if part.Get("functionCall.name").String() != "Bash" || part.Get("functionCall.id").String() != "native-list" || part.Get("thoughtSignature").String() != "signed-part" {
			t.Fatalf("mapping lost tool identity or listing semantics: %s", out)
		}
	}
}
