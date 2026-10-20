// Package session owns the on-disk identity and directory of one conversation.
//
// v1 only creates new sessions. Loading an existing session is deliberately
// deferred until the transcript can replace the in-memory history.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const metaVersion = 1

// Options describes the conversation a new session belongs to.
type Options struct {
	Cwd      string
	Provider string
	Model    string
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
	return s, nil
}

// MetaPath returns the session metadata file path.
func (s *Session) MetaPath() string { return s.metaPath }

// TranscriptPath returns the JSONL transcript file path.
func (s *Session) TranscriptPath() string { return s.transcriptPath }

// Close updates meta.json's updated_at. It is safe to call more than once.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	s.meta.UpdatedAt = time.Now().UTC()
	if err := writeMeta(s.metaPath, s.meta); err != nil {
		return err
	}
	return nil
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
