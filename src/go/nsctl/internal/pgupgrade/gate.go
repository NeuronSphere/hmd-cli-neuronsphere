package pgupgrade

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Blocker is one reason a migration must not start.
type Blocker struct {
	// Volume is the data directory at risk, or "" for a machine-wide reason.
	Volume string
	// Container is what is holding it.
	Container string
	// Forcible reports whether --force may proceed past this.
	Forcible bool
	// Reason is the sentence shown to the user.
	Reason string
}

// Gate reports every reason the planned migration must not run.
//
// Two kinds, and the difference between them is deliberate and should survive
// review:
//
//   - A container running on a volume this would rewrite is *absolute*. Two
//     postgres processes on one data directory is corruption, not a policy
//     disagreement, so --force cannot reach it. The Python checks nothing at
//     all here.
//   - A running Floci is *forcible*. It does not hold the directory itself,
//     but EnsureRDSRunning can start a database behind this command at any
//     moment, so the safe default is to refuse -- while a user who has
//     established that Floci is idle is refusing something they can see.
//
// A stopped container on the volume is the normal state, not a blocker: that
// is exactly what a platform that has been shut down for the migration looks
// like. A *running* helper of ours is the third case: it is wreckage from a
// crashed run of this command, and removing it is how the retry starts.
func Gate(ctx context.Context, d Docker, p Plan) []Blocker {
	var blockers []Blocker
	for _, v := range p.Volumes {
		for _, u := range d.VolumeContainers(ctx, v.Volume) {
			if !u.Running || IsHelper(u.Name) {
				continue
			}
			blockers = append(blockers, Blocker{
				Volume:    v.Volume,
				Container: u.Name,
				Reason: fmt.Sprintf(
					"%s is running on %s. Migrating a data directory a server is using corrupts it.",
					u.Name, v.Volume),
			})
		}
	}
	sort.Slice(blockers, func(i, j int) bool {
		if blockers[i].Volume != blockers[j].Volume {
			return blockers[i].Volume < blockers[j].Volume
		}
		return blockers[i].Container < blockers[j].Container
	})

	if running, _ := d.Running(ctx, floci.ContainerName); running {
		blockers = append(blockers, Blocker{
			Container: floci.ContainerName,
			Forcible:  true,
			Reason: "Floci is running. It restarts the databases it manages on demand, so one " +
				"could be started on a volume partway through being migrated.",
		})
	}
	return blockers
}

// Refuse turns blockers into the error the command exits with, or nil.
//
// extra is appended to the remedy -- cmd/db.go passes the environments that are
// running, which this package has no business resolving but the user needs in
// order to know what to stop.
func Refuse(blockers []Blocker, force bool, extra string) error {
	var stopping []Blocker
	for _, b := range blockers {
		if b.Forcible && force {
			continue
		}
		stopping = append(stopping, b)
	}
	if len(stopping) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("this rewrites a database, and something is using it:\n\n")
	for _, blocker := range stopping {
		fmt.Fprintf(&b, "  %s\n", blocker.Reason)
	}
	b.WriteString("\nStop the platform first:\n\n  nsctl env stop\n  nsctl control-plane stop\n")
	if extra != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(extra))
	}
	if onlyForcible(stopping) {
		b.WriteString("\nIf Floci is idle and you are sure nothing will start a database, --force proceeds.\n")
	}
	return nserr.New(nserr.InUse, "%s", strings.TrimSpace(b.String()))
}

func onlyForcible(blockers []Blocker) bool {
	for _, b := range blockers {
		if !b.Forcible {
			return false
		}
	}
	return len(blockers) > 0
}

// clearHelpers removes any helper container left behind by a crashed run.
//
// Separate from Gate because Gate establishes facts and changes nothing, and
// this is the one piece of wreckage the command is entitled to clear on its
// own: it put it there.
func clearHelpers(ctx context.Context, d Docker, v VolumePlan) error {
	for _, u := range d.VolumeContainers(ctx, v.Volume) {
		if !IsHelper(u.Name) {
			continue
		}
		if err := d.RemoveContainer(ctx, u.Name); err != nil {
			return fmt.Errorf("removing the leftover helper %s: %w", u.Name, err)
		}
	}
	// By name too: a helper that never got as far as mounting the volume, or
	// one whose volume the daemon no longer associates with it, is invisible
	// to the sweep above but will still collide on `docker run --name`.
	return d.RemoveContainer(ctx, v.Helper)
}
