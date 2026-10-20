// Package jsonl persists transcript events as one JSON object per line.
//
// A Recorder is a single-writer append-only log: callers must not open the same
// path twice in one process (v1 has no file locking). Every successful Append is
// flushed with fsync before it returns.
package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"winky/internal/transcript"
)

const (
	subscriberBuffer = 64
	readBufferSize   = 1 << 20
)

// Recorder is an append-only JSONL transcript recorder.
type Recorder struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	nextSeq     int64
	closed      bool
	subscribers []chan transcript.Event
}

// Open opens or creates path, validates all complete records, repairs a
// partially written final line, and continues the sequence after the last valid
// event.
func Open(path string) (*Recorder, error) {
	if path == "" {
		return nil, errors.New("transcript/jsonl: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("transcript/jsonl: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("transcript/jsonl: open %s: %w", path, err)
	}
	r := &Recorder{path: path, file: file, nextSeq: 1}
	if err := r.scan(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return r, nil
}

// scan validates existing records and computes the next sequence number. A
// final line without a newline is treated as a torn write and truncated.
func (r *Recorder) scan() error {
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("transcript/jsonl: seek %s: %w", r.path, err)
	}

	reader := bufio.NewReaderSize(r.file, readBufferSize)
	var (
		offset int64
		maxSeq int64
		lineNo int
	)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			lineNo++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				ev, decErr := transcript.Decode(trimmed)
				if decErr != nil {
					return fmt.Errorf("%s:%d: %w", r.path, lineNo, decErr)
				}
				if ev.Seq() > maxSeq {
					maxSeq = ev.Seq()
				}
			}
		} else if len(line) > 0 && err == io.EOF {
			if truncErr := r.file.Truncate(offset); truncErr != nil {
				return fmt.Errorf("transcript/jsonl: truncate torn line in %s: %w", r.path, truncErr)
			}
			break
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("transcript/jsonl: read %s: %w", r.path, err)
		}
	}

	r.nextSeq = maxSeq + 1
	if r.nextSeq < 1 {
		r.nextSeq = 1
	}
	return nil
}

// Append assigns the next sequence number, writes one JSONL record, fsyncs it,
// and notifies subscribers. A failed write is truncated back to the previous
// end offset.
func (r *Recorder) Append(ev transcript.Event) error {
	if ev == nil {
		return errors.New("transcript/jsonl: cannot append a nil event")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("transcript/jsonl: recorder is closed")
	}

	seq := r.nextSeq
	sequenced := transcript.WithSeq(ev, seq)
	line, err := transcript.Encode(sequenced)
	if err != nil {
		return fmt.Errorf("transcript/jsonl: append seq %d: %w", seq, err)
	}
	line = append(line, '\n')

	offset, err := r.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("transcript/jsonl: seek end of %s: %w", r.path, err)
	}

	n, writeErr := r.file.Write(line)
	if writeErr != nil || n != len(line) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return errors.Join(
			fmt.Errorf("transcript/jsonl: append seq %d: %w", seq, writeErr),
			r.file.Truncate(offset),
		)
	}
	if err := r.file.Sync(); err != nil {
		// The bytes reached the file but their durability is unknown. Consume
		// the sequence number so a retry cannot reuse it.
		r.nextSeq++
		return fmt.Errorf("transcript/jsonl: sync seq %d: %w", seq, err)
	}

	r.nextSeq++
	r.notifyLocked(sequenced)
	return nil
}

// ReadAll returns every complete record currently in the file. A trailing
// partial line (for example while another goroutine is appending) is ignored.
func (r *Recorder) ReadAll() ([]transcript.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	file, err := os.Open(r.path)
	if err != nil {
		return nil, fmt.Errorf("transcript/jsonl: open %s for read: %w", r.path, err)
	}
	defer file.Close()

	return readAll(file, r.path)
}

// Subscribe returns a live event stream. Slow subscribers do not block Append:
// an event is dropped when a subscriber's buffer is full. The channel is closed
// by Close.
func (r *Recorder) Subscribe() <-chan transcript.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := make(chan transcript.Event, subscriberBuffer)
	if r.closed {
		close(ch)
		return ch
	}
	r.subscribers = append(r.subscribers, ch)
	return ch
}

// Close flushes, closes the file, and closes all subscriber channels.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true

	syncErr := r.file.Sync()
	closeErr := r.file.Close()
	for _, ch := range r.subscribers {
		close(ch)
	}
	r.subscribers = nil
	return errors.Join(syncErr, closeErr)
}

// Path returns the transcript file path.
func (r *Recorder) Path() string { return r.path }

func (r *Recorder) notifyLocked(ev transcript.Event) {
	for _, ch := range r.subscribers {
		select {
		case ch <- ev:
		default:
			// A slow consumer must not stall durable writes.
		}
	}
}

func readAll(file *os.File, path string) ([]transcript.Event, error) {
	reader := bufio.NewReaderSize(file, readBufferSize)
	var (
		events []transcript.Event
		lineNo int
	)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			lineNo++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				ev, decErr := transcript.Decode(trimmed)
				if decErr != nil {
					return nil, fmt.Errorf("%s:%d: %w", path, lineNo, decErr)
				}
				events = append(events, ev)
			}
		} else if len(line) > 0 && err == io.EOF {
			// Ignore a torn trailing line; Open repairs it on the next start.
			break
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("transcript/jsonl: read %s: %w", path, err)
		}
	}
	return events, nil
}
