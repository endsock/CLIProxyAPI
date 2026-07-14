package claude

import "sync/atomic"

// extraSystemPrompt holds the configurable text appended to the system prompt
// when translating Claude Code requests into the Codex request format.
// The translator package has no direct access to internal/config.Config, so
// callers (e.g. cmd/server, config hot-reload) push updates via SetExtraSystemPrompt.
var extraSystemPrompt atomic.Value

// SetExtraSystemPrompt updates the extra system prompt used by ConvertClaudeRequestToCodex.
func SetExtraSystemPrompt(prompt string) {
	extraSystemPrompt.Store(prompt)
}

// getExtraSystemPrompt returns the currently configured extra system prompt, if any.
func getExtraSystemPrompt() string {
	v, _ := extraSystemPrompt.Load().(string)
	return v
}
