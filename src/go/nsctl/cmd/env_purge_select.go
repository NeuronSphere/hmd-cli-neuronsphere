package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lease"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// purgeEnvironment and purgeAllEnvironments are environment.Purge and
// environment.PurgeAll, replaceable in tests: a purge deletes through Floci by
// account, and a test registry's accounts are the platform's.
var (
	purgeEnvironment     = environment.Purge
	purgeAllEnvironments = environment.PurgeAll
)

// poolCreated is the name the pool gives an environment it registers.
var poolCreated = regexp.MustCompile("^" + regexp.QuoteMeta(lease.CreatedPrefix) + `[0-9]+$`)

// idleEnv is a pool-created environment nobody holds, and what it last held.
type idleEnv struct {
	Slug     string    `json:"env"`
	Released time.Time `json:"released"`
	Template string    `json:"template,omitempty"`
	// Trees are the instances its manifest still deploys from a working tree.
	Trees []string `json:"trees,omitempty"`
}

// idlePool lists the pool-created environments that hold no lease and have
// been released at least once (NERD035 SPEC007), most recently released
// first. Configured members are the user's own and never appear.
func idlePool(opts *Options, home string, store *lease.Store) ([]idleEnv, error) {
	names, err := registeredNames(home, opts)()
	if err != nil {
		return nil, err
	}
	cfg, err := nsconfig.Load(home, opts.Lookup)
	if err != nil && !errors.Is(err, nsconfig.ErrNoConfig) {
		return nil, err
	}
	members := map[string]bool{}
	for _, m := range cfg.EnvPool().Members {
		members[m] = true
	}
	leases, _, err := store.List()
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, l := range leases {
		held[l.Env] = true
	}

	var out []idleEnv
	for _, slug := range names {
		if !poolCreated.MatchString(slug) || members[slug] || held[slug] {
			continue
		}
		released := store.ReleasedAt(slug)
		if released.IsZero() {
			continue
		}
		e := idleEnv{Slug: slug, Released: released}
		if m, err := manifest.Load(home, slug, opts.Lookup); err == nil && m != nil {
			e.Template = m.Template
			for _, r := range m.Repos {
				if r.Source != nil && r.Source.Type == manifest.SourceLocal {
					e.Trees = append(e.Trees, r.InstanceName)
				}
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Released.After(out[j].Released) })
	return out, nil
}

// selectStale is the selectors' rule over idle (most recent first): drop the
// keep most recent when keep >= 0, keep those idle longer than idle when it
// is set. The result is oldest first, the order they are purged in.
func selectStale(idle []idleEnv, now time.Time, idleFor time.Duration, keep int) []idleEnv {
	pool := idle
	if keep >= 0 {
		if keep >= len(pool) {
			pool = nil
		} else {
			pool = pool[keep:]
		}
	}
	var out []idleEnv
	for _, e := range pool {
		if idleFor > 0 && now.Sub(e.Released) <= idleFor {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Released.Before(out[j].Released) })
	return out
}

// shortDuration renders an idle age the way a person says it: 3h, 2d4h, 10m.
func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour:
		days, hours := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	case d >= time.Hour:
		hours, minutes := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}

// describeIdle is one line about an idle pool environment.
func describeIdle(e idleEnv, now time.Time) string {
	parts := []string{"idle " + shortDuration(now.Sub(e.Released))}
	if e.Template != "" {
		parts = append(parts, "template "+e.Template)
	}
	if len(e.Trees) > 0 {
		parts = append(parts, "working trees "+strings.Join(e.Trees, ", "))
	}
	return strings.Join(parts, "; ")
}

// purgeSelectors are env purge's --idle, --keep and --dry-run.
type purgeSelectors struct {
	idle   time.Duration
	keep   int
	dryRun bool
}

func (p *purgeSelectors) bind(cmd *cobra.Command) {
	cmd.Flags().DurationVar(&p.idle, "idle", 0, "purge pool environments released longer ago than this, e.g. 72h")
	cmd.Flags().IntVar(&p.keep, "keep", -1, "purge all but this many of the most recently released pool environments")
	cmd.Flags().BoolVar(&p.dryRun, "dry-run", false, "with --idle or --keep, list what would be purged and purge nothing")
}

func (p *purgeSelectors) requested(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("idle") || cmd.Flags().Changed("keep")
}

// run is env purge with selectors (NERD035 SPEC007). It never reaches the
// purge-everything path, whatever it selects.
func (p *purgeSelectors) run(cmd *cobra.Command, opts *Options, home string, envOpts *environment.Options, yes bool) error {
	if cmd.Flags().Changed("idle") && p.idle <= 0 {
		return nserr.New(nserr.Usage, "--idle must be a positive duration such as 72h")
	}
	if cmd.Flags().Changed("keep") && p.keep < 0 {
		return nserr.New(nserr.Usage, "--keep must be zero or more")
	}
	store := leaseStore(cmd, opts, home)
	idle, err := idlePool(opts, home, store)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	now := time.Now()
	selected := selectStale(idle, now, p.idle, p.keep)
	out := cmd.OutOrStdout()
	if len(selected) == 0 {
		fmt.Fprintln(out, "nothing to purge")
		return nil
	}
	renderSelection(out, selected, now)
	if p.dryRun {
		return nil
	}
	if !yes {
		return nserr.New(nserr.Usage, "this permanently destroys the %d environment(s) above -- cluster, database, graph and state. Pass --yes to confirm.",
			len(selected))
	}

	for _, e := range selected {
		// Held for the teardown, so the pool cannot hand it to a session
		// while its cluster and database are being deleted.
		l, err := store.Acquire(e.Slug, lease.Request{Holder: "nsctl env purge", PID: os.Getpid(), TTL: time.Hour})
		var heldErr *lease.HeldError
		if errors.As(err, &heldErr) {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: skipped %s: leased by %s since it was selected\n", e.Slug, heldErr.Lease.Holder)
			continue
		}
		if err != nil {
			return leaseError(err)
		}
		if err := purgeEnvironment(cmd.Context(), envOpts, e.Slug); err != nil {
			_ = store.Release(e.Slug, l.Token)
			return err
		}
		_ = store.Release(e.Slug, l.Token)
		_ = store.Forget(e.Slug)
		fmt.Fprintf(out, "Purged %s\n", e.Slug)
	}
	return nil
}

func renderSelection(out io.Writer, selected []idleEnv, now time.Time) {
	fmt.Fprintf(out, "Selected %d pool environment(s):\n", len(selected))
	for _, e := range selected {
		fmt.Fprintf(out, "  %-12s %s\n", e.Slug, describeIdle(e, now))
	}
}
