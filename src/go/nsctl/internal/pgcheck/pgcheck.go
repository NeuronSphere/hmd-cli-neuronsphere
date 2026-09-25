// Package pgcheck refuses a start the configured postgres image cannot serve.
//
// Floci recreates an RDS instance's container from the *current*
// FLOCI_SERVICES_RDS_DEFAULT_POSTGRES_IMAGE on every start, while reusing the
// instance's named volume. A postgres *major* version bump therefore leaves the
// old data directory in place and the new binary refuses it:
//
//	FATAL:  database files are incompatible with server
//	DETAIL: The data directory was initialized by PostgreSQL version 12,
//	        which is not compatible with this version 14.
//
// Nothing reports that where it happens. Floci still calls the instance
// `available`, because it has one recorded, so the first symptom is whatever
// connects next failing -- a layer removed from the cause, and days removed if
// the bump arrived with an upgrade of the CLI rather than a deliberate edit.
//
// This is the detector the Python CLI already carries (pg_upgrade.find_mismatches
// and assert_compatible) ported to Go, and only the detector. Migrating a volume
// is a pg_dumpall/restore dance that belongs in one place, and both front ends
// address the same HMD_HOME, so the refusal names `hmd neuronsphere db upgrade`
// rather than reimplementing it.
//
// Everything here is best-effort in one direction on purpose: an image that is
// not pulled, a Docker that will not answer, a volume nothing has initialised
// yet all yield *no* mismatch. This gates a start, and a false alarm that
// refuses a working platform is worse than a missed warning that costs a
// container restart.
package pgcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// VolumePrefix is how Floci names an RDS instance's data volume. Matched by
	// name because those volumes carry no label tying them to an account --
	// the same reason container.VolumesMatching exists.
	VolumePrefix = "floci-rds-"
	// MigrationArtifacts are the names a migration leaves behind. Both hold an
	// old-major data directory by definition, so reporting one is a permanent
	// refusal: the remedy it names has already been performed, and performing
	// it again would migrate the backup.
	//
	// Excluded by name here as well as avoided by pgupgrade's naming, because
	// container.VolumesMatching matches a substring rather than a prefix --
	// hmd-pgbackup-floci-rds-db-x contains floci-rds- and is swept -- and
	// because backups written by the Python CLI are named exactly that way.
	// pgData is where the postgres images put their data directory.
	pgData = "/var/lib/postgresql/data"
)

// MigrationArtifacts are the volume-name fragments that mark a migration's own
// backup or dump. See VolumePrefix.
var MigrationArtifacts = []string{"hmd-pgbackup-", "hmd-pgdump-"}

// isMigrationArtifact reports whether a volume is one a migration made.
func isMigrationArtifact(volume string) bool {
	for _, marker := range MigrationArtifacts {
		if strings.Contains(volume, marker) {
			return true
		}
	}
	return false
}

// Docker is the surface this needs, narrowed so the check is testable without a
// daemon.
type Docker interface {
	VolumesMatching(ctx context.Context, prefix string) []string
	ImageEnv(ctx context.Context, ref string) map[string]string
	VolumeUserImage(ctx context.Context, volume string) string
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// Mismatch is one volume the configured image cannot start against.
type Mismatch struct {
	// Volume is the Docker volume holding the data directory.
	Volume string
	// Found is the major version that initialised it; Expected is the major
	// version the configured image ships.
	Found    string
	Expected string
	// Image is what is configured now, MadeBy what wrote the data directory.
	// MadeBy is what a pin-it-back remedy names, so it is worth carrying even
	// though only Image decides anything.
	Image  string
	MadeBy string
}

// FindMismatches lists the volumes image cannot be started against.
func FindMismatches(ctx context.Context, d Docker, image, flociDataDir string) []Mismatch {
	if d == nil || image == "" {
		return nil
	}
	expected := d.ImageEnv(ctx, image)["PG_MAJOR"]
	if expected == "" {
		// Not pulled, or not a postgres image. Either way this check has
		// nothing to compare and must not guess.
		return nil
	}

	state, known := flociState(flociDataDir)
	var out []Mismatch
	for _, volume := range d.VolumesMatching(ctx, VolumePrefix) {
		if isMigrationArtifact(volume) || !isLive(volume, state, known) {
			continue
		}
		// Read PG_VERSION with the image that wrote it, not the incoming one:
		// the incoming image may not be pulled yet, and a `docker run` here
		// would turn a pre-flight into a download.
		madeBy := d.VolumeUserImage(ctx, volume)
		readWith := madeBy
		if readWith == "" {
			readWith = image
		}
		found := volumePGVersion(ctx, d, volume, readWith)
		if found == "" || found == expected {
			continue
		}
		out = append(out, Mismatch{
			Volume: volume, Found: found, Expected: expected,
			Image: image, MadeBy: madeBy,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Volume < out[j].Volume })
	return out
}

// Explain words the refusal: what is wrong, and both ways out of it.
func Explain(ms []Mismatch) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s ships PostgreSQL %s, but %s initialised by an older major version:\n",
		ms[0].Image, ms[0].Expected, plural(len(ms), "a data directory was", "data directories were"))
	for _, m := range ms {
		line := fmt.Sprintf("  %s holds PostgreSQL %s", m.Volume, m.Found)
		if m.MadeBy != "" {
			line += ", written by " + m.MadeBy
		}
		fmt.Fprintln(&b, line)
	}
	b.WriteString("\nPostgres refuses a data directory from another major version, and Floci recreates\n" +
		"the container from the configured image while keeping the volume -- so starting\n" +
		"here would crash-loop the database and report it available. Either:\n\n" +
		"  hmd neuronsphere db upgrade     # migrate the data, keeping it\n")
	if ms[0].MadeBy != "" {
		fmt.Fprintf(&b, "  export HMD_POSTGRES_BASE_VERSION=%s   # stay on the old image\n", tagOf(ms[0].MadeBy))
	} else {
		b.WriteString("  export HMD_POSTGRES_BASE_VERSION=<old tag>   # stay on the old image\n")
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// tagOf is the tag part of an image reference, or "" when it carries none.
func tagOf(ref string) string {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ""
	}
	return ref[i+1:]
}

// volumePGVersion is the major version that initialised a volume's data
// directory, or "" when it has none yet.
//
// Read with the postgres image itself rather than a helper like alpine, so the
// check needs no image the platform does not already have.
func volumePGVersion(ctx context.Context, d Docker, volume, image string) string {
	stdout, _, err := d.Run(ctx, "run", "--rm", "--entrypoint", "cat",
		"-v", volume+":"+pgData, image, pgData+"/PG_VERSION")
	if err != nil {
		// An uninitialised volume has no PG_VERSION. That is a fresh instance,
		// not a mismatch.
		return ""
	}
	return strings.TrimSpace(string(stdout))
}

// flociState is Floci's persisted RDS instance records as raw text, and whether
// they could be read at all.
//
// Read off disk rather than through the API because this runs *before* Floci
// starts. The second return keeps "no instances recorded" distinct from "cannot
// tell": an unreadable directory is not proof of absence.
func flociState(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		// Purged, or never bootstrapped: Floci has no instances to recreate.
		return "", true
	}
	data, err := os.ReadFile(filepath.Join(dir, "rds-instances.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", true
		}
		return "", false
	}
	return string(data), true
}

// isLive reports whether any recorded RDS instance would mount a volume.
//
// A volume no instance references is inert: Floci recreates containers from its
// records, so it will never be mounted and cannot fail a start. Refusing over
// one is a dead end -- a purge discards those records, which is what orphaned
// the volume in the first place, so there would be no way forward.
//
// Matched by the volume's identifying token appearing anywhere in the records
// rather than by parsing a schema Floci does not document. Records that cannot
// be read at all make everything live, which is the conservative direction: a
// wrong "orphan" would skip a real incompatibility.
func isLive(volume, state string, known bool) bool {
	if !known {
		return true
	}
	if strings.TrimSpace(state) == "" {
		return false
	}
	token := strings.TrimPrefix(volume, VolumePrefix)
	for _, part := range strings.Split(token, "-") {
		if len(part) >= 8 && strings.Contains(state, part) {
			return true
		}
	}
	return false
}
