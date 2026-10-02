package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExtraSystemPromptLayoutCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"legacy", "codex: {extra-system-prompt: legacy}\n", "legacy"},
		{"v8", "client: {codex: {extra-system-prompt: shared}}\n", "shared"},
		{"v8 wins", "codex: {extra-system-prompt: legacy}\nclient: {codex: {extra-system-prompt: shared}}\n", "shared"},
		{"explicit empty wins", "codex: {extra-system-prompt: legacy}\nclient: {codex: {extra-system-prompt: ''}}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfigBytes([]byte(tc.raw + "oauth: {providers: {codex: {response-steering: true}}}\n"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Codex.ExtraSystemPrompt != tc.want || cfg.ForAPIKey().Codex.ExtraSystemPrompt != tc.want {
				t.Fatalf("prompt lost or incorrectly scoped: %+v", cfg.Codex)
			}
			if cfg.ForAPIKey().Codex.ResponseSteering || !cfg.Codex.ResponseSteering {
				t.Fatal("shared prompt changed OAuth-only steering scope")
			}
			data, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			cloned, err := ParseConfigBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			if cloned.ForAPIKey().Codex.ExtraSystemPrompt != tc.want {
				t.Fatal("snapshot lost shared prompt")
			}
			migrated, _, err := NormalizeConfigLayout([]byte(tc.raw), true)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateV8Config(migrated); err != nil {
				t.Fatalf("invalid migrated configuration: %v\n%s", err, migrated)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(migrated, &doc); err != nil {
				t.Fatal(err)
			}
			value := yamlPath(doc.Content[0], "client.codex.extra-system-prompt")
			if value == nil || value.Value != tc.want || yamlPath(doc.Content[0], "codex.extra-system-prompt") != nil {
				t.Fatalf("incorrect migrated prompt path: %s", migrated)
			}
		})
	}
}

func TestExtraSystemPromptSaveReload(t *testing.T) {
	for _, raw := range []string{
		"codex: {extra-system-prompt: initial}\n",
		"config-version: 8\nclient: {codex: {extra-system-prompt: initial}}\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Codex.ExtraSystemPrompt = "updated"
		if err = SaveConfigPreserveComments(path, cfg, true); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.ForAPIKey().Codex.ExtraSystemPrompt != "updated" {
			t.Fatal("save/reload lost global extra prompt")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateV8Config(data); err != nil || !strings.Contains(string(data), "extra-system-prompt: updated") {
			t.Fatalf("saved prompt is not valid v8 configuration: %v\n%s", err, data)
		}
	}
}
