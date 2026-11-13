package cli

import (
	"context"
	"testing"
)

func TestRootCommandFlags(t *testing.T) {
	var got *options
	cmd := newRootCmd(func(_ context.Context, o *options) error {
		got = o
		return nil
	})
	cmd.SetArgs([]string{
		"--config", "conf.json",
		"--catalog", "models.json",
		"--prompt", "prompt.md",
		"-r",
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got == nil {
		t.Fatal("run was not called")
	}
	want := options{
		configPath:  "conf.json",
		catalogPath: "models.json",
		promptPath:  "prompt.md",
		resume:      true,
	}
	if *got != want {
		t.Fatalf("options = %+v, want %+v", *got, want)
	}
}

func TestRootCommandDefaults(t *testing.T) {
	var got *options
	cmd := newRootCmd(func(_ context.Context, o *options) error {
		got = o
		return nil
	})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := options{catalogPath: "catalog.json"}
	if got == nil || *got != want {
		t.Fatalf("options = %+v, want %+v", got, want)
	}
}

func TestRootCommandRejectsArgs(t *testing.T) {
	called := false
	cmd := newRootCmd(func(_ context.Context, _ *options) error {
		called = true
		return nil
	})
	cmd.SetArgs([]string{"extra"})
	cmd.SetOut(nil)
	cmd.SetErr(nil)
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected an error for positional arguments")
	}
	if called {
		t.Fatal("run should not be called")
	}
}

func TestNormalizeArgs(t *testing.T) {
	in := []string{"-config", "c.json", "-catalog=cat.json", "-prompt", "p.md", "-r", "--", "-config"}
	want := []string{"--config", "c.json", "--catalog=cat.json", "--prompt", "p.md", "-r", "--", "-config"}
	got := normalizeArgs(in)
	if len(got) != len(want) {
		t.Fatalf("normalizeArgs = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeArgs = %q, want %q", got, want)
		}
	}
}
