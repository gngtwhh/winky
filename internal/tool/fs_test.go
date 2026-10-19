package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFS(t *testing.T, p *Policy) *FS {
	t.Helper()
	if p == nil {
		p = NewPolicy(WorkspaceWrite)
	}
	fs, err := NewFS(t.TempDir(), p)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	return fs
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, tl Tool, args string) Result {
	t.Helper()
	res, err := tl.Invoke(context.Background(), Call{Name: tl.Spec().Name, Arguments: args})
	if err != nil {
		t.Fatalf("%s(%s): %v", tl.Spec().Name, args, err)
	}
	return res
}

func TestFileCRUD(t *testing.T) {
	fs := testFS(t, nil)

	if res := run(t, fs.writeFile(), `{"path":"a.txt","content":"one\ntwo\n"}`); res.IsError {
		t.Fatalf("write: %s", res.Output)
	}
	if res := run(t, fs.readFile(), `{"path":"a.txt"}`); !strings.Contains(res.Output, "1\tone") {
		t.Fatalf("read: %s", res.Output)
	}
	if res := run(t, fs.editFile(), `{"path":"a.txt","old_string":"two","new_string":"TWO"}`); res.IsError {
		t.Fatalf("edit: %s", res.Output)
	}
	if res := run(t, fs.listDir(), `{}`); !strings.Contains(res.Output, "a.txt") {
		t.Fatalf("list: %s", res.Output)
	}
	if res := run(t, fs.delete(), `{"path":"a.txt"}`); res.IsError {
		t.Fatalf("delete: %s", res.Output)
	}
	if _, err := os.Stat(filepath.Join(fs.Root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: %v", err)
	}
}

func TestPermissionDenies(t *testing.T) {
	// Read-only denies writes.
	fs := testFS(t, NewPolicy(ReadOnly))
	_, err := fs.writeFile().Invoke(context.Background(), Call{
		Name: "write_file", Arguments: `{"path":"a.txt","content":"x"}`,
	})
	if _, ok := err.(*DeniedError); !ok {
		t.Fatalf("read-only write error = %v, want DeniedError", err)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	args := fmt.Sprintf(`{"path":%q}`, outside)

	// Workspace-write denies reading outside the workspace.
	fs2 := testFS(t, nil)
	_, err = fs2.readFile().Invoke(context.Background(), Call{Name: "read_file", Arguments: args})
	if _, ok := err.(*DeniedError); !ok {
		t.Fatalf("outside read error = %v, want DeniedError", err)
	}

	// Full access allows it.
	fs3 := testFS(t, NewPolicy(FullAccess))
	if res := run(t, fs3.readFile(), args); res.IsError || !strings.Contains(res.Output, "secret") {
		t.Fatalf("full-access read = %+v, want the secret", res)
	}
}

func TestNewDefaultRegisters(t *testing.T) {
	reg, err := NewDefault(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Names()); got != 9 {
		t.Fatalf("registered %d tools, want 9: %v", got, reg.Names())
	}
}
