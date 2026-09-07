package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsrunner"
	"github.com/spf13/cobra"
)

// newRunnerCommand is the service half of nsctl, and is hidden.
//
// It is a subcommand rather than a second binary because the work it does is
// internal/runner, which a separate Go module could not import: internal/ is
// scoped per module. Shipping one binary that can also serve costs a command
// nobody types and avoids either duplicating node execution or making it
// public. The container image is this same binary with this subcommand as its
// entrypoint.
func newRunnerCommand(opts *Options) *cobra.Command {
	runnerCmd := &cobra.Command{
		Use:           "runner",
		Short:         "The DAG-runner service",
		Hidden:        true,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	runnerCmd.AddCommand(newRunnerServeCommand(opts))
	return runnerCmd
}

func newRunnerServeCommand(opts *Options) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the DAG-runner API",
		Long: `Runs deployments submitted over HTTP, instead of inside the CLI process.

The API is shaped like the one hmd-ms-deployment already speaks to Argo, so
selecting this runner is a client choice rather than a second protocol.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			srv, err := nsrunner.NewServer(home, opts.Lookup, cmd.OutOrStdout())
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}

			httpSrv := &http.Server{
				Addr:              addr,
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				// No write timeout: a followed log stream stays open for as
				// long as the deployment it is reporting on.
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)
			go func() {
				fmt.Fprintf(cmd.OutOrStdout(), "nsrunner listening on %s (HMD_HOME=%s)\n", addr, home)
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
			}()

			select {
			case err := <-errCh:
				return nserr.Wrap(nserr.Fail, err)
			case <-ctx.Done():
			}

			// Stop listening first, then let in-flight deployments finish, so
			// a restart does not orphan a half-applied changeset.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
			srv.Shutdown(shutdownCtx)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "address to listen on")
	return cmd
}
