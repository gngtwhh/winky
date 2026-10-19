package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"winky/internal/llm"
)

// Limits for the filesystem tools; tool output is re-sent to the model every
// later request, so each call must stay small.
const (
	defaultReadLines   = 2000
	maxReadLines       = 4000
	maxReadBytes       = 8 << 20 // 8 MiB
	maxLineBytes       = 2000
	defaultListEntries = 500
	maxListEntries     = 5000
)

// FS is the filesystem tool set rooted at one directory.
//
// Paths are resolved against Root and symlink-resolved; whether an operation is
// allowed (including paths outside Root) is decided by Policy at call time.
type FS struct {
	Root   string // absolute, symlink-resolved
	Policy *Policy
}

// NewFS builds a filesystem tool set rooted at root. The directory must exist.
func NewFS(root string, p *Policy) (*FS, error) {
	if p == nil {
		p = NewPolicy(WorkspaceWrite)
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("tool: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("tool: workspace %s: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("tool: workspace %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tool: workspace %s is not a directory", abs)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("tool: workspace %s: %w", abs, err)
	}
	return &FS{Root: real, Policy: p}, nil
}

// Tools returns the filesystem tools. Mutating tools are sequenced so two
// concurrent edits to the same path cannot race.
func (fs *FS) Tools() []Tool {
	return []Tool{
		fs.readFile(),
		fs.listDir(),
		sequenced(fs.writeFile()),
		sequenced(fs.editFile()),
		sequenced(fs.delete()),
		fs.mkdir(),
	}
}

// guard asks the policy to authorize one operation.
func (fs *FS) guard(ctx context.Context, name string, a Access, target string, inside bool) error {
	return fs.Policy.guard(ctx, Request{Tool: name, Access: a, Target: target, Inside: inside})
}

// resolve maps a path argument to an absolute path. inside reports whether the
// resolved path is within Root; escapes are not rejected here, only reported so
// the caller can ask the policy.
func (fs *FS) resolve(p string) (abs string, inside bool, err error) {
	if strings.TrimSpace(p) == "" {
		return "", false, errors.New("path is required")
	}
	abs = p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(fs.Root, abs)
	}
	abs = filepath.Clean(abs)

	real, err := realPath(abs)
	if err != nil {
		return "", false, err
	}
	return real, within(fs.Root, real), nil
}

// display renders p relative to Root when it is inside, otherwise verbatim.
func (fs *FS) display(p string) string {
	if within(fs.Root, p) {
		if rel, err := filepath.Rel(fs.Root, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// within reports whether p is root or a descendant of it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// realPath resolves symlinks in the deepest existing ancestor of p and re-joins
// the part that does not, so a write target whose parent is a symlink is judged
// by where the write would actually land.
func realPath(p string) (string, error) {
	rest := ""
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return real, nil
			}
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

var (
	readFileSpec = llm.ToolSpec{
		Name: "read_file",
		Description: "Read a text file. Returns numbered lines; use it before " +
			"edit_file so the replacement matches exactly.",
		Parameters: object(
			map[string]any{
				"path":   stringProp("File path relative to the workspace root."),
				"offset": intProp("1-based line to start from (default 1).", 1, 0),
				"limit":  intProp("Maximum lines to return (default 2000, max 4000).", 1, maxReadLines),
			},
			"path",
		),
	}

	writeFileSpec = llm.ToolSpec{
		Name: "write_file",
		Description: "Create or overwrite a file. Parent directories are created. " +
			"Prefer edit_file for small changes to an existing file.",
		Parameters: object(
			map[string]any{
				"path":    stringProp("File path relative to the workspace root."),
				"content": stringProp("Complete new file content."),
			},
			"path", "content",
		),
	}

	editFileSpec = llm.ToolSpec{
		Name: "edit_file",
		Description: "Replace an exact substring in a file. old_string must match " +
			"exactly once unless replace_all is true; include enough context to be unique.",
		Parameters: object(
			map[string]any{
				"path":        stringProp("File path relative to the workspace root."),
				"old_string":  stringProp("Exact text to replace, including indentation."),
				"new_string":  stringProp("Replacement text."),
				"replace_all": boolProp("Replace every match instead of requiring a unique one."),
			},
			"path", "old_string", "new_string",
		),
	}

	listDirSpec = llm.ToolSpec{
		Name:        "list_dir",
		Description: "List one directory: subdirectories first, then files with sizes.",
		Parameters: object(
			map[string]any{
				"path":  stringProp(`Directory relative to the workspace root (default ".").`),
				"limit": intProp("Maximum entries to return (default 500, max 5000).", 1, maxListEntries),
			},
		),
	}

	deleteSpec = llm.ToolSpec{
		Name:        "delete",
		Description: "Delete a file or directory. Non-empty directories need recursive=true.",
		Parameters: object(
			map[string]any{
				"path":      stringProp("Path relative to the workspace root."),
				"recursive": boolProp("Also delete a directory's contents."),
			},
			"path",
		),
	}

	mkdirSpec = llm.ToolSpec{
		Name:        "mkdir",
		Description: "Create a directory (parents are created too).",
		Parameters: object(
			map[string]any{
				"path": stringProp("Directory path relative to the workspace root."),
			},
			"path",
		),
	}
)

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (fs *FS) readFile() Tool {
	return Func[readFileArgs](readFileSpec, func(ctx context.Context, a readFileArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		path, inside, err := fs.resolve(a.Path)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "read_file", AccessRead, fs.display(path), inside); err != nil {
			return Result{}, err
		}

		info, err := os.Stat(path)
		if err != nil {
			return Result{}, err
		}
		if info.IsDir() {
			return Result{}, fmt.Errorf("%s is a directory; use list_dir", fs.display(path))
		}
		if info.Size() > maxReadBytes {
			return Result{}, fmt.Errorf(
				"%s is %s, over the %s text read limit", fs.display(path),
				humanSize(info.Size()), humanSize(maxReadBytes),
			)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
		if isBinary(data) {
			return Result{}, fmt.Errorf("%s looks binary; refusing to read it as text", fs.display(path))
		}

		lines := splitLines(string(data))
		if len(lines) == 0 {
			return Result{Output: fmt.Sprintf("%s: empty file", fs.display(path))}, nil
		}

		offset := max(a.Offset, 1)
		limit := a.Limit
		if limit <= 0 {
			limit = defaultReadLines
		}
		limit = min(limit, maxReadLines)

		start := min(offset-1, len(lines))
		end := min(start+limit, len(lines))
		if start == end {
			return Result{Output: fmt.Sprintf(
				"%s: no lines in range (file has %d lines)", fs.display(path), len(lines),
			)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "%s: lines %d-%d of %d\n", fs.display(path), start+1, end, len(lines))
		for i := start; i < end; i++ {
			fmt.Fprintf(&b, "%6d\t%s\n", i+1, clipLine(lines[i]))
		}
		if end < len(lines) {
			fmt.Fprintf(&b, "[%d more lines; continue with offset=%d]\n", len(lines)-end, end+1)
		}
		return Result{Output: b.String()}, nil
	})
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (fs *FS) writeFile() Tool {
	return Func[writeFileArgs](writeFileSpec, func(ctx context.Context, a writeFileArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		path, inside, err := fs.resolve(a.Path)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "write_file", AccessWrite, fs.display(path), inside); err != nil {
			return Result{}, err
		}

		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return Result{}, fmt.Errorf("%s is a directory", fs.display(path))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{}, err
		}
		if err := writeFileAtomic(path, []byte(a.Content)); err != nil {
			return Result{}, err
		}
		return Result{Output: fmt.Sprintf("wrote %d bytes to %s", len(a.Content), fs.display(path))}, nil
	})
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (fs *FS) editFile() Tool {
	return Func[editFileArgs](editFileSpec, func(ctx context.Context, a editFileArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if a.OldString == "" {
			return Result{}, errors.New("old_string must not be empty")
		}
		if a.OldString == a.NewString {
			return Result{}, errors.New("old_string and new_string are identical")
		}
		path, inside, err := fs.resolve(a.Path)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "edit_file", AccessWrite, fs.display(path), inside); err != nil {
			return Result{}, err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
		if isBinary(data) {
			return Result{}, fmt.Errorf("%s looks binary; refusing to edit it as text", fs.display(path))
		}

		content := string(data)
		count := strings.Count(content, a.OldString)
		switch {
		case count == 0:
			return Result{
				Output:  fmt.Sprintf("no match for old_string in %s", fs.display(path)),
				IsError: true,
			}, nil
		case count > 1 && !a.ReplaceAll:
			return Result{
				Output: fmt.Sprintf(
					"old_string matches %d times in %s (first matches on lines %s); "+
						"add more surrounding context or set replace_all",
					count, fs.display(path), joinInts(matchLines(content, a.OldString, 5)),
				),
				IsError: true,
			}, nil
		}

		updated := strings.Replace(content, a.OldString, a.NewString, 1)
		if a.ReplaceAll {
			updated = strings.ReplaceAll(content, a.OldString, a.NewString)
		}
		if err := writeFileAtomic(path, []byte(updated)); err != nil {
			return Result{}, err
		}

		replaced := 1
		if a.ReplaceAll {
			replaced = count
		}
		return Result{Output: fmt.Sprintf("replaced %d occurrence(s) in %s", replaced, fs.display(path))}, nil
	})
}

type listDirArgs struct {
	Path  string `json:"path"`
	Limit int    `json:"limit"`
}

func (fs *FS) listDir() Tool {
	return Func[listDirArgs](listDirSpec, func(ctx context.Context, a listDirArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		arg := strings.TrimSpace(a.Path)
		if arg == "" {
			arg = "."
		}
		path, inside, err := fs.resolve(arg)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "list_dir", AccessRead, fs.display(path), inside); err != nil {
			return Result{}, err
		}

		info, err := os.Stat(path)
		if err != nil {
			return Result{}, err
		}
		if !info.IsDir() {
			return Result{}, fmt.Errorf("%s is not a directory", fs.display(path))
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return Result{}, err
		}
		sortEntries(entries)

		limit := a.Limit
		if limit <= 0 {
			limit = defaultListEntries
		}
		limit = min(limit, maxListEntries)

		var b strings.Builder
		fmt.Fprintf(&b, "%s/: %d entries\n", fs.display(path), len(entries))
		for i, e := range entries {
			if i >= limit {
				fmt.Fprintf(&b, "[%d more entries]\n", len(entries)-limit)
				break
			}
			switch {
			case e.Type()&os.ModeSymlink != 0:
				target, err := os.Readlink(filepath.Join(path, e.Name()))
				if err != nil {
					target = "?"
				}
				fmt.Fprintf(&b, "l  %s -> %s\n", e.Name(), target)
			case e.IsDir():
				fmt.Fprintf(&b, "d  %s/\n", e.Name())
			default:
				size := ""
				if fi, err := e.Info(); err == nil {
					size = "  (" + humanSize(fi.Size()) + ")"
				}
				fmt.Fprintf(&b, "f  %s%s\n", e.Name(), size)
			}
		}
		return Result{Output: b.String()}, nil
	})
}

type deleteArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func (fs *FS) delete() Tool {
	return Func[deleteArgs](deleteSpec, func(ctx context.Context, a deleteArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		path, inside, err := fs.resolve(a.Path)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "delete", AccessWrite, fs.display(path), inside); err != nil {
			return Result{}, err
		}

		info, err := os.Lstat(path)
		if err != nil {
			return Result{}, err
		}
		if info.IsDir() && a.Recursive {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			return Result{}, err
		}
		return Result{Output: "deleted " + fs.display(path)}, nil
	})
}

type mkdirArgs struct {
	Path string `json:"path"`
}

func (fs *FS) mkdir() Tool {
	return Func[mkdirArgs](mkdirSpec, func(ctx context.Context, a mkdirArgs) (Result, error) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		path, inside, err := fs.resolve(a.Path)
		if err != nil {
			return Result{}, err
		}
		if err := fs.guard(ctx, "mkdir", AccessWrite, fs.display(path), inside); err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return Result{}, err
		}
		return Result{Output: "created directory " + fs.display(path)}, nil
	})
}

// sortEntries orders directory entries: directories first, then files, each group
// case-insensitively by name.
func sortEntries(entries []os.DirEntry) {
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
}

// writeFileAtomic writes a temp file then renames it over the target, preserving
// the target's permission bits.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".winky-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	name = ""
	return nil
}

// splitLines splits text into lines, dropping the trailing empty element a final
// newline produces and stripping a carriage return before each newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// clipLine truncates one over-long line, rune-safely, and marks the cut.
func clipLine(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	return cutAtRune(s, maxLineBytes) + "…"
}

// isBinary reports whether data looks binary: a NUL byte in the first 8 KiB.
func isBinary(data []byte) bool {
	const sniff = 8000
	if len(data) > sniff {
		data = data[:sniff]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// humanSize formats a byte count for a one-line message.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	suffix := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	for _, s := range suffix {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, s)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// matchLines returns the 1-based line numbers of at most max occurrences of sub.
func matchLines(content, sub string, max int) []int {
	var out []int
	pos, line := 0, 1
	for len(out) < max {
		j := strings.Index(content[pos:], sub)
		if j < 0 {
			break
		}
		abs := pos + j
		line += strings.Count(content[pos:abs], "\n")
		out = append(out, line)
		pos = abs + len(sub)
	}
	return out
}

// joinInts renders line numbers as "3, 11, 42".
func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ", ")
}
