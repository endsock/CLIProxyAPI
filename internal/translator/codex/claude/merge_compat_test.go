package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeCompatibilityExtraSystemPrompt(t *testing.T) {
	t.Cleanup(func() { SetExtraSystemPrompt("") })
	for _, system := range []string{"", `,"system":"original"`} {
		input := []byte(`{"model":"alias","messages":[{"role":"user","content":"hello"}]` + system + `}`)
		SetExtraSystemPrompt("first")
		first, err := ConvertClaudeRequestToCodex("upstream", input, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(first, []byte(`"text":"first"`)) {
			t.Fatalf("extra system prompt missing: %s", first)
		}
		SetExtraSystemPrompt("updated")
		updated, err := ConvertClaudeRequestToCodex("upstream", input, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(updated, []byte(`"text":"updated"`)) || bytes.Contains(updated, []byte(`"text":"first"`)) {
			t.Fatalf("prompt update did not reach the next request: %s", updated)
		}
		if system != "" && !bytes.Contains(updated, []byte(`"text":"original"`)) {
			t.Fatal("extra prompt replaced the caller system prompt")
		}
		SetExtraSystemPrompt("")
		cleared, err := ConvertClaudeRequestToCodex("upstream", input, false)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(cleared, []byte(`"text":"updated"`)) {
			t.Fatal("cleared prompt was retained")
		}
	}
}

func TestMergeCompatibilityResponseUsesUpstreamModel(t *testing.T) {
	for _, tc := range []struct{ original, want string }{
		{`{"model":"client-alias"}`, "upstream-model"},
		{`{}`, "upstream-model"},
	} {
		var param any
		stream := ConvertCodexResponseToClaude(context.Background(), "", []byte(tc.original), nil,
			[]byte(`data: {"type":"response.created","response":{"id":"r","model":"upstream-model"}}`), &param)
		if !bytes.Contains(bytes.Join(stream, nil), []byte(`"model":"`+tc.want+`"`)) {
			t.Fatalf("stream did not use upstream model: %s", bytes.Join(stream, nil))
		}
		nonstream := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(tc.original), nil,
			[]byte(`{"type":"response.completed","response":{"id":"r","model":"upstream-model","output":[],"usage":{}}}`), nil)
		if gjson.GetBytes(nonstream, "model").String() != tc.want {
			t.Fatalf("nonstream did not use upstream model: %s", nonstream)
		}
	}
}
