package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/environment"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hosturl"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/lease"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// newEnvLeaseCommand groups the run-lease verbs (NERD032).
func newEnvLeaseCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lease",
		Short: "Take, renew, release or list run leases on environments",
		Long: `A lease gives one run -- a deploy, a test suite, a verify -- exclusive use of
an environment until it releases it. While an environment is leased, every
command that changes it -- env start, apply, stop, purge and delete, instance
add/remove/import, stack add/remove, bom import -- refuses anyone who does not
present the lease's token (--lease-token, or NSCTL_LEASE_TOKEN), so concurrent
sessions cannot deploy over each other. --ignore-lease overrides the refusal.

A run lease covers one run: take it for the run, release it when the run ends.
A small pool of environments ([pool] in nsctl.toml; by default local plus one
cc-N created on demand) then serves many runs, and a run that finds them all
busy can queue with --wait.

A session lease (acquire --session) covers a whole working session -- one
person or one coding agent iterating on one environment for hours. It lives for
[pool] session_ttl (default 8h) between heartbeats, watches the session's own
process, and answers any run lease taken inside the session, so scripts that
take run leases work unchanged. Only release --session ends it.

A lease ends when it is released, when its TTL passes without a renew, or when
the process it watches (--pid) exits.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newEnvLeaseAcquireCommand(opts),
		newEnvLeaseRenewCommand(opts),
		newEnvLeaseReleaseCommand(opts),
		newEnvLeaseListCommand(opts),
		newEnvLeaseHeartbeatCommand(opts),
		newEnvLeaseWhoamiCommand(opts),
	)
	return cmd
}

func newEnvLeaseAcquireCommand(opts *Options) *cobra.Command {
	var fromPool, wait, steal, asJSON, session, shell, noStart, noPull, keepRunning bool
	var holder, forPath, runID string
	var planner fromRepo
	shape := sessionShape{shared: &planner}
	var ttl time.Duration
	var pid int
	cmd := &cobra.Command{
		Use:   "acquire [name]",
		Short: "Lease an environment, or the closest free one from the pool",
		Long: `Leases the named environment (default: the one HMD_LOCAL_ENV or the registry
names), or with --pool the free pool environment that needs the least redeploying
for --for, an environment manifest of what the run will deploy. Ties go to the
environment released most recently.

When the pool has room and nothing is free, the next cc-N is registered for the
run; the output says so ("created"), and it must be started before deploying.

With --session the lease is held for a working session rather than one run
(NERD035 SPEC002): its TTL is [pool] session_ttl (default 8h), and --pid
defaults to the session's process -- the nearest ancestor named claude, else
the caller's parent. --shell prints the two exports that point every later
nsctl, hmd deploy --local and hmd bender in the shell at the leased environment.

With --template and --repo a session acquire shapes and brings up its
environment (NERD035 SPEC004, SPEC005): it composes the template and the
repositories being edited -- before taking any lease, so a composition that
cannot be built costs nothing -- then, with --pool, places the session in the
free environment of the same template that needs the least redeploying,
avoiding one holding another session's working trees. After printing the lease
it fetches what the composition needs (unless --no-pull), writes it as the
environment's manifest and runs env start (unless --no-start), with progress on
stderr. If that fails the lease is kept: fix it and run env start.

Inside a session (NSCTL_LEASE_TOKEN names a live session lease), an acquire --
bare, naming the session's environment, or with --pool -- is answered with the
session's own lease, marked nested, instead of contending for it.

Prints the lease, including the token that renew, release and leased commands
(NSCTL_LEASE_TOKEN) present.`,
		Example: `  eval "$(nsctl env lease acquire --session --pool --wait --shell \
      --template telemetry --repo ~/src/hmd-inf-clickhouse)"
  eval "$(nsctl env lease acquire --session --pool --wait --shell)"
  nsctl env lease acquire --pool --wait --holder my-session --json
  nsctl env lease acquire dev --holder ci-123 --ttl 30m
  nsctl env lease acquire --pool --for run-manifest.yaml --json`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromPool && len(args) == 1 {
				return nserr.New(nserr.Usage, "name an environment or pass --pool, not both")
			}
			if steal && fromPool {
				return nserr.New(nserr.Usage, "--steal takes a named environment, not --pool")
			}
			if shell && asJSON {
				return nserr.New(nserr.Usage, "--shell and --json are two output formats; pass one")
			}
			if keepRunning && !session {
				return nserr.New(nserr.Usage, "--keep-running is about a session's end; pass --session")
			}
			if shape.requested() && !session {
				return nserr.New(nserr.Usage, "--template and --repo shape a session's environment; pass --session")
			}
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if holder == "" {
				holder = defaultHolder()
			}
			// Resolved here, not at registration, so the default never bakes
			// whichever process built the command tree into help or the docs.
			if !cmd.Flags().Changed("pid") {
				pid = os.Getppid()
				if session {
					pid = lease.SessionPID(pid, lease.ProcParent)
				}
			}
			r := lease.Request{Holder: holder, RunID: runID, PID: pid, TTL: ttl, Steal: steal}
			if session {
				r.Scope = lease.ScopeSession
				r.KeepRunning = keepRunning
				if !cmd.Flags().Changed("ttl") {
					r.TTL = sessionTTL(home, opts)
				}
			}
			// Composed before any lease is taken: a composition that cannot be
			// built must cost no lease and start nothing.
			var composed *composition
			if session && shape.requested() {
				composed, err = composeSession(opts, home, "session", &shape)
				if err != nil {
					return err
				}
				r.Template = shape.template
				for _, path := range shape.repos {
					abs, err := filepath.Abs(path)
					if err != nil {
						return nserr.Wrap(nserr.Usage, err)
					}
					r.Repos = append(r.Repos, abs)
				}
			}
			store := leaseStore(cmd, opts, home)
			render := func(l *lease.Lease) error {
				if err := renderLease(cmd.OutOrStdout(), l, asJSON, shell, composed != nil && !noStart); err != nil {
					return err
				}
				if composed == nil {
					return nil
				}
				return bringUp(cmd, opts, home, l.Env, composed, noPull, noStart)
			}

			// A run inside a session is answered with the session: contending
			// for the environment the session already holds could only fail.
			var mine *lease.Lease
			if !session && !steal {
				mine, err = store.ByToken(opts.Lookup("NSCTL_LEASE_TOKEN"))
				if err != nil {
					return leaseError(err)
				}
				if mine != nil && !mine.IsSession() {
					mine = nil
				}
			}
			if mine != nil && (fromPool || len(args) == 0) {
				mine.Nested = true
				return render(mine)
			}

			// The running budget (NERD035 SPEC008) is about environments a
			// session acquire starts, so it applies only when this one will.
			budget := 0
			if session && composed != nil && !noStart {
				budget = maxRunning(home, opts)
			}
			var p *lease.Pool
			if fromPool {
				p, err = envPool(home, opts, forPath)
				if err != nil {
					return err
				}
				if composed != nil {
					p.Score = sessionScore(home, opts, composed.Manifest)
				}
				if budget > 0 {
					p.MaxRunning, p.Running = budget, poolRunning(cmd, opts, home)
				}
			} else {
				reg, err := registry.Load(home, opts.Lookup)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				var name string
				if len(args) == 1 {
					name = args[0]
				}
				env, err := reg.Environment(name, opts.Lookup)
				if err != nil {
					return nserr.Wrap(nserr.Usage, err)
				}
				if mine != nil && mine.Env == env.Slug {
					mine.Nested = true
					return render(mine)
				}
				if steal || (!wait && budget == 0) {
					l, err := store.Acquire(env.Slug, r)
					if err != nil {
						return leaseError(err)
					}
					if l.Stole != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: took %s from %s (%s)\n",
							env.Slug, l.Stole.Holder, l.Stole.Where())
					}
					return render(l)
				}
				// Waiting for one named environment is a pool of one, so it
				// queues in turn with everyone else. Under a budget it is placed
				// the same way, counting the whole pool's running against it.
				p = &lease.Pool{Members: []string{env.Slug}, Size: 1, Registered: registeredNames(home, opts)}
				if budget > 0 {
					whole, err := envPool(home, opts, "")
					if err != nil {
						return err
					}
					p.MaxRunning, p.Running, p.Counted = budget, poolRunning(cmd, opts, home), whole.Candidates()
				}
			}

			lastAhead := -1
			l, err := store.AcquireFromPool(cmd.Context(), p, r, wait, func(ahead int) {
				if ahead != lastAhead {
					fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for an environment: %d run(s) ahead\n", ahead)
					lastAhead = ahead
				}
			})
			if err != nil {
				return leaseError(err)
			}
			return render(l)
		},
	}
	cmd.Flags().BoolVar(&fromPool, "pool", false, "lease the closest free environment from the pool")
	cmd.Flags().BoolVar(&wait, "wait", false, "queue until an environment is free instead of failing")
	cmd.Flags().BoolVar(&steal, "steal", false, "take the environment even if someone else holds it")
	cmd.Flags().StringVar(&holder, "holder", "", "who is asking, shown to anyone refused (default: user and parent pid)")
	cmd.Flags().StringVar(&runID, "run-id", "", "an id for this run, recorded in the lease")
	cmd.Flags().StringVar(&forPath, "for", "", "an environment manifest of what the run will deploy, to pick the closest pool environment")
	cmd.Flags().DurationVar(&ttl, "ttl", lease.DefaultTTL, "how long the lease lives without a renew (with --session: [pool] session_ttl)")
	cmd.Flags().IntVar(&pid, "pid", 0, "the process whose exit ends the lease (default: the caller's parent; with --session, the session's process); 0 relies on --ttl alone")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the lease as JSON")
	cmd.Flags().BoolVar(&session, "session", false, "hold the environment for a working session, not one run")
	cmd.Flags().BoolVar(&shell, "shell", false, "print export lines for HMD_LOCAL_ENV and NSCTL_LEASE_TOKEN, for eval")
	shape.bind(cmd)
	cmd.Flags().BoolVar(&keepRunning, "keep-running", false, "with --session, do not stop the environment when the session ends")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "with --template/--repo, write the session's manifest but do not start the environment")
	cmd.Flags().BoolVar(&noPull, "no-pull", false, "with --template/--repo, do not fetch the artifacts the composition names")
	cmd.Flags().StringSliceVar(&planner.profiles, "profile", nil, "with --repo, local profiles to activate in every repository. Repeatable, or comma-separated")
	cmd.Flags().BoolVar(&planner.allProfiles, "all-profiles", false, "with --repo, activate every profile each lock mentions")
	cmd.Flags().BoolVar(&planner.lean, "lean", false, "with --repo, activate no profiles")
	cmd.Flags().StringArrayVar(&planner.names, "name", nil, "with --repo, name one instance, as <role-or-declared-name>=<instance>. Repeatable")
	return cmd
}

func newEnvLeaseRenewCommand(opts *Options) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:           "renew <name> --token <token>",
		Short:         "Push a lease's expiry out by its TTL",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			return leaseError(leaseStore(cmd, opts, home).Renew(args[0], tokenOrEnv(token, opts)))
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
	return cmd
}

func newEnvLeaseReleaseCommand(opts *Options) *cobra.Command {
	var token string
	var session, keepRunning bool
	cmd := &cobra.Command{
		Use:   "release <name> --token <token>",
		Short: "Give a leased environment back",
		Long: `Ends the lease the token holds on the environment.

A session lease is ended only with --session. A run inside a session is handed
the session's own token, so a script written for run leases releasing "its"
lease would otherwise end the session; without --session such a release leaves
the lease in place, says so, and exits zero.

Ending a session stops its environment (NERD035 SPEC006), keeping its state for
the next session to reuse warm; --keep-running leaves it running. Nothing is
ever purged here: that is env purge.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if keepRunning && !session {
				return nserr.New(nserr.Usage, "--keep-running is about ending a session; pass --session")
			}
			store := leaseStore(cmd, opts, home)
			if session {
				err = store.ReleaseSession(args[0], tokenOrEnv(token, opts), keepRunning)
			} else {
				err = store.Release(args[0], tokenOrEnv(token, opts))
			}
			if errors.Is(err, lease.ErrSessionLease) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"note: %s is held by a session lease, which a run does not end; the session keeps it. "+
						"Pass --session to end the session.\n", args[0])
				return nil
			}
			if err != nil {
				return leaseError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Released %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
	cmd.Flags().BoolVar(&session, "session", false, "end a session lease, not just a run's use of it")
	cmd.Flags().BoolVar(&keepRunning, "keep-running", false, "with --session, leave the environment running instead of stopping it")
	return cmd
}

func newEnvLeaseListCommand(opts *Options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "Show who holds which environment, and who is waiting",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			store := leaseStore(cmd, opts, home)
			leases, waiters, err := store.List()
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			// Free pool environments and how long they have sat, so a person
			// can see when `env purge --idle` is worth running (NERD035
			// SPEC007). Best effort: a listing never fails over it.
			idle, _ := idlePool(opts, home, store)
			out := cmd.OutOrStdout()
			if asJSON {
				// Tokens are bearer credentials for the lease; a listing
				// never repeats them.
				for i := range leases {
					leases[i].Token = ""
				}
				doc := struct {
					Leases  []lease.Lease  `json:"leases"`
					Waiting []lease.Waiter `json:"waiting"`
					Idle    []idleEnv      `json:"idle"`
				}{Leases: nonNil(leases), Waiting: nonNil(waiters), Idle: nonNil(idle)}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			now := time.Now()
			if len(leases) == 0 && len(waiters) == 0 {
				fmt.Fprintln(out, "No environment is leased.")
			}
			for _, l := range leases {
				scope := "run"
				if l.IsSession() {
					scope = "session"
				}
				fmt.Fprintf(out, "%-12s %-7s %s (%s), held %s, expires in %s%s\n", l.Env, scope, l.Holder, l.Where(),
					now.Sub(l.Acquired).Round(time.Second), l.Expires().Sub(now).Round(time.Second), shapedBy(&l))
			}
			for i, w := range waiters {
				fmt.Fprintf(out, "waiting #%d  %s, for %s, queued %s ago\n", i+1, w.Holder,
					strings.Join(w.Candidates, "|"), now.Sub(w.Enqueued).Round(time.Second))
			}
			for _, e := range idle {
				fmt.Fprintf(out, "%-12s %-7s %s\n", e.Slug, "free", describeIdle(e, now))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

// envPool builds the pool from nsctl.toml, the registry and, with forPath, the
// manifest of what the run will deploy.
func envPool(home string, opts *Options, forPath string) (*lease.Pool, error) {
	cfg, err := nsconfig.Load(home, opts.Lookup)
	if err != nil && !errors.Is(err, nsconfig.ErrNoConfig) {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	conf := cfg.EnvPool()
	p := &lease.Pool{
		Members:    conf.Members,
		Size:       conf.Size,
		Registered: registeredNames(home, opts),
		Create: func(name string) error {
			_, err := registry.Update(home, opts.Lookup, "env lease: add "+name, func(r *registry.Registry) error {
				_, err := r.NewEnvironment(home, name, opts.Lookup)
				return err
			})
			return err
		},
	}
	if forPath != "" {
		want, err := manifest.LoadFile(forPath, opts.Lookup)
		if err != nil {
			return nil, nserr.Wrap(nserr.Usage, err)
		}
		p.Score = func(env string) int {
			have, err := manifest.Load(home, env, opts.Lookup)
			if err != nil {
				return len(want.Repos) + 1
			}
			return manifestDistance(want, have)
		}
	}
	return p, nil
}

// manifestDistance counts the instances want declares that have does not
// declare identically: the instances a run would have to (re)deploy there.
func manifestDistance(want, have *manifest.Manifest) int {
	n := 0
	for _, w := range want.Repos {
		var h manifest.Repo
		ok := false
		if have != nil {
			h, ok = have.Repo(w.InstanceName)
		}
		if !ok || h.RepoClassName != w.RepoClassName || h.Version != w.Version ||
			h.SourceType() != w.SourceType() || sourcePath(h) != sourcePath(w) {
			n++
		}
	}
	return n
}

func sourcePath(r manifest.Repo) string {
	if r.Source == nil {
		return ""
	}
	return r.Source.Path
}

func registeredNames(home string, opts *Options) func() ([]string, error) {
	return func() ([]string, error) {
		reg, err := registry.Load(home, opts.Lookup)
		if err != nil {
			return nil, err
		}
		return reg.Names(), nil
	}
}

func defaultHolder() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "nsctl"
	}
	return fmt.Sprintf("%s/pid-%d", user, os.Getppid())
}

func tokenOrEnv(token string, opts *Options) string {
	if token != "" {
		return token
	}
	return opts.Lookup("NSCTL_LEASE_TOKEN")
}

// stopEnvironment is what `env stop` runs, replaceable in tests.
var stopEnvironment = func(ctx context.Context, opts *Options, home, slug string, out io.Writer) error {
	return environment.Stop(ctx, &environment.Options{Home: home, Lookup: opts.Lookup, Out: out, Err: out}, slug)
}

// leaseStore is the lease store every command uses: one that stops an
// environment when a session lease on it ends (NERD035 SPEC006), in whichever
// nsctl process notices -- a release, or anything that reads the leases and
// finds a session expired or its process gone. The stop's progress goes to
// stderr, so it never mixes into output a script reads.
func leaseStore(cmd *cobra.Command, opts *Options, home string) *lease.Store {
	s := lease.New(home)
	s.OnSessionEnd = func(env string) error {
		out := cmd.ErrOrStderr()
		fmt.Fprintf(out, "A session on %s ended; stopping it (its state is kept for the next session).\n", env)
		err := stopEnvironment(cmd.Context(), opts, home, env, out)
		if err != nil {
			fmt.Fprintf(out, "warning: stopping %s after its session ended: %v\n", env, err)
		}
		return err
	}
	return s
}

// startEnvironment is what `env start` runs, replaceable in tests.
var startEnvironment = startWithControlPlane

// bringUp is a session acquire's second half (NERD035 SPEC005): fetch what the
// composition needs, write it as slug's manifest, and start the environment.
// The lease is already printed and stays held whatever happens here, so a
// failure says how to retry rather than giving the environment up.
func bringUp(cmd *cobra.Command, opts *Options, home, slug string, c *composition, noPull, noStart bool) error {
	// stdout carries the lease alone -- with --shell, eval runs it -- so
	// everything from here, the planner's and the puller's output included,
	// goes to stderr.
	out := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()
	cmd.SetOut(out)
	defer cmd.SetOut(stdout)

	m := c.Manifest
	m.Name = slug
	path := manifest.Find(home, slug, opts.Lookup)
	if path == "" {
		path = manifest.DefaultPath(home, slug)
	}
	if m.Substrate == "" {
		// The environment's recorded mode (NERD014) is the environment's, not
		// the template's to clear.
		if existing, err := manifest.Load(home, slug, opts.Lookup); err == nil && existing != nil {
			m.Substrate = existing.Substrate
		}
	}
	if missing := c.missingArtifacts(home); len(missing) > 0 {
		if noPull {
			fmt.Fprintf(out, "warning: %d artifact(s) are not cached and --no-pull was given; starting %s will refuse until they are\n",
				len(missing), slug)
		} else {
			var libs librarians
			if err := pullMissing(cmd, opts, &libs, home, missing); err != nil {
				return retryable(slug, err)
			}
		}
	}
	if err := m.Save(path); err != nil {
		return retryable(slug, nserr.Wrap(nserr.Fail, err))
	}
	for _, note := range c.Notes {
		fmt.Fprintf(out, "note: %s\n", note)
	}
	fmt.Fprintf(out, "Wrote %s's manifest to %s\n", slug, path)
	if noStart {
		fmt.Fprintf(out, "Start it with `nsctl env start %s`.\n", slug)
		return nil
	}
	if err := startEnvironment(cmd.Context(), opts, home, slug, startOptions{Out: out, Err: out}); err != nil {
		return retryable(slug, err)
	}
	return nil
}

// retryable is a bring-up failure that keeps the error's exit code and says
// the lease is still held.
func retryable(slug string, err error) error {
	code := nserr.CodeOf(err)
	if code == nserr.OK {
		code = nserr.Fail
	}
	return nserr.New(code, "bringing up %s: %v\nThe lease is still yours. Fix the cause and run `nsctl env start %s`.",
		slug, err, slug)
}

// sessionScore is how far env is from a session's composition (NERD035
// SPEC005), lowest first: another template costs 1000, so a same-template
// environment always wins; then the instances to (re)deploy; then one for
// each working tree another session left declared there.
func sessionScore(home string, opts *Options, want *manifest.Manifest) func(string) int {
	return func(env string) int {
		have, err := manifest.Load(home, env, opts.Lookup)
		if err != nil {
			return 1000 + len(want.Repos) + 1
		}
		score := manifestDistance(want, have) + staleTrees(want, have)
		template := ""
		if have != nil {
			template = have.Template
		}
		if template != want.Template {
			score += 1000
		}
		return score
	}
}

// staleTrees counts the instances have deploys from a working tree that want
// does not declare: another session's, left deployed.
func staleTrees(want, have *manifest.Manifest) int {
	if have == nil {
		return 0
	}
	n := 0
	for _, r := range have.Repos {
		if r.Source == nil || r.Source.Type != manifest.SourceLocal {
			continue
		}
		if _, declared := want.Repo(r.InstanceName); !declared {
			n++
		}
	}
	return n
}

// maxRunning is [pool] max_running, or 0 -- no cap -- when nsctl.toml is
// absent or unreadable.
func maxRunning(home string, opts *Options) int {
	cfg, err := nsconfig.Load(home, opts.Lookup)
	if err != nil {
		cfg = nil
	}
	return cfg.EnvPool().MaxRunning
}

// poolRunning reports which registered environments have a container up, for
// the running budget. It asks the container engine, so the pool calls it
// outside the lease lock.
func poolRunning(cmd *cobra.Command, opts *Options, home string) func() map[string]bool {
	return func() map[string]bool {
		out := map[string]bool{}
		reg, err := registry.Load(home, opts.Lookup)
		if err != nil {
			return out
		}
		for _, name := range reg.Names() {
			env, err := reg.Environment(name, opts.Lookup)
			if err != nil {
				continue
			}
			if len(runningContainers(cmd.Context(), env)) > 0 {
				out[name] = true
			}
		}
		return out
	}
}

// sessionTTL is [pool] session_ttl, or its default when nsctl.toml is absent
// or unreadable -- acquire must not fail over a file only this value comes from.
func sessionTTL(home string, opts *Options) time.Duration {
	cfg, err := nsconfig.Load(home, opts.Lookup)
	if err != nil {
		cfg = nil
	}
	return cfg.EnvPool().SessionTTLOrDefault()
}

// shapedBy is the template and working trees a session lease records, for one
// line of a listing.
func shapedBy(l *lease.Lease) string {
	var parts []string
	if l.Template != "" {
		parts = append(parts, "template "+l.Template)
	}
	if len(l.Repos) > 0 {
		parts = append(parts, "repos "+strings.Join(l.Repos, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

func newEnvLeaseHeartbeatCommand(opts *Options) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "heartbeat [--token <token>]",
		Short: "Renew the lease a token holds, without naming its environment",
		Long: `Pushes the expiry of the lease the token holds out by its TTL. The token
identifies the lease, so a hook or a background loop needs nothing else.`,
		Example:       `  while sleep 600; do nsctl env lease heartbeat || break; done`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			l, err := leaseStore(cmd, opts, home).Heartbeat(tokenOrEnv(token, opts))
			if err != nil {
				return leaseError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Renewed %s until %s\n", l.Env, l.Expires().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
	return cmd
}

func newEnvLeaseWhoamiCommand(opts *Options) *cobra.Command {
	var token string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the lease this session holds",
		Long: `Describes the lease NSCTL_LEASE_TOKEN (or --token) holds: its environment,
scope, holder, template, working trees, expiry and where the environment's
routes are served. Exits non-zero when the token holds no live lease.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			t := tokenOrEnv(token, opts)
			if t == "" {
				return nserr.New(nserr.Usage, "no lease token: set NSCTL_LEASE_TOKEN or pass --token")
			}
			l, err := leaseStore(cmd, opts, home).ByToken(t)
			if err != nil {
				return leaseError(err)
			}
			if l == nil {
				return nserr.New(nserr.Usage, "this token holds no live lease: it expired, was released or was taken over")
			}
			l.Token = ""
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(l)
			}
			scope := "run"
			if l.IsSession() {
				scope = "session"
			}
			fmt.Fprintf(out, "environment  %s\n", l.Env)
			fmt.Fprintf(out, "scope        %s\n", scope)
			fmt.Fprintf(out, "holder       %s (%s)\n", l.Holder, l.Where())
			if l.Template != "" {
				fmt.Fprintf(out, "template     %s\n", l.Template)
			}
			for i, r := range l.Repos {
				label := ""
				if i == 0 {
					label = "repos"
				}
				fmt.Fprintf(out, "%-12s %s\n", label, r)
			}
			fmt.Fprintf(out, "expires      %s\n", l.Expires().Format(time.RFC3339))
			fmt.Fprintf(out, "routes       %s/<service>/\n", hosturl.Route(l.Env))
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the lease as JSON, without its token")
	return cmd
}

// leaseGuard is what every command that changes an environment carries
// (NERD035 SPEC001): the holder's token to get past a lease, and the override.
type leaseGuard struct {
	token  string
	ignore bool
}

func (g *leaseGuard) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&g.token, "lease-token", "",
		"The token of the lease held on the environment (default: NSCTL_LEASE_TOKEN)")
	cmd.Flags().BoolVar(&g.ignore, "ignore-lease", false,
		"Proceed even though someone else holds the environment's lease")
}

// check refuses unless slug is unleased or the caller holds its lease. It runs
// before the command changes anything, so a refusal costs nothing.
func (g *leaseGuard) check(cmd *cobra.Command, opts *Options, home, slug string) error {
	err := leaseStore(cmd, opts, home).Check(slug, tokenOrEnv(g.token, opts))
	var held *lease.HeldError
	if g.ignore && errors.As(err, &held) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: ignoring the lease %s holds on %s (%s, until %s)\n",
			held.Lease.Holder, slug, held.Lease.Where(), held.Lease.Expires().Format(time.RFC3339))
		return nil
	}
	return leaseError(err)
}

// checkName is check for a name as the user gave it; empty means the default.
//
// A name that does not resolve is left to the command, whose own error about
// it is the better one -- and an environment that is not registered has no
// lease to protect.
func (g *leaseGuard) checkName(cmd *cobra.Command, opts *Options, home, name string) error {
	_, _, slug, err := resolveEnvSlug(opts, name)
	if err != nil {
		return nil
	}
	return g.check(cmd, opts, home, slug)
}

// checkAll is check for every registered environment, for a verb that acts on
// all of them at once.
func (g *leaseGuard) checkAll(cmd *cobra.Command, opts *Options, home string) error {
	names, err := registeredNames(home, opts)()
	if err != nil {
		return nil
	}
	for _, slug := range names {
		if err := g.check(cmd, opts, home, slug); err != nil {
			return err
		}
	}
	return nil
}

func leaseError(err error) error {
	if err == nil {
		return nil
	}
	var held *lease.HeldError
	var busy *lease.PoolBusyError
	switch {
	case errors.As(err, &held), errors.As(err, &busy):
		return nserr.Wrap(nserr.InUse, err)
	case errors.Is(err, lease.ErrNotHolder):
		return nserr.Wrap(nserr.Usage, err)
	}
	return nserr.Wrap(nserr.Fail, err)
}

func renderLease(out io.Writer, l *lease.Lease, asJSON, shell, startsHere bool) error {
	if shell {
		// Nothing but the exports: this is read by eval.
		fmt.Fprintf(out, "export HMD_LOCAL_ENV=%s\nexport NSCTL_LEASE_TOKEN=%s\n", l.Env, l.Token)
		return nil
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(l)
	}
	if l.Nested {
		fmt.Fprintf(out, "Using %s, which this session holds until %s\n", l.Env, l.Expires().Format(time.RFC3339))
	} else {
		fmt.Fprintf(out, "Leased %s to %s until %s\n", l.Env, l.Holder, l.Expires().Format(time.RFC3339))
	}
	if l.Created && !startsHere {
		fmt.Fprintf(out, "%s was added to the pool for this run; start it with `nsctl env start %s`.\n", l.Env, l.Env)
	}
	fmt.Fprintf(out, "Token: %s\n", l.Token)
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
