package tool

// tailBuffer keeps the last max bytes written to it and counts everything it
// dropped. Tool output becomes conversation state, so a command that prints
// gigabytes must not be able to grow the process: this bounds the memory at
// max while keeping the end, where build and test failures print their cause.
type tailBuffer struct {
	buf   []byte
	pos   int
	full  bool
	total int64
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = 1
	}
	return &tailBuffer{buf: make([]byte, max)}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.total += int64(n)
	for len(p) > 0 {
		copied := copy(t.buf[t.pos:], p)
		t.pos += copied
		p = p[copied:]
		if t.pos == len(t.buf) {
			t.pos = 0
			t.full = true
		}
	}
	return n, nil
}

// Bytes returns the retained window in write order.
func (t *tailBuffer) Bytes() []byte {
	if !t.full {
		return t.buf[:t.pos]
	}
	out := make([]byte, 0, len(t.buf))
	out = append(out, t.buf[t.pos:]...)
	out = append(out, t.buf[:t.pos]...)
	return out
}

// Text returns the retained window as a string, starting on a rune boundary when
// it dropped bytes from the front.
func (t *tailBuffer) Text() string {
	data := t.Bytes()
	if !t.full {
		return string(data)
	}
	for len(data) > 0 && !isRuneStart(data[0]) {
		data = data[1:]
	}
	return string(data)
}

// Retained is how many bytes the buffer still holds.
func (t *tailBuffer) Retained() int64 { return int64(len(t.Bytes())) }

// Dropped is how many bytes fell out of the front of the window.
func (t *tailBuffer) Dropped() int64 {
	if d := t.total - t.Retained(); d > 0 {
		return d
	}
	return 0
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
