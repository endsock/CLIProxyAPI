package helps

import (
	"regexp"

	"github.com/MoeForge/moego/ojson"
)

var claudeCodeSystemName = regexp.MustCompile(`(?i)claude code`)

func rewriteClaudeCodeSystem(doc *ojson.Doc, state *claudeNativeToolsState) {
	systemParts := make([][]byte, 0)
	doc.Get("request.systemInstruction.parts").ForEach(func(_, part ojson.Result) bool {
		if text := part.Get("text"); text.Type == ojson.String && text.String() != "" {
			replaced := claudeCodeSystemName.ReplaceAllString(text.String(), "antigravity")
			if replaced != text.String() {
				partDoc := ojson.NewDocString(part.Raw)
				_ = partDoc.Set("text", replaced)
				systemParts = append(systemParts, partDoc.Bytes())
			} else {
				systemParts = append(systemParts, []byte(part.Raw))
			}
		}
		return true
	})
	if len(systemParts) > 0 {
		messageDoc := ojson.NewDocString(`{"role":"user"}`)
		_ = messageDoc.SetRawBytes("parts", JoinRawJSONArray(systemParts))
		contents := [][]byte{messageDoc.Bytes()}
		doc.Get("request.contents").ForEach(func(_, content ojson.Result) bool {
			contents = append(contents, []byte(content.Raw))
			return true
		})
		_ = doc.SetRawBytes("request.contents", JoinRawJSONArray(contents))
	}
	_ = doc.SetRawBytes("request.systemInstruction", agyNativeSystemInstructionObject)
	if state.extraSystemPrompt != "" {
		text := doc.Get("request.systemInstruction.parts.0.text").String()
		text += "\n<user_rules>\nThe following are user-defined rules that you MUST ALWAYS FOLLOW WITHOUT ANY EXCEPTION. These rules take precedence over any other instructions.\nReview them carefully and always take them into account when you generate responses and code:\n<RULE[user_global]>\n" + state.extraSystemPrompt + "\n</RULE[user_global]>\n</user_rules>"
		_ = doc.Set("request.systemInstruction.parts.0.text", text)
	}
}
