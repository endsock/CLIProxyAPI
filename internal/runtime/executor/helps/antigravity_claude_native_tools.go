package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityClaudeNativeUserAgent = "antigravity/cli/1.1.22 (aidev_client; os_type=windows; arch=amd64; cl=971564011; auth_method=consumer)"

type claudeNativeToolsKey struct{}

type claudeNativeToolsState struct {
	preferPowerShell  bool
	extraSystemPrompt string
}

// AnnotateClaudeNativeTools marks ctx so Antigravity Claude native-tool rewrite
// and header override run later without changing executor signatures.
func AnnotateClaudeNativeTools(ctx context.Context, from sdktranslator.Format, cfg *config.Config) context.Context {
	if ctx == nil || from != sdktranslator.FormatClaude {
		return ctx
	}
	extra := ""
	if cfg != nil {
		extra = strings.TrimSpace(cfg.Codex.ExtraSystemPrompt)
	}
	return context.WithValue(ctx, claudeNativeToolsKey{}, &claudeNativeToolsState{extraSystemPrompt: extra})
}

func claudeNativeToolsFromCtx(ctx context.Context) *claudeNativeToolsState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(claudeNativeToolsKey{}).(*claudeNativeToolsState)
	return state
}

// RewriteRequestTools replaces request.tools with the Antigravity native catalog
// plus translator-produced mcp__* declarations, overwrites
// request.systemInstruction with the captured Antigravity prompt, then appends
// codex.extra-system-prompt as an extra parts item when set. History contents
// are not touched.
func RewriteRequestTools(ctx context.Context, payload []byte) []byte {
	state := claudeNativeToolsFromCtx(ctx)
	if state == nil || len(payload) == 0 {
		return payload
	}
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return payload
	}
	state.preferPowerShell = requestPrefersPowerShell(tools)
	mcpDecls := extractClaudeMCPDeclarations(tools)
	replaced := agyNativeToolsCatalogArray
	if len(mcpDecls) > 0 {
		items := make([][]byte, 0, len(agyNativeToolObjects)+len(mcpDecls))
		items = append(items, agyNativeToolObjects...)
		for _, decl := range mcpDecls {
			items = append(items, wrapFunctionDeclaration(decl))
		}
		replaced = JoinRawJSONArray(items)
	}
	updated, errSet := sjson.SetRawBytes(payload, "request.tools", replaced)
	if errSet != nil {
		return payload
	}
	updated, errSet = sjson.SetRawBytes(updated, "request.systemInstruction", agyNativeSystemInstructionObject)
	if errSet != nil {
		return payload
	}
	return appendExtraSystemPromptPart(updated, state.extraSystemPrompt)
}

func appendExtraSystemPromptPart(payload []byte, extra string) []byte {
	if extra == "" {
		return payload
	}
	part, errSet := sjson.SetBytes([]byte(`{}`), "text", extra)
	if errSet != nil {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "request.systemInstruction.parts.-1", part)
	if errSet != nil {
		return payload
	}
	return updated
}

// MapResponseFunctionCalls rewrites complete upstream functionCall objects to
// Claude tool names/args. Unmapped names, mcp__*, and incomplete args pass through.
func MapResponseFunctionCalls(ctx context.Context, body []byte) []byte {
	state := claudeNativeToolsFromCtx(ctx)
	if state == nil || len(body) == 0 {
		return body
	}
	path := functionCallPartsPath(body)
	if path == "" {
		return body
	}
	rewritten, ok := mapResponseParts(gjson.GetBytes(body, path), state)
	if !ok {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, path, rewritten)
	if errSet != nil {
		return body
	}
	return updated
}

// ApplyRequestHeaders overrides the upstream User-Agent on the Claude native-tool path.
func ApplyRequestHeaders(ctx context.Context, req *http.Request) {
	if req == nil || claudeNativeToolsFromCtx(ctx) == nil {
		return
	}
	req.Header.Set("User-Agent", antigravityClaudeNativeUserAgent)
}

func requestPrefersPowerShell(tools gjson.Result) bool {
	hasPowerShell, hasBash := false, false
	tools.ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
			switch decl.Get("name").String() {
			case "PowerShell":
				hasPowerShell = true
			case "Bash":
				hasBash = true
			}
			return true
		})
		return true
	})
	return hasPowerShell && !hasBash
}

func extractClaudeMCPDeclarations(tools gjson.Result) [][]byte {
	var decls [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
			if IsClaudeMCPToolName(decl.Get("name").String()) {
				decls = append(decls, []byte(decl.Raw))
			}
			return true
		})
		return true
	})
	return decls
}

func wrapFunctionDeclaration(decl []byte) []byte {
	out := make([]byte, 0, len(decl)+len(`{"functionDeclarations":[]}`))
	out = append(out, `{"functionDeclarations":[`...)
	out = append(out, decl...)
	return append(out, `]}`...)
}

func functionCallPartsPath(body []byte) string {
	if gjson.GetBytes(body, "response.candidates.0.content.parts").Exists() {
		return "response.candidates.0.content.parts"
	}
	if gjson.GetBytes(body, "candidates.0.content.parts").Exists() {
		return "candidates.0.content.parts"
	}
	return ""
}
