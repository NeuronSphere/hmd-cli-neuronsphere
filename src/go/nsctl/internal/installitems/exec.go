package installitems

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// userinfoRe is the credential part of a URL: scheme://user[:pass]@.
var userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)

// Redact removes URL userinfo. A package index URL commonly embeds a token,
// and failure output is what gets pasted into a ticket, so nothing this
// package prints -- its own messages or a child's -- carries one (SPEC006).
// The host stays: it is what tells the reader which index refused.
func Redact(s string) string { return userinfoRe.ReplaceAllString(s, "${1}***@") }

// tailWriter forwards whole, redacted lines to w and keeps the last few for
// an error message.
type tailWriter struct {
	mu   sync.Mutex
	w    io.Writer
	buf  []byte
	tail []string
}

const tailLines = 15

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		t.line(string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	return len(p), nil
}

func (t *tailWriter) line(s string) {
	s = Redact(s)
	if t.w != nil {
		fmt.Fprintln(t.w, s)
	}
	t.tail = append(t.tail, s)
	if len(t.tail) > tailLines {
		t.tail = t.tail[len(t.tail)-tailLines:]
	}
}

func (t *tailWriter) close() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) > 0 {
		t.line(string(t.buf))
		t.buf = nil
	}
	return strings.Join(t.tail, "\n")
}

// run executes a host tool nsctl composed the arguments for, streaming its
// output through the redactor to stderr. The returned tail is redacted.
func run(ctx context.Context, env Env, environ []string, dir string, bin string, args ...string) (string, error) {
	tw := &tailWriter{w: env.Stderr}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = environ
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = tw, tw
	err := cmd.Run()
	return tw.close(), err
}

// lookPath finds name on the PATH the child environment will have, which is
// the PATH that matters: nsctl's own may differ in a test or under a profile.
func lookPath(name string, environ []string) (string, bool) {
	path := ""
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// getenv reads one variable out of a composed environment.
func getenv(environ []string, key string) (string, bool) {
	val, found := "", false
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val, found = v, true
		}
	}
	return val, found
}

// copyTree copies a directory of regular files. A symlink is refused: the
// tree came out of a zip or a clone, and a link is how either reaches out.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if d.Name() == ".git" && p != src {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(p, target)
		default:
			return fmt.Errorf("%s is not a regular file or directory", p)
		}
	})
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info.Mode()&0o111 != 0 {
		mode = 0o755
	}
	return os.WriteFile(dst, data, mode)
}
