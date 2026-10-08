package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
command that changes it -- env start, apply, stop, purge and delete, repo
add/remove/import, stack add/remove, bom import -- refuses anyone who does not
present the lease's token (--lease-token, or NSCTL_LEASE_TOKEN), so concurrent
sessions cannot deploy over each other. --ignore-lease overrides the refusal.

Leases are run-scoped, not session-scoped: take one for the run, release it
when the run ends. A small pool of environments ([pool] in nsctl.toml; by
default local plus one cc-N created on demand) then serves many sessions, and a
run that finds them all busy can queue with --wait.

A lease ends when it is released, when its TTL passes without a renew, or when
the process it watches (--pid, by default the caller's parent) exits.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newEnvLeaseAcquireCommand(opts),
		newEnvLeaseRenewCommand(opts),
		newEnvLeaseReleaseCommand(opts),
		newEnvLeaseListCommand(opts),
	)
	return cmd
}

func newEnvLeaseAcquireCommand(opts *Options) *cobra.Command {
	var fromPool, wait, steal, asJSON bool
	var holder, forPath, runID string
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

Prints the lease, including the token that renew, release and leased commands
(NSCTL_LEASE_TOKEN) present.`,
		Example: `  nsctl env lease acquire --pool --wait --holder my-session --json
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
			}
			r := lease.Request{Holder: holder, RunID: runID, PID: pid, TTL: ttl, Steal: steal}
			store := lease.New(home)

			var p *lease.Pool
			if fromPool {
				p, err = envPool(home, opts, forPath)
				if err != nil {
					return err
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
				if steal || !wait {
					l, err := store.Acquire(env.Slug, r)
					if err != nil {
						return leaseError(err)
					}
					if l.Stole != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: took %s from %s (%s)\n",
							env.Slug, l.Stole.Holder, l.Stole.Where())
					}
					return renderLease(cmd.OutOrStdout(), l, asJSON)
				}
				// Waiting for one named environment is a pool of one, so it
				// queues in turn with everyone else.
				p = &lease.Pool{Members: []string{env.Slug}, Size: 1, Registered: registeredNames(home, opts)}
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
			return renderLease(cmd.OutOrStdout(), l, asJSON)
		},
	}
	cmd.Flags().BoolVar(&fromPool, "pool", false, "lease the closest free environment from the pool")
	cmd.Flags().BoolVar(&wait, "wait", false, "queue until an environment is free instead of failing")
	cmd.Flags().BoolVar(&steal, "steal", false, "take the environment even if someone else holds it")
	cmd.Flags().StringVar(&holder, "holder", "", "who is asking, shown to anyone refused (default: user and parent pid)")
	cmd.Flags().StringVar(&runID, "run-id", "", "an id for this run, recorded in the lease")
	cmd.Flags().StringVar(&forPath, "for", "", "an environment manifest of what the run will deploy, to pick the closest pool environment")
	cmd.Flags().DurationVar(&ttl, "ttl", lease.DefaultTTL, "how long the lease lives without a renew")
	cmd.Flags().IntVar(&pid, "pid", 0, "the process whose exit ends the lease (default: the caller's parent); 0 relies on --ttl alone")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the lease as JSON")
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
			return leaseError(lease.New(home).Renew(args[0], tokenOrEnv(token, opts)))
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
	return cmd
}

func newEnvLeaseReleaseCommand(opts *Options) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:           "release <name> --token <token>",
		Short:         "Give a leased environment back",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if err := lease.New(home).Release(args[0], tokenOrEnv(token, opts)); err != nil {
				return leaseError(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Released %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the lease token (default: NSCTL_LEASE_TOKEN)")
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
			leases, waiters, err := lease.New(home).List()
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
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
				}{Leases: nonNil(leases), Waiting: nonNil(waiters)}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			if len(leases) == 0 && len(waiters) == 0 {
				fmt.Fprintln(out, "No environment is leased.")
				return nil
			}
			now := time.Now()
			for _, l := range leases {
				fmt.Fprintf(out, "%-12s %s (%s), held %s, expires in %s\n", l.Env, l.Holder, l.Where(),
					now.Sub(l.Acquired).Round(time.Second), l.Expires().Sub(now).Round(time.Second))
			}
			for i, w := range waiters {
				fmt.Fprintf(out, "waiting #%d  %s, for %s, queued %s ago\n", i+1, w.Holder,
					strings.Join(w.Candidates, "|"), now.Sub(w.Enqueued).Round(time.Second))
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
	err := lease.New(home).Check(slug, tokenOrEnv(g.token, opts))
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

func renderLease(out io.Writer, l *lease.Lease, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(l)
	}
	fmt.Fprintf(out, "Leased %s to %s until %s\n", l.Env, l.Holder, l.Expires().Format(time.RFC3339))
	if l.Created {
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
