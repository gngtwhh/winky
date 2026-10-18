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

// Limits for the filesystem tools. They exist to keep one call from flooding the
// conversation: everything a tool returns is re-sent to the model on every later
// request.
const (
	// defaultReadLines is how many lines read_file returns without a limit.
	defaultReadLines = 2000
	// maxReadLines caps an explicit limit, so a model cannot ask for a dump.
	maxReadLines = 4000
	// maxReadBytes refuses files too large to hold in memory for a text read.
	maxReadBytes = 8 << 20 // 8 MiB
	// maxLineBytes clips a single pathological line (minified JS, base64 blob).
	maxLineBytes = 2000
	// defaultListEntries is how many directory entries list_dir returns.
	defaultListEntries = 500
	// maxListEntries caps an explicit list limit.
	maxListEntries = 5000
)

// errReadOnly is returned when a write tool is invoked on a read-only workspace.
// The write tools are not registered in read-only mode; this is defense in depth.
var errReadOnly = errors.New("workspace is read-only: writing is disabled")

// FS is the filesystem tool set rooted at one directory.
//
// Every path argument is resolved against Root and rejected if it escapes, both
// lexically and after resolving symlinks, so a model cannot read or write
// outside the workspace. Writable=false keeps only the read-only tools.
type FS struct {
	// Root is the absolute, symlink-resolved workspace directory.
	Root string
	// Writable allows write_file and edit_file.
	Writable bool
}

// NewFS builds a filesystem tool set rooted at root. The directory must exist.
func NewFS(root string, writable bool) (*FS, error) {
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
	// Resolve the root once so containment checks compare real paths: comparing
	// a resolved child against an unresolved root would reject valid paths.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("tool: workspace %s: %w", abs, err)
	}
	return &FS{Root: real, Writable: writable}, nil
}

// Tools returns the tool set. Read-only workspaces expose read_file and
// list_dir only; a model that cannot see write_file cannot waste a turn trying
// to call it.
func (fs *FS) Tools() []Tool {
	out := []Tool{fs.readFile(), fs.listDir()}
	if fs.Writable {
		out = append(out, fs.writeFile(), fs.editFile())
	}
	return out
}

// requireWritable guards the mutating tools.
func (fs *FS) requireWritable() error {
	if !fs.Writable {
		return errReadOnly
	}
	return nil
}

// resolve maps a tool path argument to an absolute path inside Root.
//
// Containment is checked twice: lexically on the cleaned path, then again after
// resolving symlinks in the deepest existing ancestor. The second check is what
// stops a symlinked directory from redirecting a write outside the workspace,
// including for a path that does not exist yet.
func (fs *FS) resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(fs.Root, abs)
	}
	abs = filepath.Clean(abs)
	if !within(fs.Root, abs) {
		return "", fmt.Errorf("path %q is outside the workspace", p)
	}

	real, err := realPath(abs)
	if err != nil {
		return "", err
	}
	if !within(fs.Root, real) {
		return "", fmt.Errorf("path %q resolves outside the workspace", p)
	}
	return real, nil
}

// display renders p relative to Root, so tool output carries workspace-relative
// paths instead of absolute host paths.
func (fs *FS) display(p string) string {
	rel, err := filepath.Rel(fs.Root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
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

// realPath resolves symlinks in the deepest ancestor of p that exists and
// re-joins the part that does not, so a write target whose parent is a symlink is
// judged by where the write would actually land.
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
		Description: "Read a text file from the workspace. Returns numbered " +
			"lines; use it before edit_file so the replacement matches exactly.",
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
		Description: "Create or overwrite a file with the given content. Parent " +
			"directories are created. Prefer edit_file for small changes to an " +
			"existing file: rewriting a whole file costs tokens and loses the rest " +
			"of its content.",
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
		Description: "Replace an exact substring in an existing file. old_string " +
			"must match exactly once unless replace_all is true; include enough " +
			"surrounding context to make it unique.",
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
		Name: "list_dir",
		Description: "List one directory of the workspace: subdirectories first, " +
			"then files with their sizes. Use it to discover paths before reading.",
		Parameters: object(
			map[string]any{
				"path":  stringProp(`Directory relative to the workspace root (default ".").`),
				"limit": intProp("Maximum entries to return (default 500, max 5000).", 1, maxListEntries),
			},
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

		path, err := fs.resolve(a.Path)
		if err != nil {
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
				"%s: no lines in range (file has %d lines)",
				fs.display(path), len(lines),
			)}, nil
		}

		var b strings.Builder
		fmt.Fprintf(&b, "%s: lines %d-%d of %d\n", fs.display(path), start+1, end, len(lines))
		for i := start; i < end; i++ {
			fmt.Fprintf(&b, "%6d\t%s\n", i+1, clipLine(lines[i]))
		}
		if end < len(lines) {
			fmt.Fprintf(
				&b, "[%d more lines; continue with offset=%d]\n",
				len(lines)-end, end+1,
			)
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
		if err := fs.requireWritable(); err != nil {
			return Result{}, err
		}

		path, err := fs.resolve(a.Path)
		if err != nil {
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
		return Result{Output: fmt.Sprintf(
			"wrote %d bytes to %s", len(a.Content), fs.display(path),
		)}, nil
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
		if err := fs.requireWritable(); err != nil {
			return Result{}, err
		}
		if a.OldString == "" {
			return Result{}, errors.New("old_string must not be empty")
		}
		if a.OldString == a.NewString {
			return Result{}, errors.New("old_string and new_string are identical")
		}

		path, err := fs.resolve(a.Path)
		if err != nil {
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
		return Result{Output: fmt.Sprintf(
			"replaced %d occurrence(s) in %s", replaced, fs.display(path),
		)}, nil
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
		path, err := fs.resolve(arg)
		if err != nil {
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

// sortEntries orders directory entries the way a human reads them: directories
// first, then files, each group case-insensitively by name.
func sortEntries(entries []os.DirEntry) {
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
}

// writeFileAtomic replaces path in one step: write a temp file in the same
// directory, then rename over the target. A crash mid-write cannot leave a
// half-written source file, and the target's permission bits are preserved.
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
	name = "" // renamed: nothing left to clean up
	return nil
}

// splitLines splits text into lines and drops the trailing empty element a final
// newline produces, so a 3-line file reports 3 lines rather than 4. A carriage
// return before the newline is stripped so CRLF files read cleanly.
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

// isBinary reports whether data looks like a binary blob. A NUL byte in the
// first 8 KiB is the heuristic used by git and most editors.
func isBinary(data []byte) bool {
	const sniff = 8000
	if len(data) > sniff {
		data = data[:sniff]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// humanSize formats a byte count for a one-line tool message.
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

// matchLines returns the 1-based line numbers of at most max occurrences of sub
// in content, so an ambiguous edit can tell the model where the candidates are.
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
