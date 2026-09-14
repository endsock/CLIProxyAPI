package executor

import (
	"context"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestPrepareAntigravityGeminiReasoningReplayPreservesToolErrorOnDegradation verifies that provenance fallback does not erase tool failures.
func TestPrepareAntigravityGeminiReasoningReplayPreservesToolErrorOnDegradation(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		modelName    = "gemini-3.6-flash-high"
		toolResponse = "EISDIR: illegal operation on a directory, read '/tmp/a'"
	)
	clientID := util.GeminiClaudeToolUseID("native-missing", "Read", `{"file_path":"/tmp/a"}`)
	payload := []byte(`{"sessionId":"sess-missing-error-provenance","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID + `","name":"Read","args":{"file_path":"/tmp/a"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Read","response":{"error":"` + toolResponse + `"}}}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(
		context.Background(),
		modelName,
		cliproxyexecutor.Request{Model: modelName, Payload: payload},
		opts,
		payload,
	)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	if antigravityPayloadHasClaudeToolProvenanceID(out) {
		t.Fatalf("reserved provenance IDs leaked upstream: %s", out)
	}

	call := gjson.GetBytes(out, "request.contents.0.parts.0")
	response := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse")
	callID := call.Get("functionCall.id").String()
	if callID == "" || callID != response.Get("id").String() {
		t.Fatalf("degraded call/response pairing broken: call=%q response=%q", callID, response.Get("id").String())
	}
	if got := call.Get("thoughtSignature").String(); got != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first degraded call thoughtSignature = %q, want bypass sentinel", got)
	}
	if got := response.Get("response.error").String(); got != toolResponse {
		t.Fatalf("response.error = %q, want %q; output=%s", got, toolResponse, out)
	}
	if response.Get("response.result").Exists() {
		t.Fatalf("response.result must remain absent after degradation: %s", out)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("degraded history is invalid: %v", errPairing)
	}
}
