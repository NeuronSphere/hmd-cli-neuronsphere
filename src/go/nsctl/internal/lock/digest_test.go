package lock

import (
	"strings"
	"testing"
)

// NERD017 SPEC007: an optional digest per entry, under schema version 1.

func TestDigestRoundTripsAndIsOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	l := &Lock{Version: Version, RepoClassName: "hmd-stack-x", GeneratedFrom: "pins", Resolved: []Entry{
		{RepoClassName: "hmd-inf-a", Version: "0.1.0", Profiles: []string{}, ContentPath: "repository:/hmd-inf-a/0.1.0/hmd-inf-a_0.1.0_build.zip"},
		{RepoClassName: "hmd-inf-b", Version: "0.2.0", Profiles: []string{}, ContentPath: "repository:/hmd-inf-b/0.2.0/hmd-inf-b_0.2.0_build.zip",
			Digest: "sha256:" + strings.Repeat("a", 64)},
	}}
	dir := t.TempDir()
	if err := Write(dir, l); err != nil {
		t.Fatal(err)
	}
	back, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != 1 {
		t.Errorf("schema version changed to %d; SPEC007 keeps it at 1", back.Version)
	}
	a, _ := back.Entry("hmd-inf-a")
	b, _ := back.Entry("hmd-inf-b")
	if a.Digest != "" || b.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Errorf("digests = %q / %q", a.Digest, b.Digest)
	}
}

func TestOldLockWithoutDigestStillParses(t *testing.T) {
	t.Parallel()
	l, err := Parse([]byte(`version = 1
repo_class_name = "x"
generated_from = "pins"

[[resolved]]
repo_class_name = "hmd-inf-a"
version = "0.1.0"
profiles = []
content_path = "repository:/hmd-inf-a/0.1.0/hmd-inf-a_0.1.0_build.zip"
`))
	if err != nil {
		t.Fatal(err)
	}
	if l.Resolved[0].Digest != "" {
		t.Errorf("Digest = %q", l.Resolved[0].Digest)
	}
}

func TestSetDigestAndVerify(t *testing.T) {
	t.Parallel()
	l := &Lock{Version: Version, Resolved: []Entry{{RepoClassName: "a", Version: "1"}}}
	if l.SetDigest("missing", "sha256:x") {
		t.Error("SetDigest on an absent class must report false")
	}
	want := "sha256:" + strings.Repeat("b", 64)
	if !l.SetDigest("a", want) {
		t.Fatal("SetDigest must report true for a present class")
	}
	e, _ := l.Entry("a")
	if e.Digest != want {
		t.Errorf("Digest = %q", e.Digest)
	}
	if err := e.VerifyDigest(want); err != nil {
		t.Errorf("matching digest: %v", err)
	}
	if err := e.VerifyDigest("sha256:" + strings.Repeat("c", 64)); err == nil || !strings.Contains(err.Error(), "a@1") {
		t.Errorf("mismatch must fail naming the entry: %v", err)
	}
	// An entry with no digest cannot disagree.
	if err := (Entry{RepoClassName: "a"}).VerifyDigest(want); err != nil {
		t.Errorf("no recorded digest must verify: %v", err)
	}
}
