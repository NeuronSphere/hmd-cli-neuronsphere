package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker is a stand-in docker CLI: `save` prints a payload, and `exec -i`
// copies stdin to a file so the test can see what reached the node.
func fakeDocker(t *testing.T, saveExit, execExit string) (bin, received string) {
	t.Helper()
	dir := t.TempDir()
	received = filepath.Join(dir, "received")
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
case "$1" in
save) echo "image-bytes:$2"; echo "save said no" >&2; exit ` + saveExit + ` ;;
exec) echo "$@" > "` + received + `.args"; cat > "` + received + `"; echo "ctr said no"; exit ` + execExit + ` ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, received
}

func TestImportToNodePipesTheSavedImageIntoCtr(t *testing.T) {
	bin, received := fakeDocker(t, "0", "0")
	d := &Docker{Bin: bin}

	if err := d.ImportToNode(context.Background(), "node-1", "my/img:dev"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(received)
	if strings.TrimSpace(string(got)) != "image-bytes:my/img:dev" {
		t.Fatalf("node received %q", got)
	}
	args, _ := os.ReadFile(received + ".args")
	if want := "exec -i node-1 ctr -n k8s.io images import -"; strings.TrimSpace(string(args)) != want {
		t.Fatalf("exec args = %q, want %q", args, want)
	}
}

func TestImportToNodeReportsASaveFailure(t *testing.T) {
	bin, _ := fakeDocker(t, "1", "0")
	err := (&Docker{Bin: bin}).ImportToNode(context.Background(), "node-1", "my/img:dev")
	if err == nil || !strings.Contains(err.Error(), "save said no") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportToNodeReportsAnImportFailure(t *testing.T) {
	bin, _ := fakeDocker(t, "0", "1")
	err := (&Docker{Bin: bin}).ImportToNode(context.Background(), "node-1", "my/img:dev")
	if err == nil || !strings.Contains(err.Error(), "ctr said no") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportToNodeNeedsANodeAndARef(t *testing.T) {
	d := &Docker{}
	if d.ImportToNode(context.Background(), "", "img") == nil || d.ImportToNode(context.Background(), "node", "") == nil {
		t.Fatal("empty node or ref must be refused")
	}
}
