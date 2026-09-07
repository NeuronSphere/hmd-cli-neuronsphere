package nsrunner

import (
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
