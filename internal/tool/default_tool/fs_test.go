package defaulttool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"winky/internal/tool"
)

func testFS(t *testing.T) *FS {
	t.Helper()
	fs, err := NewFS(t.TempDir())
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

func run(t *testing.T, tl tool.Tool, args string) tool.Result {
	t.Helper()
	res, err := tl.Invoke(context.Background(), tool.Call{Name: tl.Spec().Name, Arguments: args})
	if err != nil {
		t.Fatalf("%s(%s): %v", tl.Spec().Name, args, err)
	}
	return res
}

func TestFileCRUD(t *testing.T) {
	fs := testFS(t)

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

// Authorization happens before the call runs, so it is the registry's job: a
// tool's Invoke no longer asks, and a refused call never starts.
func TestPermissionDenies(t *testing.T) {
	// Read-only denies writes and allows reads.
	readOnly, err := NewDefault(t.TempDir(), tool.NewPolicy(tool.ReadOnly))
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	write := tool.Call{Name: "write_file", Arguments: `{"path":"a.txt","content":"x"}`}
	if err := readOnly.Allow(context.Background(), write); err == nil {
		t.Fatal("read-only mode allowed a write")
	} else if _, ok := err.(*tool.DeniedError); !ok {
		t.Fatalf("read-only write error = %v, want DeniedError", err)
	}
	if err := readOnly.Allow(context.Background(), tool.Call{
		Name: "read_file", Arguments: `{"path":"a.txt"}`,
	}); err != nil {
		t.Fatalf("read-only mode denied a read: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	args := fmt.Sprintf(`{"path":%q}`, outside)

	// Workspace-write denies reading outside the workspace.
	workspace, err := NewDefault(t.TempDir(), tool.NewPolicy(tool.WorkspaceWrite))
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	readOutside := tool.Call{Name: "read_file", Arguments: args}
	if err := workspace.Allow(context.Background(), readOutside); err == nil {
		t.Fatal("workspace mode allowed a read outside the workspace")
	} else if _, ok := err.(*tool.DeniedError); !ok {
		t.Fatalf("outside read error = %v, want DeniedError", err)
	}

	// Full access allows it.
	full, err := NewDefault(t.TempDir(), tool.NewPolicy(tool.FullAccess))
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	if err := full.Allow(context.Background(), readOutside); err != nil {
		t.Fatalf("full access denied a read: %v", err)
	}
	if res := full.Run(context.Background(), readOutside); res.IsError ||
		!strings.Contains(res.Output, "secret") {
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
