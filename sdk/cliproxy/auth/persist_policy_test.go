package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingStore struct {
	saveCount atomic.Int32
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

func TestMarkResult_429SyncsCooldownStateToMetadata(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	retryAfter := 2 * time.Minute
	mgr.MarkResult(context.Background(), Result{
		AuthID:     "auth-1",
		Provider:   "codex",
		Model:      "gpt-5",
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "quota exhausted",
			Retryable:  true,
			HTTPStatus: 429,
		},
	})

	updated, ok := mgr.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("GetByID() auth missing")
	}
	raw, ok := updated.Metadata[cooldownStateMetadataKey]
	if !ok {
		t.Fatalf("metadata missing %q", cooldownStateMetadataKey)
	}
	cooldown, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("metadata[%q] type = %T, want map[string]any", cooldownStateMetadataKey, raw)
	}
	if got, _ := cooldown["unavailable"].(bool); !got {
		t.Fatalf("cooldown_state.unavailable = %v, want true", cooldown["unavailable"])
	}
	modelStates, ok := cooldown["model_states"].(map[string]any)
	if !ok {
		t.Fatalf("cooldown_state.model_states type = %T, want map[string]any", cooldown["model_states"])
	}
	if _, ok := modelStates["gpt-5"]; !ok {
		t.Fatalf("cooldown_state.model_states missing gpt-5")
	}
}

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}
