package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravityClaudeNativeUserAgent = "antigravity/cli/1.5.15 (aidev_client; os_type=windows; arch=amd64; cl=1027820220; auth_method=consumer)"

type claudeNativeToolsKey struct{}

type claudeNativeToolsState struct {
	preferPowerShell  bool
	piTools           bool
	extraSystemPrompt string
}

// AnnotateClaudeNativeTools marks ctx so Antigravity Claude native-tool rewrite
// and header override run later without changing executor signatures.
func AnnotateClaudeNativeTools(ctx context.Context, from sdktranslator.Format, cfg *config.Config, originAgents ...string) context.Context {
	if ctx == nil || from != sdktranslator.FormatClaude {
		return ctx
	}
	extra := ""
	if cfg != nil {
		extra = strings.TrimSpace(cfg.Codex.ExtraSystemPrompt)
	}
	piTools := len(originAgents) > 0 && strings.Contains(originAgents[0], "pi (")
	return context.WithValue(ctx, claudeNativeToolsKey{}, &claudeNativeToolsState{extraSystemPrompt: extra, piTools: piTools})
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
// codex.extra-system-prompt to the native text when set. The incoming system
// text is prepended as a user message without changing existing history.
func RewriteRequestTools(ctx context.Context, payload []byte) []byte {
	if claudeNativeToolsFromCtx(ctx) == nil || len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !RewriteRequestToolsDoc(ctx, doc) {
		return payload
	}
	return doc.Bytes()
}

// RewriteRequestToolsDoc applies native-tool compatibility before payload finalization.
func RewriteRequestToolsDoc(ctx context.Context, doc *ojson.Doc) bool {
	state := claudeNativeToolsFromCtx(ctx)
	if state == nil || doc == nil {
		return false
	}
	// Dedicated web-search requests keep their tools and search instruction.
	if doc.Get("requestType").String() == "web_search" {
		return false
	}
	tools := doc.Get("request.tools")
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
	_ = doc.SetRawBytes("request.tools", replaced)
	if state.piTools {
		rewritePiSystem(doc, state)
	} else {
		rewriteClaudeCodeSystem(doc, state)
	}
	return true
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
	rewritten, ok := mapResponseParts(ojson.GetBytesNoCopy(body, path), state)
	if !ok {
		return body
	}
	doc := ojson.NewDoc(body)
	if errSet := doc.SetRawBytes(path, rewritten); errSet != nil {
		return body
	}
	return doc.Bytes()
}

// ApplyRequestHeaders overrides the upstream User-Agent on the Claude native-tool path.
func ApplyRequestHeaders(ctx context.Context, req *http.Request) {
	if req == nil || claudeNativeToolsFromCtx(ctx) == nil {
		return
	}
	req.Header.Set("User-Agent", antigravityClaudeNativeUserAgent)
}

func requestPrefersPowerShell(tools ojson.Result) bool {
	hasPowerShell, hasBash := false, false
	tools.ForEach(func(_, tool ojson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl ojson.Result) bool {
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

func extractClaudeMCPDeclarations(tools ojson.Result) [][]byte {
	var decls [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl ojson.Result) bool {
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
	if ojson.GetBytesNoCopy(body, "response.candidates.0.content.parts").Exists() {
		return "response.candidates.0.content.parts"
	}
	if ojson.GetBytesNoCopy(body, "candidates.0.content.parts").Exists() {
		return "candidates.0.content.parts"
	}
	return ""
}
