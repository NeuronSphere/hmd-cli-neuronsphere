package pgupgrade

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/pgcheck"
)

// Docker is the surface a migration needs, narrowed so the whole of it is
// testable without a daemon. Satisfied by *container.Docker.
type Docker interface {
	pgcheck.Docker
	PullImage(ctx context.Context, ref string) error
	RemoveContainer(ctx context.Context, name string) error
	Logs(ctx context.Context, name string, lines int) string
	VolumeContainers(ctx context.Context, volume string) []container.VolumeUser
}

// Stage is how far one volume's migration has got, as recorded in the dump
// volume. The order of the constants is the order they are reached.
type Stage string

const (
	// StageNone is a volume no migration has touched.
	StageNone Stage = ""
	// StageBackedUp: the old data directory has been copied to the backup
	// volume. Nothing has been read or written since.
	StageBackedUp Stage = "backed-up"
	// StageDumped: a dump exists and carries its trailer. The live data
	// directory is still intact, so this is not yet a one-way street.
	StageDumped Stage = "dumped"
	// StageWiped: the live data directory is empty. From here the dump is the
	// only way forward, which is why this is the one stage that resumes
	// without being asked.
	StageWiped Stage = "wiped"
	// StageRestored: the dump has been replayed into a new-major cluster.
	StageRestored Stage = "restored"
	// StageVerified: the inventory taken before the dump was found again
	// afterwards. The migration is done.
	StageVerified Stage = "verified"
)

// reached orders the stages so a resume can ask "have we got at least this far".
var reached = map[Stage]int{
	StageNone: 0, StageBackedUp: 1, StageDumped: 2,
	StageWiped: 3, StageRestored: 4, StageVerified: 5,
}

// AtLeast reports whether s has reached other.
func (s Stage) AtLeast(other Stage) bool { return reached[s] >= reached[other] }

// Valid reports whether s is a stage this package writes. Anything else in a
// manifest is a file from another version or a corrupted one, and is treated as
// no progress at all rather than guessed at.
func (s Stage) Valid() bool { _, ok := reached[s]; return ok }

// StepKind is one action in a migration, named so a plan can be printed and,
// more importantly, so the order of the destructive ones can be asserted over
// as a value rather than observed as side effects.
type StepKind string

const (
	// StepPull fetches the old-major image before any work begins, so a
	// missing image is not discovered after the volume has been touched.
	StepPull StepKind = "pull"
	// StepRemoveHelper clears a helper container left by a crashed run.
	StepRemoveHelper StepKind = "remove-helper"
	// StepInventory records the databases and roles the old cluster holds.
	StepInventory StepKind = "inventory"
	// StepBackup copies the old data directory to the backup volume.
	StepBackup StepKind = "backup"
	// StepDump writes pg_dumpall's output into the dump volume.
	StepDump StepKind = "dump"
	// StepCheckDump proves the dump is whole before anything is destroyed.
	StepCheckDump StepKind = "check-dump"
	// StepWipe empties the live data directory. The first destructive step,
	// and the one every guard in this package exists to sequence.
	StepWipe StepKind = "wipe"
	// StepRestore replays the dump into the new-major cluster.
	StepRestore StepKind = "restore"
	// StepVerify re-reads the inventory and diffs it against StepInventory's.
	StepVerify StepKind = "verify"
	// StepDropDump removes the dump volume once the migration is verified.
	StepDropDump StepKind = "drop-dump"
)

// Destructive reports whether a step can lose data if it runs out of order.
func (k StepKind) Destructive() bool { return k == StepWipe }

// Step is one action, with the sentence a dry run prints for it.
type Step struct {
	Kind StepKind
	What string
}

// VolumePlan is everything one volume's migration will do.
type VolumePlan struct {
	// Volume is the live Floci data volume being migrated in place.
	Volume string
	// From and To are the major versions.
	From, To string
	// Image is the new postgres; DumpImage reads the old directory.
	Image, DumpImage string
	// Backup, Dump and Helper are the names this run will use.
	Backup, Dump, Helper string
	// Resume is the stage found in the dump volume before planning.
	Resume Stage
	// ReuseBackup is set when a non-empty backup already exists. It is
	// preserved rather than overwritten: a second run after a crash at the
	// wiped stage would otherwise copy an empty data directory over the only
	// good copy of the data.
	ReuseBackup bool
	// Steps is what Execute will do, in order.
	Steps []Step
}

// Plan is a whole migration, validated and printable, and separate from its
// execution so that what it will do can be asserted before anything runs.
type Plan struct {
	Image   string
	Volumes []VolumePlan
}

// Empty reports whether there is nothing to migrate.
func (p Plan) Empty() bool { return len(p.Volumes) == 0 }

// Options configures a migration.
type Options struct {
	// Image is the configured postgres. Empty means "resolve it the way the
	// start pre-flight does", which the caller has already done.
	Image string
	// FlociDataDir is passed to pgcheck so a volume no Floci instance claims
	// is left alone.
	FlociDataDir string
	// Only restricts the run to these volumes. Empty means every mismatch.
	Only []string
	// DumpImage overrides the image used to read the old data directory.
	DumpImage string
	// KeepDump leaves the dump volume in place after a verified migration.
	KeepDump bool
	// Force skips the Floci-running refusal. It cannot skip the per-volume
	// one: a second postgres on one data directory is corruption, not a
	// policy disagreement.
	Force bool
}

// steps is the pure half of planning: given how far a volume got and whether a
// backup is already there, what remains to be done.
//
// Written as one ordered list with stages filtered out of it, rather than a
// switch per stage, so that the invariant the tests assert -- no wipe before a
// dump and the check that proves it whole -- is a property of a single
// sequence and cannot be broken by one stage's branch drifting from another's.
func steps(v VolumePlan, keepDump bool) []Step {
	all := []struct {
		kind StepKind
		// after is the stage at which this step is already done.
		done Stage
		what string
	}{
		{StepPull, "", fmt.Sprintf("pull %s", v.DumpImage)},
		{StepRemoveHelper, "", fmt.Sprintf("remove any leftover %s", v.Helper)},
		{StepInventory, StageDumped, fmt.Sprintf("record the databases and roles in %s", v.Volume)},
		{StepBackup, StageBackedUp, backupWhat(v)},
		{StepDump, StageWiped, fmt.Sprintf("pg_dumpall %s into %s", v.Volume, v.Dump)},
		{StepCheckDump, StageRestored, fmt.Sprintf("check the dump in %s is complete", v.Dump)},
		{StepWipe, StageWiped, fmt.Sprintf("empty %s so %s can initdb", v.Volume, v.Image)},
		{StepRestore, StageRestored, fmt.Sprintf("restore the dump into %s under %s", v.Volume, v.Image)},
		{StepVerify, StageVerified, "re-read the databases and roles, and diff them against the record"},
		{StepDropDump, "", fmt.Sprintf("remove %s", v.Dump)},
	}

	var out []Step
	for _, s := range all {
		if s.kind == StepDropDump && keepDump {
			continue
		}
		// A stage of "" is a step that runs every time: pulling, clearing a
		// crashed helper, and dropping the dump are all idempotent and cheap,
		// and re-running them is how a resume gets back to a known footing.
		if s.done != "" && v.Resume.AtLeast(s.done) {
			continue
		}
		out = append(out, Step{Kind: s.kind, What: s.what})
	}
	return out
}

func backupWhat(v VolumePlan) string {
	if v.ReuseBackup {
		return fmt.Sprintf("keep the existing backup in %s", v.Backup)
	}
	return fmt.Sprintf("copy %s to %s", v.Volume, v.Backup)
}

// BuildPlan works out what migrating would do, changing nothing.
//
// Reads only: the mismatches pgcheck finds, the stage manifest in each dump
// volume, and whether a backup volume already holds anything.
func BuildPlan(ctx context.Context, d Docker, opts Options) (Plan, error) {
	if d == nil {
		return Plan{}, fmt.Errorf("no docker")
	}
	if opts.Image == "" {
		return Plan{}, fmt.Errorf("no postgres image configured")
	}

	mismatches := pgcheck.FindMismatches(ctx, d, opts.Image, opts.FlociDataDir)
	if len(opts.Only) > 0 {
		var err error
		mismatches, err = restrict(mismatches, opts.Only)
		if err != nil {
			return Plan{}, err
		}
	}

	p := Plan{Image: opts.Image}
	for _, m := range mismatches {
		v := VolumePlan{
			Volume:    m.Volume,
			From:      m.Found,
			To:        m.Expected,
			Image:     opts.Image,
			DumpImage: dumpImageFor(opts, m.Found),
			Backup:    BackupVolume(m.Volume, m.Found),
			Dump:      DumpVolume(m.Volume, m.Found),
			Helper:    HelperContainer(m.Volume),
		}
		state, err := ReadState(ctx, d, v.Dump, v.DumpImage)
		if err != nil {
			return Plan{}, fmt.Errorf("reading the migration state of %s: %w", m.Volume, err)
		}
		v.Resume = state.Stage
		v.ReuseBackup = volumeHasData(ctx, d, v.Backup, v.DumpImage)
		v.Steps = steps(v, opts.KeepDump)
		p.Volumes = append(p.Volumes, v)
	}
	return p, nil
}

func dumpImageFor(opts Options, major string) string {
	if opts.DumpImage != "" {
		return opts.DumpImage
	}
	return DumpImage(major)
}

// restrict narrows the mismatches to the names asked for, and refuses a name
// that is not among them. A --volume that matches nothing is a typo, and
// silently migrating everything instead is the wrong reading of it.
func restrict(ms []pgcheck.Mismatch, only []string) ([]pgcheck.Mismatch, error) {
	found := map[string]pgcheck.Mismatch{}
	for _, m := range ms {
		found[m.Volume] = m
	}
	var out []pgcheck.Mismatch
	var missing []string
	for _, name := range only {
		m, ok := found[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		out = append(out, m)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var have []string
		for _, m := range ms {
			have = append(have, m.Volume)
		}
		if len(have) == 0 {
			return nil, fmt.Errorf("no volume needs migrating, so --volume %s matches nothing",
				strings.Join(missing, ", "))
		}
		return nil, fmt.Errorf("--volume %s does not need migrating; these do: %s",
			strings.Join(missing, ", "), strings.Join(have, ", "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Volume < out[j].Volume })
	return out, nil
}

// Timeouts for the container work. Defaults matter because container.Docker.Run
// applies none of its own -- it inherits the caller's context and nothing else,
// while the capture-based helpers cap at fifteen seconds. A multi-gigabyte cp
// and a half-hour pg_dumpall both go through Run, so the context handed to them
// is the only thing standing between a slow migration and one that hangs.
type Timeouts struct {
	Short   time.Duration // inspecting, removing, reading a small file
	Copy    time.Duration // cp -a of a whole data directory
	Dump    time.Duration // pg_dumpall
	Restore time.Duration // psql replaying the dump
	Ready   time.Duration // waiting for a helper to accept connections
}

// DefaultTimeouts are deliberately generous: every one of these bounds an
// operation whose honest duration depends on how much data the user has, and
// the cost of being wrong is a migration killed partway.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Short:   2 * time.Minute,
		Copy:    30 * time.Minute,
		Dump:    60 * time.Minute,
		Restore: 60 * time.Minute,
		Ready:   5 * time.Minute,
	}
}
