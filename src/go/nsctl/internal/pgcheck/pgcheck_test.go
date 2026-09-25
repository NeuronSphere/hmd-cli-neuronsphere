package pgcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDocker answers from tables rather than a daemon.
type fakeDocker struct {
	volumes    []string
	imageEnv   map[string]map[string]string
	volumeUser map[string]string
	pgVersion  map[string]string
}

// Contains, not HasPrefix: container.VolumesMatching is documented as matching
// a substring, and a fake that was stricter than the real thing hid the fact
// that hmd-pgbackup-floci-rds-db-x is swept exactly as floci-rds-db-x is.
func (f *fakeDocker) VolumesMatching(_ context.Context, prefix string) []string {
	var out []string
	for _, v := range f.volumes {
		if strings.Contains(v, prefix) {
			out = append(out, v)
		}
	}
	return out
}

func (f *fakeDocker) ImageEnv(_ context.Context, ref string) map[string]string {
	return f.imageEnv[ref]
}

func (f *fakeDocker) VolumeUserImage(_ context.Context, volume string) string {
	return f.volumeUser[volume]
}

// Run answers only the `cat PG_VERSION` invocation, keyed by the volume in the
// -v argument, so a test says what a data directory holds without a container.
func (f *fakeDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	for i, a := range args {
		if a != "-v" || i+1 >= len(args) {
			continue
		}
		volume, _, _ := strings.Cut(args[i+1], ":")
		if version, ok := f.pgVersion[volume]; ok {
			return []byte(version + "\n"), nil, nil
		}
	}
	return nil, []byte("cat: no such file"), errors.New("exit 1")
}

// liveState writes Floci records naming every volume's token, so nothing is
// treated as orphaned.
func liveState(t *testing.T, volumes ...string) string {
	t.Helper()
	dir := t.TempDir()
	var tokens []string
	for _, v := range volumes {
		tokens = append(tokens, strings.TrimPrefix(v, VolumePrefix))
	}
	body := `[{"id":"` + strings.Join(tokens, `"},{"id":"`) + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "rds-instances.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const (
	vol14  = VolumePrefix + "aaaaaaaa-1111"
	img14  = "ghcr.io/hmdlabs/hmd-postgres-base:0.3"
	img12  = "ghcr.io/hmdlabs/hmd-postgres-base:0.2.11"
	envPG  = "PG_MAJOR"
	dbUpgr = "hmd neuronsphere db upgrade"
)

func base() *fakeDocker {
	return &fakeDocker{
		volumes:    []string{vol14},
		imageEnv:   map[string]map[string]string{img14: {envPG: "14"}, img12: {envPG: "12"}},
		volumeUser: map[string]string{vol14: img12},
		pgVersion:  map[string]string{vol14: "12"},
	}
}

func TestAMajorBumpOverExistingDataIsAMismatch(t *testing.T) {
	t.Parallel()

	got := FindMismatches(context.Background(), base(), img14, liveState(t, vol14))
	if len(got) != 1 {
		t.Fatalf("FindMismatches = %v, want one mismatch", got)
	}
	if got[0].Found != "12" || got[0].Expected != "14" {
		t.Errorf("found/expected = %q/%q, want 12/14", got[0].Found, got[0].Expected)
	}
	// The image that wrote the directory is what a pin-it-back remedy names.
	if got[0].MadeBy != img12 {
		t.Errorf("MadeBy = %q, want %q", got[0].MadeBy, img12)
	}
}

func TestTheSameMajorIsNotAMismatch(t *testing.T) {
	t.Parallel()

	d := base()
	d.pgVersion[vol14] = "14"
	d.volumeUser[vol14] = img14
	if got := FindMismatches(context.Background(), d, img14, liveState(t, vol14)); len(got) != 0 {
		t.Errorf("FindMismatches = %v, want none", got)
	}
}

// The whole point of the orphan rule: a purge discards Floci's records, so a
// volume nothing references can never be mounted. Refusing over one would leave
// no way forward, because the thing that would clear it is what orphaned it.
func TestAnOrphanedVolumeIsIgnored(t *testing.T) {
	t.Parallel()

	empty := t.TempDir() // a readable state dir with no records
	if got := FindMismatches(context.Background(), base(), img14, empty); len(got) != 0 {
		t.Errorf("FindMismatches = %v, want none for an unreferenced volume", got)
	}
}

// Unreadable records must not be read as "no instances": that would silently
// skip a real incompatibility. Everything is live instead.
func TestUnreadableFlociStateTreatsVolumesAsLive(t *testing.T) {
	t.Parallel()

	if got := FindMismatches(context.Background(), base(), img14, ""); len(got) != 1 {
		t.Errorf("FindMismatches = %v, want the mismatch kept when state is unknown", got)
	}
}

// Every indeterminate case yields no mismatch, because this gates a start and a
// false alarm refuses a platform that was working.
func TestIndeterminateCasesRaiseNoAlarm(t *testing.T) {
	t.Parallel()

	state := liveState(t, vol14)
	for _, tt := range []struct {
		name   string
		image  string
		mutate func(*fakeDocker)
	}{
		{"the configured image is not pulled", "ghcr.io/hmdlabs/hmd-postgres-base:9.9", nil},
		{"no image is configured", "", nil},
		{"the image declares no PG_MAJOR", img14, func(d *fakeDocker) {
			d.imageEnv[img14] = map[string]string{"PATH": "/usr/bin"}
		}},
		{"the volume has no data directory yet", img14, func(d *fakeDocker) {
			delete(d.pgVersion, vol14)
		}},
		{"there are no RDS volumes", img14, func(d *fakeDocker) { d.volumes = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := base()
			if tt.mutate != nil {
				tt.mutate(d)
			}
			if got := FindMismatches(context.Background(), d, tt.image, state); len(got) != 0 {
				t.Errorf("FindMismatches = %v, want none", got)
			}
		})
	}
}

func TestANilDockerIsNotAMismatch(t *testing.T) {
	t.Parallel()

	if got := FindMismatches(context.Background(), nil, img14, ""); got != nil {
		t.Errorf("FindMismatches = %v, want nil", got)
	}
}

// The refusal has to carry both ways out, or it is a dead end.
func TestExplainNamesBothRemedies(t *testing.T) {
	t.Parallel()

	msg := Explain(FindMismatches(context.Background(), base(), img14, liveState(t, vol14)))
	for _, want := range []string{vol14, "12", "14", img12, dbUpgr, "HMD_POSTGRES_BASE_VERSION=0.2.11"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Explain() does not mention %q:\n%s", want, msg)
		}
	}
}

func TestExplainIsEmptyWithoutMismatches(t *testing.T) {
	t.Parallel()

	if got := Explain(nil); got != "" {
		t.Errorf("Explain(nil) = %q, want empty", got)
	}
}

// A migration's own backup holds an old-major data directory by definition, so
// reporting it is a refusal with no way out: the remedy it names has already
// been performed, and performing it again would migrate the backup.
//
// Found by a live rehearsal, which migrated a volume and was then told its
// backup was the next thing to migrate.
func TestMigrationArtifactsAreNotReportedAsMismatches(t *testing.T) {
	live := VolumePrefix + "db-aaaaaaaa-1111"
	backup := "hmd-pgbackup-db-aaaaaaaa-1111-pg12"
	dump := "hmd-pgdump-db-aaaaaaaa-1111-pg12"
	// And the shape the Python writes, which carries the prefix outright.
	legacy := "hmd-pgbackup-" + live + "-pg12"

	d := &fakeDocker{
		volumes:  []string{live, backup, dump, legacy},
		imageEnv: map[string]map[string]string{"new:14": {"PG_MAJOR": "14"}},
		pgVersion: map[string]string{
			live: "12", backup: "12", dump: "12", legacy: "12",
		},
	}

	got := FindMismatches(context.Background(), d, "new:14", liveState(t, live, backup, dump, legacy))
	if len(got) != 1 {
		t.Fatalf("want only the live volume reported, got %+v", got)
	}
	if got[0].Volume != live {
		t.Errorf("reported %q, want %q", got[0].Volume, live)
	}
}
