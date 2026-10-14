package model

import (
	"os"
	"path/filepath"
	"testing"
)

const testCatalog = `{
	"deepseek-flash": {
		"provider": "deepseek",
		"protocol": "openai-completions",
		"family": "deepseek-flash",
		"baseUrl": "https://api.deepseek.com"
	},
	"deepseek-v4-pro": {
		"provider": "deepseek",
		"protocol": "openai-completions",
		"family": "deepseek-v4-pro",
		"baseUrl": "https://api.deepseek.com"
	}
}`

func loadTestCatalog(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(testCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadCatalog(path); err != nil {
		t.Fatal(err)
	}
}

func TestLookupResolvesProtocolAndBaseURL(t *testing.T) {
	loadTestCatalog(t)

	m, err := lookup("deepseek-flash")
	if err != nil {
		t.Fatal(err)
	}
	if m.Protocol != "openai-completions" {
		t.Errorf("protocol = %q, want openai-completions", m.Protocol)
	}
	if m.BaseURL != "https://api.deepseek.com" {
		t.Errorf("baseURL = %q, want https://api.deepseek.com", m.BaseURL)
	}
	if m.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek", m.Provider)
	}
}

func TestFixFillsProtocolAndBaseURLFromCatalog(t *testing.T) {
	loadTestCatalog(t)

	m := &Model{Name: "deepseek-flash"}
	if err := Fix(m); err != nil {
		t.Fatal(err)
	}
	if m.Protocol != "openai-completions" {
		t.Errorf("protocol = %q, want openai-completions", m.Protocol)
	}
	if m.BaseURL != "https://api.deepseek.com" {
		t.Errorf("baseURL = %q, want https://api.deepseek.com", m.BaseURL)
	}
}

func TestFixKeepsExplicitBaseURLOverride(t *testing.T) {
	loadTestCatalog(t)

	m := &Model{Name: "deepseek-flash", BaseURL: "https://proxy.example.com"}
	if err := Fix(m); err != nil {
		t.Fatal(err)
	}
	if m.BaseURL != "https://proxy.example.com" {
		t.Errorf("baseURL = %q, want explicit override to win", m.BaseURL)
	}
	if m.Protocol != "openai-completions" {
		t.Errorf("protocol = %q, want openai-completions", m.Protocol)
	}
}
