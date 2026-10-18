package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testFS builds a filesystem tool set rooted in a fresh temp directory.
func testFS(t *testing.T, writable bool) *FS {
	t.Helper()
	fs, err := NewFS(t.TempDir(), writable)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	return fs
}

// writeTestFile creates a file and any missing parent directories.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// invoke runs a tool and fails the test if it could not run at all.
func invoke(t *testing.T, tl Tool, args string) Result {
	t.Helper()
	res, err := tl.Invoke(context.Background(), Call{Name: tl.Spec().Name, Arguments: args})
	if err != nil {
		t.Fatalf("%s(%s): %v", tl.Spec().Name, args, err)
	}
	return res
}

// invokeErr runs a tool and returns the error it could not recover from.
func invokeErr(t *testing.T, tl Tool, args string) error {
	t.Helper()
	_, err := tl.Invoke(context.Background(), Call{Name: tl.Spec().Name, Arguments: args})
	if err == nil {
		t.Fatalf("%s(%s) = nil error, want one", tl.Spec().Name, args)
	}
	return err
}

// jsonArgs renders one path argument as a JSON object.
func jsonArgs(path string) string {
	return fmt.Sprintf(`{"path":%q}`, path)
}

func TestNewFSValidatesRoot(t *testing.T) {
	if _, err := NewFS("", false); err == nil {
		t.Fatal("NewFS(\"\") = nil error, want one")
	}
	if _, err := NewFS(filepath.Join(t.TempDir(), "absent"), false); err == nil {
		t.Fatal("NewFS(missing dir) = nil error, want one")
	}

	file := filepath.Join(t.TempDir(), "file.txt")
	writeTestFile(t, file, "x")
	if _, err := NewFS(file, false); err == nil {
		t.Fatal("NewFS(regular file) = nil error, want one")
	}
}

// A read-only workspace must not even offer the write tools: the model cannot
// waste a turn on a tool that is guaranteed to fail.
func TestToolsRespectWritable(t *testing.T) {
	readOnly := testFS(t, false).Tools()
	names := toolNames(readOnly)
	want := []string{"list_dir", "read_file"}
	if !equalStrings(names, want) {
		t.Fatalf("read-only tools = %v, want %v", names, want)
	}

	all := toolNames(testFS(t, true).Tools())
	want = []string{"edit_file", "list_dir", "read_file", "write_file"}
	if !equalStrings(all, want) {
		t.Fatalf("writable tools = %v, want %v", all, want)
	}
}

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, tl := range tools {
		names[i] = tl.Spec().Name
	}
	// The FS returns tools in a fixed order; sort for an order-independent check.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

func TestReadFileNumbersLines(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "one\ntwo\nthree\n")

	res := invoke(t, fs.readFile(), jsonArgs("a.txt"))
	if res.IsError {
		t.Fatalf("IsError = true: %s", res.Output)
	}
	for _, want := range []string{"a.txt: lines 1-3 of 3", "     1\tone", "     2\ttwo", "     3\tthree"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("Output = %q, want it to contain %q", res.Output, want)
		}
	}
}

func TestReadFileRange(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")

	res := invoke(t, fs.readFile(), `{"path":"a.txt","offset":3,"limit":2}`)
	for _, want := range []string{"lines 3-4 of 10", "     3\tc", "     4\td", "6 more lines"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("Output = %q, want it to contain %q", res.Output, want)
		}
	}
	if strings.Contains(res.Output, "     5\te") {
		t.Fatalf("Output = %q, want the limit to stop at line 4", res.Output)
	}
}

func TestReadFileOffsetPastEOF(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "one\ntwo\n")

	res := invoke(t, fs.readFile(), `{"path":"a.txt","offset":10}`)
	if !strings.Contains(res.Output, "no lines in range") {
		t.Fatalf("Output = %q, want a no-lines notice", res.Output)
	}
}

func TestReadFileRejectsDirectoriesAndBinaries(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "sub", "x.txt"), "x")
	writeTestFile(t, filepath.Join(fs.Root, "blob.bin"), "\x00\x01\x02")

	if err := invokeErr(t, fs.readFile(), jsonArgs("sub")); !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory read error = %v, want a directory hint", err)
	}
	if err := invokeErr(t, fs.readFile(), jsonArgs("blob.bin")); !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary read error = %v, want a binary refusal", err)
	}
	if err := invokeErr(t, fs.readFile(), jsonArgs("absent.txt")); err == nil {
		t.Fatal("missing file read = nil error, want one")
	}
}

// One pathological line must not flood the context window.
func TestReadFileClipsLongLines(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "min.js"), strings.Repeat("x", maxLineBytes*3))

	res := invoke(t, fs.readFile(), jsonArgs("min.js"))
	if !strings.Contains(res.Output, "…") {
		t.Fatalf("Output = %q, want a clip marker", res.Output)
	}
	if strings.Contains(res.Output, strings.Repeat("x", maxLineBytes+1)) {
		t.Fatalf("Output kept a %d-byte run, want it clipped at %d", maxLineBytes+1, maxLineBytes)
	}
}

func TestReadFileEmpty(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "empty.txt"), "")

	res := invoke(t, fs.readFile(), jsonArgs("empty.txt"))
	if !strings.Contains(res.Output, "empty file") {
		t.Fatalf("Output = %q, want an empty-file notice", res.Output)
	}
}

// CRLF files are common on Windows; the read view must not show stray \r.
func TestReadFileStripsCarriageReturn(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "win.txt"), "one\r\ntwo\r\n")

	res := invoke(t, fs.readFile(), jsonArgs("win.txt"))
	if strings.Contains(res.Output, "\r") {
		t.Fatalf("Output = %q, want carriage returns stripped", res.Output)
	}
}

func TestWriteFileCreatesParents(t *testing.T) {
	fs := testFS(t, true)

	res := invoke(t, fs.writeFile(), `{"path":"nested/dir/x.txt","content":"hi"}`)
	if res.IsError || !strings.Contains(res.Output, "wrote 2 bytes to nested/dir/x.txt") {
		t.Fatalf("write result = %+v, want a byte count for the nested path", res)
	}
	data, err := os.ReadFile(filepath.Join(fs.Root, "nested", "dir", "x.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hi" {
		t.Fatalf("content = %q, want %q", data, "hi")
	}
}

func TestWriteFileReadOnly(t *testing.T) {
	fs := testFS(t, false)
	if err := invokeErr(t, fs.writeFile(), `{"path":"x.txt","content":"hi"}`); err != errReadOnly {
		t.Fatalf("write error = %v, want errReadOnly", err)
	}
}

func TestEditFileReplacesUniqueMatch(t *testing.T) {
	fs := testFS(t, true)
	path := filepath.Join(fs.Root, "a.txt")
	writeTestFile(t, path, "one\ntwo\nthree\n")

	res := invoke(t, fs.editFile(), `{"path":"a.txt","old_string":"two","new_string":"TWO"}`)
	if res.IsError || !strings.Contains(res.Output, "replaced 1 occurrence(s)") {
		t.Fatalf("edit result = %+v, want one replacement", res)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "one\nTWO\nthree\n" {
		t.Fatalf("content = %q, want the single replacement applied", data)
	}
}

func TestEditFileMissingMatch(t *testing.T) {
	fs := testFS(t, true)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "hello\n")

	res := invoke(t, fs.editFile(), `{"path":"a.txt","old_string":"absent","new_string":"x"}`)
	if !res.IsError || !strings.Contains(res.Output, "no match") {
		t.Fatalf("edit result = %+v, want a no-match error result", res)
	}
}

// An ambiguous edit must fail with the candidate lines, so the model can add
// context instead of guessing.
func TestEditFileRejectsAmbiguousMatch(t *testing.T) {
	fs := testFS(t, true)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "x\nx\n")

	res := invoke(t, fs.editFile(), `{"path":"a.txt","old_string":"x","new_string":"y"}`)
	if !res.IsError {
		t.Fatal("IsError = false, want true for an ambiguous match")
	}
	for _, want := range []string{"matches 2 times", "lines 1, 2", "replace_all"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("Output = %q, want it to contain %q", res.Output, want)
		}
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	fs := testFS(t, true)
	path := filepath.Join(fs.Root, "a.txt")
	writeTestFile(t, path, "x x x\n")

	res := invoke(t, fs.editFile(), `{"path":"a.txt","old_string":"x","new_string":"y","replace_all":true}`)
	if res.IsError || !strings.Contains(res.Output, "replaced 3 occurrence(s)") {
		t.Fatalf("edit result = %+v, want three replacements", res)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "y y y\n" {
		t.Fatalf("content = %q, want every match replaced", data)
	}
}

func TestEditFileRejectsEmptySearch(t *testing.T) {
	fs := testFS(t, true)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "hello\n")
	if err := invokeErr(t, fs.editFile(), `{"path":"a.txt","old_string":"","new_string":"x"}`); err == nil {
		t.Fatal("empty old_string = nil error, want one")
	}
}

// A tool path may not escape the workspace, lexically or through a symlink.
func TestResolveRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestFile(t, filepath.Join(base, "secret.txt"), "secret")

	fs, err := NewFS(root, false)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	for _, p := range []string{"../secret.txt", filepath.Join(base, "secret.txt")} {
		err := invokeErr(t, fs.readFile(), jsonArgs(p))
		if !strings.Contains(err.Error(), "outside the workspace") {
			t.Fatalf("read %q error = %v, want an escape refusal", p, err)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(base, "outside")
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "secret")

	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	fs, err := NewFS(root, false)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	err = invokeErr(t, fs.readFile(), jsonArgs("link/secret.txt"))
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("symlinked read error = %v, want an escape refusal", err)
	}
}

func TestListDirOrdersAndSizes(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "b.txt"), "12345")
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "")
	writeTestFile(t, filepath.Join(fs.Root, "sub", "x.txt"), "x")

	res := invoke(t, fs.listDir(), `{}`)
	for _, want := range []string{"3 entries", "d  sub/", "f  a.txt  (0 B)", "f  b.txt  (5 B)"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("Output = %q, want it to contain %q", res.Output, want)
		}
	}
	if strings.Index(res.Output, "d  sub/") > strings.Index(res.Output, "f  a.txt") {
		t.Fatalf("Output = %q, want directories before files", res.Output)
	}
}

func TestListDirLimit(t *testing.T) {
	fs := testFS(t, false)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		writeTestFile(t, filepath.Join(fs.Root, name), "")
	}

	res := invoke(t, fs.listDir(), `{"limit":2}`)
	if !strings.Contains(res.Output, "more entries") {
		t.Fatalf("Output = %q, want a truncation notice", res.Output)
	}
	if strings.Contains(res.Output, "f  e") {
		t.Fatalf("Output = %q, want the entry cap enforced", res.Output)
	}
}

func TestListDirRejectsFiles(t *testing.T) {
	fs := testFS(t, false)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "x")
	if err := invokeErr(t, fs.listDir(), jsonArgs("a.txt")); !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("list file error = %v, want a not-a-directory hint", err)
	}
}

// The whole point of the package: FS tools register into the registry and run
// through it, including argument decoding and output capping.
func TestFSToolsThroughRegistry(t *testing.T) {
	fs := testFS(t, true)
	writeTestFile(t, filepath.Join(fs.Root, "a.txt"), "hello\n")

	reg := NewRegistry()
	for _, tl := range fs.Tools() {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("Register(%s): %v", tl.Spec().Name, err)
		}
	}
	if got, want := reg.Names(), []string{"edit_file", "list_dir", "read_file", "write_file"}; !equalStrings(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}

	res := reg.Run(context.Background(), Call{
		ID:        "call_1",
		Name:      "read_file",
		Arguments: jsonArgs("a.txt"),
	})
	if res.IsError || !strings.Contains(res.Output, "hello") {
		t.Fatalf("registry Run = %+v, want the file content", res)
	}
}
