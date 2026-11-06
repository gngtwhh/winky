// Package session owns the on-disk identity and directory of one conversation:
// creating a new session, and listing or reopening the sessions that were
// started in a given working directory.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"winky/internal/transcript"
)

const (
	metaVersion = 1
	titleRunes  = 72

	// lockName is the per-session lock file. It stays on disk for the lifetime
	// of the session directory: unlinking it would let two processes lock
	// different inodes and defeat the mutual exclusion.
	lockName = "session.lock"
)

// ErrInUse reports a session that is currently open in another process.
var ErrInUse = errors.New("session is in use by another process")

// Options describes the conversation a new session belongs to.
type Options struct {
	Cwd      string
	Provider string
	Model    string
}

// Summary describes one stored session in a listing.
type Summary struct {
	Meta
	Title string
	InUse bool
}

// Meta is the session sidecar written to meta.json.
type Meta struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Cwd       string    `json:"cwd,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
}

// Session is one conversation directory.
type Session struct {
	ID  string
	Dir string

	metaPath       string
	transcriptPath string
	meta           Meta

	mu     sync.Mutex
	lock   *flock.Flock
	closed bool
}

// New creates a fresh session directory under agentDir/sessions and writes its
// meta.json.
func New(agentDir string, opts Options) (*Session, error) {
	if agentDir == "" {
		return nil, errors.New("session: empty agent directory")
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	sessionsDir := filepath.Join(agentDir, "sessions")
	dir := filepath.Join(sessionsDir, id)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create sessions directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: create %s: %w", dir, err)
	}

	now := time.Now().UTC()
	meta := Meta{
		Version:   metaVersion,
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Cwd:       opts.Cwd,
		Provider:  opts.Provider,
		Model:     opts.Model,
	}
	s := &Session{
		ID:             id,
		Dir:            dir,
		metaPath:       filepath.Join(dir, "meta.json"),
		transcriptPath: filepath.Join(dir, "transcript.jsonl"),
		meta:           meta,
	}
	if err := writeMeta(s.metaPath, meta); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := s.lockSession(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return s, nil
}

// Open reopens the session with the given id under agentDir/sessions. The
// returned session continues where the transcript ended, because the recorder
// resumes the sequence numbers it finds on disk. A session already open in
// another process fails with ErrInUse.
func Open(agentDir, id string) (*Session, error) {
	dir, err := sessionDir(agentDir, id)
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:             id,
		Dir:            dir,
		metaPath:       filepath.Join(dir, "meta.json"),
		transcriptPath: filepath.Join(dir, "transcript.jsonl"),
	}
	if err := s.lockSession(); err != nil {
		return nil, err
	}

	meta, err := readMeta(s.metaPath)
	if err != nil {
		_ = s.releaseLock()
		return nil, err
	}
	meta.ID = id
	s.meta = meta
	return s, nil
}

// lockSession takes the exclusive lock of one session directory.
func (s *Session) lockSession() error {
	l := flock.New(filepath.Join(s.Dir, lockName))
	ok, err := l.TryLock()
	if err != nil {
		_ = l.Close()
		if errors.Is(err, errors.ErrUnsupported) {
			return fmt.Errorf("session %s: file locking is not supported on this filesystem: %w", s.ID, err)
		}
		return fmt.Errorf("session %s: lock: %w", s.ID, err)
	}
	if !ok {
		_ = l.Close()
		return fmt.Errorf("session %s: %w", s.ID, ErrInUse)
	}
	s.lock = l
	return nil
}

// releaseLock drops the lock but keeps the lock file, so another process cannot
// lock a fresh inode while a third one still holds the old one.
func (s *Session) releaseLock() error {
	if s.lock == nil {
		return nil
	}
	l := s.lock
	s.lock = nil
	return errors.Join(l.Unlock(), l.Close())
}

// inUse probes the session lock without keeping it.
func inUse(dir string) bool {
	l := flock.New(filepath.Join(dir, lockName))
	ok, err := l.TryLock()
	if err != nil {
		_ = l.Close()
		return false
	}
	if !ok {
		_ = l.Close()
		return true
	}
	_ = errors.Join(l.Unlock(), l.Close())
	return false
}

// Delete removes a stored session directory and its transcript. A session open
// in another process is refused with ErrInUse.
func Delete(agentDir, id string) error {
	dir, err := sessionDir(agentDir, id)
	if err != nil {
		return err
	}

	s := &Session{
		ID:             id,
		Dir:            dir,
		metaPath:       filepath.Join(dir, "meta.json"),
		transcriptPath: filepath.Join(dir, "transcript.jsonl"),
	}
	if err := s.lockSession(); err != nil {
		return err
	}

	// Files go first: the lock file cannot be removed while it is held, and a
	// racing opener fails on the missing metadata instead of a half session.
	rmErr := errors.Join(removeFile(s.transcriptPath), removeFile(s.metaPath))
	unlockErr := s.releaseLock()
	dirErr := os.RemoveAll(dir)
	return errors.Join(rmErr, unlockErr, dirErr)
}

// removeFile deletes path, tolerating a missing file.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// readMeta loads and validates a session's meta.json.
func readMeta(metaPath string) (Meta, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return Meta{}, fmt.Errorf("session: read %s: %w", metaPath, err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, fmt.Errorf("session: decode %s: %w", metaPath, err)
	}
	if meta.Version != metaVersion {
		return Meta{}, fmt.Errorf("session: unsupported meta version %d in %s", meta.Version, metaPath)
	}
	return meta, nil
}

// sessionDir resolves and validates the directory of one stored session.
func sessionDir(agentDir, id string) (string, error) {
	if agentDir == "" {
		return "", errors.New("session: empty agent directory")
	}
	if id == "" || id == "." || id == ".." || id != filepath.Base(id) {
		return "", fmt.Errorf("session: invalid id %q", id)
	}
	return filepath.Join(agentDir, "sessions", id), nil
}

// List returns the sessions created in cwd, most recently updated first. A
// session whose meta.json is unreadable is skipped instead of failing the whole
// listing.
func List(agentDir, cwd string) ([]Summary, error) {
	if agentDir == "" {
		return nil, errors.New("session: empty agent directory")
	}

	sessionsDir := filepath.Join(agentDir, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: list %s: %w", sessionsDir, err)
	}

	out := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsDir, entry.Name())
		meta, err := readMeta(filepath.Join(dir, "meta.json"))
		if err != nil || !samePath(meta.Cwd, cwd) {
			continue
		}
		title, hasContent, scanErr := scanContent(filepath.Join(dir, "transcript.jsonl"))
		if scanErr == nil && !hasContent {
			// A killed process leaves a session directory behind that never
			// recorded a conversation: hide it instead of offering it up.
			continue
		}
		meta.ID = entry.Name()
		out = append(out, Summary{Meta: meta, Title: title, InUse: inUse(dir)})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// samePath reports whether two working directories denote the same location;
// Windows paths are compared case-insensitively.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	return runtime.GOOS == "windows" && strings.EqualFold(ca, cb)
}

// scanContent titles the session and reports whether it has content.
// Failed/aborted turns count, as do unreadable records (never delete those).
func scanContent(path string) (title string, hasContent bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		ev, decodeErr := transcript.Decode(line)
		if decodeErr != nil {
			return "", true, nil
		}
		switch e := ev.(type) {
		case transcript.UserMessage:
			title := strings.Join(strings.Fields(e.Content), " ")
			if r := []rune(title); len(r) > titleRunes {
				title = string(r[:titleRunes]) + "…"
			}
			return title, true, nil
		case transcript.AssistantOutputMessage, transcript.ToolResult,
			transcript.CompactionSummary, transcript.TurnFailed, transcript.TurnAborted:
			hasContent = true
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", hasContent, nil
}

// MetaPath returns the session metadata file path.
func (s *Session) MetaPath() string { return s.metaPath }

// TranscriptPath returns the JSONL transcript file path.
func (s *Session) TranscriptPath() string { return s.transcriptPath }

// Close releases the session lock and updates meta.json's updated_at. A session
// that never recorded a conversation is removed instead, so quitting right
// after launch leaves no empty history behind. It is safe to call more than
// once.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	// Only an inspected-and-empty transcript is removed; an unreadable one is
	// kept, because deleting data is worse than leaving an empty session.
	if _, hasContent, err := scanContent(s.transcriptPath); err == nil && !hasContent {
		unlockErr := s.releaseLock()
		return errors.Join(unlockErr, os.RemoveAll(s.Dir))
	}

	s.meta.UpdatedAt = time.Now().UTC()
	metaErr := writeMeta(s.metaPath, s.meta)
	return errors.Join(metaErr, s.releaseLock())
}

func newID() (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(suffix[:]), nil
}

func writeMeta(path string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("session: encode meta: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("session: write meta: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: replace meta: %w", err)
	}
	return nil
}
