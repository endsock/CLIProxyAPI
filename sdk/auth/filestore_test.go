package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestReadAuthFile_RestoreCooldownState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	next := time.Now().Add(10 * time.Minute).UTC()
	raw := []byte(`{
		"type": "codex",
		"email": "user@example.com",
		"cooldown_state": {
			"status": "error",
			"status_message": "quota exhausted",
			"unavailable": true,
			"next_retry_after": "` + next.Format(time.RFC3339) + `",
			"quota": {
				"exceeded": true,
				"reason": "quota",
				"next_recover_at": "` + next.Format(time.RFC3339) + `"
			},
			"model_states": {
				"gpt-5": {
					"status": "error",
					"status_message": "quota exhausted",
					"unavailable": true,
					"next_retry_after": "` + next.Format(time.RFC3339) + `",
					"quota": {
						"exceeded": true,
						"reason": "quota",
						"next_recover_at": "` + next.Format(time.RFC3339) + `"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	store := NewFileTokenStore()
	auth, err := store.readAuthFile(path, dir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if auth == nil {
		t.Fatalf("readAuthFile() auth = nil")
	}
	if auth.Status != cliproxyauth.StatusError {
		t.Fatalf("auth.Status = %q, want %q", auth.Status, cliproxyauth.StatusError)
	}
	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if !auth.Quota.Exceeded {
		t.Fatalf("auth.Quota.Exceeded = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want restored value")
	}
	state := auth.ModelStates["gpt-5"]
	if state == nil {
		t.Fatalf("auth.ModelStates[gpt-5] = nil")
	}
	if !state.Unavailable {
		t.Fatalf("state.Unavailable = false, want true")
	}
	if !state.Quota.Exceeded {
		t.Fatalf("state.Quota.Exceeded = false, want true")
	}
}

func TestReadAuthFile_ExpiredCooldownStateCleared(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	expired := time.Now().Add(-10 * time.Minute).UTC()
	raw := []byte(`{
		"type": "codex",
		"cooldown_state": {
			"status": "error",
			"status_message": "quota exhausted",
			"unavailable": true,
			"next_retry_after": "` + expired.Format(time.RFC3339) + `",
			"quota": {
				"exceeded": true,
				"reason": "quota",
				"next_recover_at": "` + expired.Format(time.RFC3339) + `"
			}
		}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	store := NewFileTokenStore()
	auth, err := store.readAuthFile(path, dir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if auth == nil {
		t.Fatalf("readAuthFile() auth = nil")
	}
	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if auth.Quota.Exceeded {
		t.Fatalf("auth.Quota.Exceeded = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
	if _, ok := auth.Metadata["cooldown_state"]; ok {
		t.Fatalf("auth.Metadata[cooldown_state] still exists after restore")
	}
}
