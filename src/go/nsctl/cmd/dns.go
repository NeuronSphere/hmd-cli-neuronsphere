package cmd

import (
	"fmt"
	"net"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dnsd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// newDNSCommand gives the host the local names, for the things that cannot be
// reached by port.
//
// Most of what a local NeuronSphere serves is now reached at a port and needs
// none of this (NERD025). What is left are the names that must be one string
// from the browser, a sibling container and a cluster pod alike -- the identity
// provider's issuer, the package registry, control-plane extensions -- and for
// those a wildcard resolver replaces an /etc/hosts line per name with one
// arrangement that covers every name, including ones that do not exist yet.
func newDNSCommand(opts *Options) *cobra.Command {
	dnsCmd := &cobra.Command{
		Use:   "dns",
		Short: "Resolve the local NeuronSphere host names on this machine",
		Long: `Make *.` + dnsd.DefaultSuffix + ` resolve on this machine.

Local NeuronSphere serves its user interfaces on published ports, which need no
name resolution at all. A few things cannot work that way: an OIDC issuer, a
package index URL and a control-plane extension's URL are each read by a
browser, by a container and by a cluster pod, and have to be the same string in
all three -- which http://localhost:<port> can never be, because inside a pod
localhost is the pod.

Those names used to cost an /etc/hosts line each, forever, because a hosts file
has no wildcards and so can only name what already exists. A resolver answers
the whole suffix at once, offline, with no public DNS zone involved.

nsctl prints the one privileged step rather than running it.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	dnsCmd.AddCommand(newDNSInstallCommand(), newDNSStatusCommand(), newDNSServeCommand())
	return dnsCmd
}

func newDNSInstallCommand() *cobra.Command {
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
			fmt.Fprintf(out, "Run this once. nsctl does not run it for you: it needs root, and it\n")
			fmt.Fprintf(out, "changes a file that belongs to you.\n\n")
			fmt.Fprintf(out, "    %s\n\n", dnsd.InstallStep(runtime.GOOS, suffix, port))
			fmt.Fprintf(out, "It is scoped to %s, not to neuronsphere.io, so it cannot\n", suffix)
			fmt.Fprintf(out, "capture any name outside the local platform.\n\n")
			fmt.Fprintf(out, "Then check it with `nsctl dns status`.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&suffix, "suffix", dnsd.DefaultSuffix, "DNS suffix to resolve locally")
	cmd.Flags().IntVar(&port, "port", dnsd.DefaultPort, "Port the local resolver listens on")
	return cmd
}

func newDNSStatusCommand() *cobra.Command {
	var suffix string
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Report whether the local host names resolve",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// A name nothing has deployed. Resolving it is the property a hosts
			// file cannot have, so it is the one worth testing.
			probe := "wildcard-probe." + suffix
			ips, err := net.LookupIP(probe)
			if err != nil || len(ips) == 0 {
				fmt.Fprintf(out, "not resolving   %s does not resolve on this machine.\n", suffix)
				fmt.Fprintf(out, "                Run `nsctl dns install` for the one step that fixes it.\n")
				return nil
			}
			fmt.Fprintf(out, "ok              *.%s resolves to %s\n", suffix, ips[0])
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
