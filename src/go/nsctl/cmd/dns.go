package cmd

import (
	"fmt"
	"net"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
	"github.com/spf13/cobra"
)

// newDNSCommand gives the host the local names.
//
// Every user interface is reached this way -- NERD025 SPEC001 briefly served
// them on ports instead and was withdrawn -- along with the names that must be
// one string from a browser, a sibling container and a cluster pod alike: the
// identity provider's issuer, the package registry, control-plane extensions. A
// wildcard resolver replaces an /etc/hosts line per name with one arrangement
// that covers every name, including ones that do not exist yet.
func newDNSCommand(opts *Options) *cobra.Command {
	dnsCmd := &cobra.Command{
		Use:   "dns",
		Short: "Resolve the local NeuronSphere host names on this machine",
		Long: `Make *.` + dnsd.DefaultSuffix + ` resolve on this machine.

This is how a local platform is reached by name: a user interface at
<instance>.` + dnsd.DefaultSuffix + `, the identity provider's issuer, the
package index, a control-plane extension. Starting a platform and deploying to
it need none of it -- nsctl dials Floci's own hostnames on loopback itself -- so
nothing here is required until you want to open something.

Some of these could never have been a port. An OIDC issuer, a package index URL
and an extension's URL are each read by a browser, by a container and by a
cluster pod, and have to be the same string in all three -- which
http://localhost:<port> can never be, because inside a pod localhost is the pod.

Those names used to cost an /etc/hosts line each, forever, because a hosts file
has no wildcards and so can only name what already exists. A resolver answers
the whole suffix at once, offline, with no public DNS zone involved.

nsctl prints the one privileged step rather than running it.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	dnsCmd.AddCommand(newDNSInstallCommand(opts), newDNSStatusCommand(opts), newDNSServeCommand())
	return dnsCmd
}

// resolvedDNSPort is the port the resolver is actually served on for this home.
//
// The port is chosen when 19153 is already held (NERD025 SPEC008), and the
// printed step has to name the chosen one: a resolver file pointing at a port
// nothing listens on fails silently and adds latency to every lookup in the
// suffix. Precedence is the user's own statement, then what this home recorded,
// then the historical default -- and a home that cannot be read is not an error
// here, because `dns install` is informational and must work anywhere.
func resolvedDNSPort(opts *Options) int {
	if raw := opts.Lookup(dnsd.PortEnv); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	if opts.Home != "" {
		if reg, err := registry.Load(opts.Home, opts.Lookup); err == nil {
			if p := reg.ControlPlane.Port(registry.PortDNS); p > 0 {
				return p
			}
		}
	}
	return dnsd.DefaultPort
}

// installText is everything `dns install` prints.
//
// Split out and given its platform so both branches can be asserted from a unit
// test on either machine. The contract suite can only check the platform it runs
// on, and asserting a macOS resolver-file path there passed locally and failed
// the release on Linux, where the step is a systemd-resolved routing domain.
func installText(goos, suffix string, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run this once. nsctl does not run it for you: it needs root, and it\n")
	fmt.Fprintf(&b, "changes a file that belongs to you.\n\n")
	fmt.Fprintf(&b, "    %s\n\n", dnsd.InstallStep(goos, suffix, port))
	// The subtree it must NOT be scoped to, named from the suffix rather than
	// written down: a resolver captures everything below the name it is filed
	// under, so `local` would swallow every mDNS name on this machine exactly as
	// `neuronsphere.io` would once have swallowed the public site.
	fmt.Fprintf(&b, "It is scoped to %s, not to %s, so it cannot\n", suffix, dnsd.ParentOf(suffix))
	fmt.Fprintf(&b, "capture any name outside the local platform.\n\n")
	fmt.Fprintf(&b, "Then check it with `nsctl dns status`.\n")
	return b.String()
}

func newDNSInstallCommand(opts *Options) *cobra.Command {
	var suffix string
	var port int
	cmd := &cobra.Command{
		Use:           "install",
		Short:         "Print the one step that points this machine at the local resolver",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// 0 means the flag was not given: resolve it from this home.
			if port == 0 {
				port = resolvedDNSPort(opts)
			}
			fmt.Fprint(out, installText(runtime.GOOS, suffix, port))
			return nil
		},
	}
	cmd.Flags().StringVar(&suffix, "suffix", dnsd.DefaultSuffix, "DNS suffix to resolve locally")
	cmd.Flags().IntVar(&port, "port", 0, "Port the local resolver listens on (default: this home's)")
	return cmd
}

func newDNSStatusCommand(opts *Options) *cobra.Command {
	var suffix string
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Report whether the local host names resolve",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// A name nothing has deployed, and a different one every call -- a
			// constant would be cached and keep reporting success after the
			// resolver stopped. See dnsd.ProbeName.
			probe := dnsd.ProbeName(suffix)
			port := resolvedDNSPort(opts)
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

			// Two independent facts, because they have opposite fixes: does the
			// resolver answer at all, and does this machine route the suffix to
			// it. One message for both would tell a user whose control plane is
			// down to edit a file that was already correct (NERD026 SPEC003).
			running := dnsd.Answers(cmd.Context(), addr, probe) == nil
			ips, err := net.LookupIP(probe)
			resolving := err == nil && len(ips) > 0

			switch {
			case resolving && running:
				fmt.Fprintf(out, "ok              *.%s resolves to %s, through the resolver on %s\n",
					suffix, ips[0], addr)
			case resolving && !running:
				fmt.Fprintf(out, "degraded        %s resolves, but not through the local resolver:\n", probe)
				fmt.Fprintf(out, "                nothing answers on %s. The name is coming from somewhere\n", addr)
				fmt.Fprintf(out, "                else -- an /etc/hosts line, or a stale cache -- so names that\n")
				fmt.Fprintf(out, "                have not been deployed yet will not resolve.\n")
				fmt.Fprintf(out, "                Start it with `nsctl control-plane start`.\n")
			case !resolving && running:
				fmt.Fprintf(out, "not installed   the resolver is running on %s and answers for %s,\n", addr, probe)
				fmt.Fprintf(out, "                but this machine does not route %s to it.\n", suffix)
				fmt.Fprintf(out, "                Run `nsctl dns install` for the one step that fixes it.\n")
			default:
				fmt.Fprintf(out, "not running     nothing answers on %s, and %s does not\n", addr, probe)
				fmt.Fprintf(out, "                resolve on this machine either.\n")
				fmt.Fprintf(out, "                Start the resolver with `nsctl control-plane start`, then run\n")
				fmt.Fprintf(out, "                `nsctl dns install` if the name still does not resolve.\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&suffix, "suffix", dnsd.DefaultSuffix, "DNS suffix to check")
	return cmd
}

// newDNSServeCommand runs the resolver. Hidden: it is a service, started in a
// container by the control plane, not something anyone invokes by hand.
func newDNSServeCommand() *cobra.Command {
	var addr, suffix string
	cmd := &cobra.Command{
		Use:           "serve",
		Short:         "Serve the wildcard resolver",
		Hidden:        true,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.ErrOrStderr(), "resolving *.%s on %s\n", suffix, addr)
			if err := dnsd.New(suffix).ListenAndServe(ctx, addr); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", fmt.Sprintf(":%d", dnsd.DefaultPort), "UDP address to listen on")
	cmd.Flags().StringVar(&suffix, "suffix", dnsd.DefaultSuffix, "DNS suffix to answer for")
	return cmd
}
