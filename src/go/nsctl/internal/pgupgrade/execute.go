package pgupgrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Executor runs a Plan.
type Executor struct {
	Docker   Docker
	Timeouts Timeouts
	// Progress is called as each step begins. Optional.
	Progress func(volume string, step Step)
	// Warn reports something the user should know that did not stop the run.
	Warn func(format string, args ...any)
	// KeepDump mirrors Options.KeepDump, for the steps that consult it.
	KeepDump bool
}

// Run migrates every volume in the plan, stopping at the first failure.
//
// Stopping rather than continuing, unlike the Python's upgrade_all: a failure
// here is almost always systemic -- no disk, no daemon, an image that will not
// start -- and carrying on simply multiplies the number of wiped volumes
// waiting on the same repair.
func (e *Executor) Run(ctx context.Context, p Plan) error {
	for _, v := range p.Volumes {
		if err := e.volume(ctx, v); err != nil {
			return fmt.Errorf("migrating %s from PostgreSQL %s to %s: %w", v.Volume, v.From, v.To, err)
		}
	}
	return nil
}

func (e *Executor) volume(ctx context.Context, v VolumePlan) error {
	state, err := ReadState(ctx, e.Docker, v.Dump, v.DumpImage)
	if err != nil {
		return err
	}
	state.Volume, state.From, state.To = v.Volume, v.From, v.To
	state.Image, state.DumpImage = v.Image, v.DumpImage

	// Whatever happens, no container of ours is left holding the data
	// directory: Floci will want it back, and a helper still mounted is how a
	// migrated volume comes back up refusing connections.
	defer func() {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.Timeouts.Short)
		defer cancel()
		_ = e.Docker.RemoveContainer(stop, v.Helper)
	}()

	for _, step := range v.Steps {
		if e.Progress != nil {
			e.Progress(v.Volume, step)
		}
		if err := e.step(ctx, v, &state, step); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) step(ctx context.Context, v VolumePlan, state *State, step Step) error {
	switch step.Kind {
	case StepPull:
		// Before anything is touched, so a missing image is discovered while
		// the volume is still exactly as the user left it.
		pull, cancel := context.WithTimeout(ctx, e.Timeouts.Copy)
		defer cancel()
		if err := e.Docker.PullImage(pull, v.DumpImage); err != nil {
			return fmt.Errorf("pulling %s, which is what can read a PostgreSQL %s data directory: %w",
				v.DumpImage, v.From, err)
		}
		return nil

	case StepRemoveHelper:
		return clearHelpers(ctx, e.Docker, v)

	case StepInventory:
		if err := e.startServer(ctx, v, v.DumpImage); err != nil {
			return err
		}
		inv, err := e.inventory(ctx, v.Helper)
		if err != nil {
			return fmt.Errorf("reading what %s holds: %w", v.Volume, err)
		}
		state.Databases, state.Roles = inv.Databases, inv.Roles
		return nil

	case StepBackup:
		return e.backup(ctx, v, state)

	case StepDump:
		return e.dump(ctx, v)

	case StepCheckDump:
		return e.checkDump(ctx, v, state)

	case StepWipe:
		return e.wipe(ctx, v, state)

	case StepRestore:
		return e.restore(ctx, v, state)

	case StepVerify:
		return e.verify(ctx, v, state)

	case StepDropDump:
		return e.dropDump(ctx, v)
	}
	return fmt.Errorf("unknown migration step %q", step.Kind)
}

// backup copies the old data directory, unless one is already there.
func (e *Executor) backup(ctx context.Context, v VolumePlan, state *State) error {
	// Re-asked here rather than trusted from the plan. The plan may have been
	// built minutes ago, and the question -- is the only good copy of this
	// data already in that volume -- is one whose stale answer overwrites it.
	if volumeHasData(ctx, e.Docker, v.Backup, v.DumpImage) {
		if e.Warn != nil {
			e.Warn("%s already holds a backup of %s; keeping it rather than copying over it.", v.Backup, v.Volume)
		}
		return e.record(ctx, v, state, StageBackedUp)
	}
	// Stop the server first if one of ours is up: cp -a of a directory being
	// written to copies a torn one.
	if err := e.Docker.RemoveContainer(ctx, v.Helper); err != nil {
		return err
	}
	copyCtx, cancel := context.WithTimeout(ctx, e.Timeouts.Copy)
	defer cancel()
	if _, stderr, err := e.Docker.Run(copyCtx, "run", "--rm", "--entrypoint", "sh",
		"-v", v.Volume+":/from", "-v", v.Backup+":/to", v.DumpImage,
		"-c", "cp -a /from/. /to/"); err != nil {
		return fmt.Errorf("copying %s to %s: %s", v.Volume, v.Backup, detail(stderr, err))
	}
	return e.record(ctx, v, state, StageBackedUp)
}

// dump writes pg_dumpall's output into the dump volume.
//
// Into a file in a volume, not into this process's memory as the Python does.
// A dump held in memory is lost with the process, and the Python wipes the
// live directory while still holding it -- so a crash in that window loses the
// data outright, with the backup volume the only thing standing between the
// user and a restore from nothing.
func (e *Executor) dump(ctx context.Context, v VolumePlan) error {
	if err := e.startServer(ctx, v, v.DumpImage); err != nil {
		return err
	}
	dumpCtx, cancel := context.WithTimeout(ctx, e.Timeouts.Dump)
	defer cancel()
	_, stderr, err := e.Docker.Run(dumpCtx, "exec", v.Helper, "sh", "-c",
		fmt.Sprintf("pg_dumpall -U postgres --clean --if-exists > %s/%s", DumpDir, DumpFile))
	if err != nil {
		return fmt.Errorf("pg_dumpall on %s: %s", v.Volume, detail(stderr, err))
	}
	// The old server has done its job, and must not be holding the directory
	// when the wipe comes.
	return e.Docker.RemoveContainer(ctx, v.Helper)
}

// checkDump proves the dump is whole, and only then records the stage.
//
// pg_dumpall exits 0 on a truncated file when the disk fills under it, so the
// exit status is not enough to authorise what comes next. The trailer is.
func (e *Executor) checkDump(ctx context.Context, v VolumePlan, state *State) error {
	complete, size, err := dumpIsComplete(ctx, e.Docker, v.Dump, v.DumpImage)
	if err != nil {
		return fmt.Errorf("reading the dump in %s: %w", v.Dump, err)
	}
	if !complete {
		return fmt.Errorf("the dump in %s is %d bytes and does not end with %q, so it is not a complete "+
			"dump of %s. Nothing has been changed; the data directory is as it was",
			v.Dump, size, DumpTrailer, v.Volume)
	}
	return e.record(ctx, v, state, StageDumped)
}

// wipe empties the live data directory so the new image will initdb into it.
func (e *Executor) wipe(ctx context.Context, v VolumePlan, state *State) error {
	// The last thing checked before the only irreversible step, even though
	// checkDump has already run: this is the guard that has to hold when some
	// later edit reorders the steps above it.
	complete, _, err := dumpIsComplete(ctx, e.Docker, v.Dump, v.DumpImage)
	if err != nil || !complete {
		return fmt.Errorf("refusing to clear %s: the dump in %s could not be confirmed complete", v.Volume, v.Dump)
	}
	// And nothing may be holding it. Gate established this before the run;
	// this re-establishes it at the moment it matters.
	for _, u := range e.Docker.VolumeContainers(ctx, v.Volume) {
		if u.Running && !IsHelper(u.Name) {
			return nserr.New(nserr.InUse,
				"refusing to clear %s: %s started on it during the migration", v.Volume, u.Name)
		}
	}
	if err := e.Docker.RemoveContainer(ctx, v.Helper); err != nil {
		return err
	}

	wipeCtx, cancel := context.WithTimeout(ctx, e.Timeouts.Copy)
	defer cancel()
	if _, stderr, err := e.Docker.Run(wipeCtx, "run", "--rm", "--entrypoint", "sh",
		"-v", v.Volume+":"+PGData, v.DumpImage, "-c",
		fmt.Sprintf("rm -rf %s/..?* %s/.[!.]* %s/*", PGData, PGData, PGData)); err != nil {
		return fmt.Errorf("clearing %s: %s", v.Volume, detail(stderr, err))
	}
	return e.record(ctx, v, state, StageWiped)
}

// restore replays the dump into a freshly initialised new-major cluster.
func (e *Executor) restore(ctx context.Context, v VolumePlan, state *State) error {
	if err := e.startServer(ctx, v, v.Image); err != nil {
		return err
	}
	restoreCtx, cancel := context.WithTimeout(ctx, e.Timeouts.Restore)
	defer cancel()
	// ON_ERROR_STOP stays off: the image's own dbs_init.sh has already created
	// some of what the dump recreates, and stopping on the first collision
	// would abort every migration. What the errors were is then judged.
	_, stderr, err := e.Docker.Run(restoreCtx, "exec", v.Helper,
		"psql", "-U", "postgres", "-d", "postgres", "-f", DumpDir+"/"+DumpFile)
	if fatal := classify(string(stderr)); len(fatal) > 0 {
		return fmt.Errorf("restoring %s failed:\n  %s\n\nThe old data directory is still in %s",
			v.Volume, strings.Join(fatal, "\n  "), v.Backup)
	}
	if err != nil && len(stderr) == 0 {
		return fmt.Errorf("restoring %s: %w", v.Volume, err)
	}
	return e.record(ctx, v, state, StageRestored)
}

// verify re-reads the cluster and diffs it against what was recorded before the
// dump.
//
// The check that decides, because it depends on no parsing at all. Without it,
// a restore that did nothing whatsoever still reports the volume migrated --
// which is what the Python does today.
func (e *Executor) verify(ctx context.Context, v VolumePlan, state *State) error {
	if len(state.Databases) == 0 && len(state.Roles) == 0 {
		// Nothing was recorded to check against. Resuming a migration begun by
		// a version that did not record one is the only way here, and claiming
		// a verification that did not happen is worse than saying so.
		if e.Warn != nil {
			e.Warn("%s carries no record of what %s held before the dump, so the restore could not be verified.",
				v.Dump, v.Volume)
		}
		return e.record(ctx, v, state, StageVerified)
	}
	if err := e.startServer(ctx, v, v.Image); err != nil {
		return err
	}
	now, err := e.inventory(ctx, v.Helper)
	if err != nil {
		return fmt.Errorf("reading back what %s holds: %w", v.Volume, err)
	}
	before := Inventory{Databases: state.Databases, Roles: state.Roles}
	if lost := before.missingFrom(now); len(lost) > 0 {
		return fmt.Errorf("the restore of %s did not bring back:\n  %s\n\nThe old data directory is still in %s",
			v.Volume, strings.Join(lost, "\n  "), v.Backup)
	}
	return e.record(ctx, v, state, StageVerified)
}

func (e *Executor) dropDump(ctx context.Context, v VolumePlan) error {
	if err := e.Docker.RemoveContainer(ctx, v.Helper); err != nil {
		return err
	}
	rm, cancel := context.WithTimeout(ctx, e.Timeouts.Short)
	defer cancel()
	if _, stderr, err := e.Docker.Run(rm, "volume", "rm", v.Dump); err != nil {
		// Tidying up is not worth failing a migration that worked.
		if e.Warn != nil {
			e.Warn("could not remove %s: %s", v.Dump, detail(stderr, err))
		}
	}
	return nil
}

// startServer brings up a postgres of the given image on the volume, and waits
// for it to accept connections.
//
// One helper name per volume, reused across steps: if it is already running the
// right image this is a no-op, and if it is running the other one -- the old
// server when the new one is wanted -- it is replaced.
func (e *Executor) startServer(ctx context.Context, v VolumePlan, image string) error {
	for _, u := range e.Docker.VolumeContainers(ctx, v.Volume) {
		if u.Name == v.Helper && u.Running && u.Image == image {
			return e.waitReady(ctx, v, image)
		}
	}
	if err := e.Docker.RemoveContainer(ctx, v.Helper); err != nil {
		return err
	}
	start, cancel := context.WithTimeout(ctx, e.Timeouts.Short)
	defer cancel()
	// No published port: nothing outside this command has any business
	// reaching a database that is midway through being rewritten.
	if _, stderr, err := e.Docker.Run(start, "run", "-d", "--name", v.Helper,
		"-v", v.Volume+":"+PGData, "-v", v.Dump+":"+DumpDir,
		"-e", "POSTGRES_PASSWORD=postgres", image); err != nil {
		return fmt.Errorf("starting %s on %s: %s", image, v.Volume, detail(stderr, err))
	}
	return e.waitReady(ctx, v, image)
}

// waitReady blocks until the helper accepts connections, or explains why it
// never will.
func (e *Executor) waitReady(ctx context.Context, v VolumePlan, image string) error {
	deadline := time.Now().Add(e.Timeouts.Ready)
	for {
		probe, cancel := context.WithTimeout(ctx, e.Timeouts.Short)
		_, _, err := e.Docker.Run(probe, "exec", v.Helper, "pg_isready", "-U", "postgres")
		cancel()
		if err == nil {
			return nil
		}
		if running, _ := e.Docker.Running(ctx, v.Helper); !running {
			return fmt.Errorf("%s exited while starting on %s:\n%s",
				image, v.Volume, e.Docker.Logs(ctx, v.Helper, 25))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never accepted connections on %s within %s:\n%s",
				image, v.Volume, e.Timeouts.Ready, e.Docker.Logs(ctx, v.Helper, 25))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// inventory reads the databases and roles a running helper holds.
func (e *Executor) inventory(ctx context.Context, helper string) (Inventory, error) {
	databases, err := e.query(ctx, helper, databasesQuery)
	if err != nil {
		return Inventory{}, err
	}
	roles, err := e.query(ctx, helper, rolesQuery)
	if err != nil {
		return Inventory{}, err
	}
	return Inventory{Databases: databases, Roles: roles}, nil
}

func (e *Executor) query(ctx context.Context, helper, sql string) ([]string, error) {
	q, cancel := context.WithTimeout(ctx, e.Timeouts.Short)
	defer cancel()
	stdout, stderr, err := e.Docker.Run(q, "exec", helper,
		"psql", "-U", "postgres", "-d", "postgres", "-At", "-c", sql)
	if err != nil {
		return nil, fmt.Errorf("%s", detail(stderr, err))
	}
	return parseList(string(stdout)), nil
}

// record advances the stage manifest, so a later run knows where this one got
// to. Written after the step it describes, never before.
func (e *Executor) record(ctx context.Context, v VolumePlan, state *State, stage Stage) error {
	state.Stage = stage
	return WriteState(ctx, e.Docker, v.Dump, v.DumpImage, *state)
}

// detail prefers what the command said over what the exec package concluded.
func detail(stderr []byte, err error) string {
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}
