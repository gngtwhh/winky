package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"sessions", "sessions"},
		{"/var/lib/winky", "/var/lib/winky"},
		{"~", home},
		{"~/.winky", filepath.Join(home, ".winky")},
		{"~/a/b", filepath.Join(home, "a", "b")},
		// Only a leading ~ is expanded; ~ elsewhere is an ordinary character.
		{"a/~/b", "a/~/b"},
		{"~user/winky", "~user/winky"},
	}
	for _, tc := range tests {
		got, err := expandHome(tc.in)
		if err != nil {
			t.Fatalf("expandHome(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// writeConfig drops a config file in a temporary directory and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadExpandsAgentDir(t *testing.T) {
	t.Setenv("API_KEY", "test-key")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	cfg, err := Load(writeConfig(t, `{
		"provider": {"name": "p", "model": "m"},
		"agent":    {"agentDir": "~/.winky-test"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, ".winky-test"); cfg.Agent.AgentDir != want {
		t.Fatalf("agentDir = %q, want %q", cfg.Agent.AgentDir, want)
	}
}

// The default agent directory is already absolute, so expansion must leave it
// untouched.
func TestLoadKeepsAbsoluteAgentDir(t *testing.T) {
	t.Setenv("API_KEY", "test-key")
	dir := t.TempDir()

	cfg, err := Load(writeConfig(t, `{
		"provider": {"name": "p", "model": "m"},
		"agent":    {"agentDir": "`+filepath.ToSlash(dir)+`"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.ToSlash(dir); cfg.Agent.AgentDir != want {
		t.Fatalf("agentDir = %q, want %q", cfg.Agent.AgentDir, want)
	}
}

// log_level never did anything; it is gone, and unknown members are rejected so
// a stale config fails loudly instead of being silently ignored.
func TestLoadRejectsUnknownMembers(t *testing.T) {
	t.Setenv("API_KEY", "test-key")

	if _, err := Load(writeConfig(t, `{
		"provider":  {"name": "p", "model": "m"},
		"log_level": "info"
	}`)); err == nil {
		t.Fatal("expected an error for the removed log_level field")
	}
}

func TestLoadRequiresKeyAndModel(t *testing.T) {
	t.Setenv("API_KEY", "")

	if _, err := Load(writeConfig(t, `{"provider": {"model": "m"}}`)); err == nil {
		t.Fatal("expected an error for the missing api key")
	}
	if _, err := Load(writeConfig(t, `{"provider": {"name": "p"}}`)); err == nil {
		t.Fatal("expected an error for the missing model")
	}
}
