package nsrunner

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// logBuffer is one workflow's output: an in-memory record that also lands on
// disk, and that late readers can follow from the beginning.
//
// It exists because the CLI no longer owns the process writing this output. A
// caller that attaches halfway through still needs everything printed so far --
// otherwise `nsctl env attach` shows a deploy already in progress with no
// indication of what it did -- so a subscriber is replayed the backlog before
// it starts receiving new lines.
type logBuffer struct {
	mu       sync.Mutex
	lines    []string
	closed   bool
	subs     map[chan string]struct{}
	file     *os.File
	filePath string
}

func newLogBuffer(path string) *logBuffer {
	lb := &logBuffer{subs: map[chan string]struct{}{}, filePath: path}
	// Best effort: a runner that cannot write its log still runs the deploy.
	if f, err := os.Create(path); err == nil {
		lb.file = f
	}
	return lb
}

// Write makes logBuffer an io.Writer, so internal/runner's existing Out/Err
// reporting is captured with no change to it at all.
func (lb *logBuffer) Write(p []byte) (int, error) {
	lb.append(string(p))
	return len(p), nil
}

func (lb *logBuffer) append(s string) {
	if s == "" {
		return
	}
	lb.mu.Lock()
	if lb.closed {
		lb.mu.Unlock()
		return
	}
	lb.lines = append(lb.lines, s)
	if lb.file != nil {
		_, _ = lb.file.WriteString(s)
	}
	for ch := range lb.subs {
		// Never block the deploy on a slow reader; a full channel drops the
		// line for that subscriber alone.
		select {
		case ch <- s:
		default:
		}
	}
	lb.mu.Unlock()
}

// Subscribe returns the backlog and a channel of subsequent lines. The channel
// is closed when the workflow finishes. A nil channel means it already had.
func (lb *logBuffer) Subscribe() ([]string, chan string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	backlog := append([]string(nil), lb.lines...)
	if lb.closed {
		return backlog, nil
	}
	ch := make(chan string, 256)
	lb.subs[ch] = struct{}{}
	return backlog, ch
}

// Unsubscribe releases a subscriber's channel.
func (lb *logBuffer) Unsubscribe(ch chan string) {
	if ch == nil {
		return
	}
	lb.mu.Lock()
	if _, ok := lb.subs[ch]; ok {
		delete(lb.subs, ch)
		close(ch)
	}
	lb.mu.Unlock()
}

// Close ends the stream for every subscriber.
func (lb *logBuffer) Close() {
	lb.mu.Lock()
	if lb.closed {
		lb.mu.Unlock()
		return
	}
	lb.closed = true
	for ch := range lb.subs {
		close(ch)
		delete(lb.subs, ch)
	}
	if lb.file != nil {
		_ = lb.file.Close()
		lb.file = nil
	}
	lb.mu.Unlock()
}

// replayLogBuffer reads back a log this process did not write, for a workflow
// recovered from disk. It deliberately does not open the file for writing:
// newLogBuffer truncates, which would destroy the very record being recovered.
func replayLogBuffer(path string) *logBuffer {
	lb := &logBuffer{subs: map[chan string]struct{}{}, filePath: path, closed: true}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		lb.lines = []string{string(data)}
	}
	return lb
}

// prefixWriter attributes every line a node writes to that node.
//
// Four nodes deploying at once share one logBuffer, and that buffer is exactly
// what `nsctl env start` and `nsctl env attach` put on the user's screen, so
// without attribution a parallel deploy reads as four narrations shuffled
// together.
//
// The alternative -- buffering each node and flushing the block when it
// finishes -- was rejected because the container's own output is not streamed
// at all: Docker.Run collects stdout and stderr and hands them back at the end,
// so these progress lines are the only live signal there is. Withholding them
// until a node completed would leave a multi-minute deploy silent, which is a
// worse trade than a prefix. Prefixing is skipped entirely when only one node
// can be in flight, where there is nothing to disambiguate.
//
// One writer belongs to one node's goroutine and is not shared, so it carries
// no lock of its own; the logBuffer underneath has one.
type prefixWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func newPrefixWriter(w io.Writer, instance string) *prefixWriter {
	return &prefixWriter{w: w, prefix: "[" + instance + "] "}
}

// Write buffers until it has whole lines, then emits them in one call -- so the
// shared buffer records a node's output as one entry rather than as fragments
// another node can land between.
func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	cut := bytes.LastIndexByte(p.buf, '\n')
	if cut < 0 {
		return len(b), nil
	}
	p.emit(p.buf[:cut+1])
	p.buf = append(p.buf[:0], p.buf[cut+1:]...)
	return len(b), nil
}

// Close flushes a trailing line that never got its newline.
func (p *prefixWriter) Close() {
	if len(p.buf) == 0 {
		return
	}
	p.emit(append(p.buf, '\n'))
	p.buf = nil
}

func (p *prefixWriter) emit(chunk []byte) {
	var out bytes.Buffer
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		line := chunk[:i+1]
		chunk = chunk[i+1:]
		// A blank line is a separator, and attributing it only adds noise.
		if len(line) > 1 {
			out.WriteString(p.prefix)
		}
		out.Write(line)
	}
	_, _ = p.w.Write(out.Bytes())
}
