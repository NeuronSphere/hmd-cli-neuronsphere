package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/spf13/cobra"
)

// newAuthdCommand is the local identity provider, serving and minting.
//
// Not hidden, unlike `runner`. The runner is a service nobody invokes by hand,
// but `authd token` is the point of the whole thing: it is how a test, a curl
// or a person gets a token with the claims they want to see a policy decide on.
func newAuthdCommand(opts *Options) *cobra.Command {
	authdCmd := &cobra.Command{
		Use:   "authd",
		Short: "The local mock identity provider",
		Long: `A local stand-in for Okta, for testing authorization without one.

It emulates Okta's URL shape because the consumers build those URLs by string
concatenation -- Superset and Airflow from OKTA_BASE_URL, hmd-lib-auth from the
issuer -- so the cloud's own configuration runs locally unmodified.

It signs with a key it generated and will mint a token with any claims asked of
it. That is deliberate: what is being tested locally is what the platform does
with a token's claims, and neither the Rego policies nor the apps' role mapping
verify a signature.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	authdCmd.AddCommand(newAuthdServeCommand(opts), newAuthdTokenCommand(opts))
	return authdCmd
}

func newAuthdServeCommand(opts *Options) *cobra.Command {
	var addr, issuer string
	cmd := &cobra.Command{
		Use:           "serve",
		Short:         "Serve the mock identity provider",
		Hidden:        true,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if issuer == "" {
				issuer = opts.Lookup(authd.IssuerEnv)
			}
			if issuer == "" {
				return nserr.New(nserr.Usage,
					"no issuer base URL. Pass --issuer, or set %s.\n"+
						"  It has to be the one URL that resolves to this server from the host, from a pod "+
						"and from a Floci container alike: a consumer that fetched the keys under one name "+
						"and reads `iss` as another rejects every token.", authd.IssuerEnv)
			}
			key, err := authd.LoadOrCreateKey(authd.KeyDir(home))
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			srv := authd.NewServer(issuer, key)

			httpSrv := &http.Server{
				Addr:              addr,
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)
			go func() {
				fmt.Fprintf(cmd.OutOrStdout(), "authd listening on %s, issuing as %s\n", addr, srv.Issuer)
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
			}()

			select {
			case err := <-errCh:
				return nserr.Wrap(nserr.Fail, err)
			case <-ctx.Done():
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "address to listen on")
	cmd.Flags().StringVar(&issuer, "issuer", "", "base URL this server is reached at")
	return cmd
}

func newAuthdTokenCommand(opts *Options) *cobra.Command {
	var (
		server, subject, audience, clientID string
		groups, scopes, claims              []string
		lifetime                            time.Duration
		decode, showClaims                  bool
	)
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint a token with the claims you name",
		Long: `Prints a signed JWT built from the claims given.

Signed with the same key the running server publishes, and minted locally --
so this works whether or not the control plane is up, which is what makes it
usable from a test.

The groups matter most. They decide a user's role in Superset and Airflow and
every Rego decision about them, and the applications parse them by shape:

    NeuronSphere Airflow Admin - Local (none)
    NeuronSphere <app> <role> - <Environment> (<customer code>)`,
		Example: `  nsctl authd token --server services --sub ms-deployment
  nsctl authd token --group 'NeuronSphere Superset Admin - Local (none)'
  nsctl authd token --claim tenant=acme --scope service
  nsctl authd token --sub alice --show-claims
  nsctl authd token --decode "$TOKEN"`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			if decode {
				return decodeToken(cmd, args)
			}
			if len(args) > 0 {
				return nserr.New(nserr.Usage,
					"a token argument only means something with --decode; to mint one, name its claims with --sub, --group and --claim")
			}
			issuer := opts.Lookup(authd.IssuerEnv)
			if issuer == "" {
				issuer = authd.DefaultIssuerBase
			}
			extra, err := parseClaims(claims)
			if err != nil {
				return err
			}
			key, err := authd.LoadOrCreateKey(authd.KeyDir(home))
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			token, err := authd.Mint(key, issuer, authd.Request{
				Server:          server,
				Subject:         subject,
				Audience:        audience,
				ClientID:        clientID,
				Groups:          groups,
				Scopes:          scopes,
				Claims:          extra,
				LifetimeSeconds: int(lifetime.Seconds()),
			}, time.Now())
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			if showClaims {
				decoded, err := authd.DecodeClaims(token)
				if err != nil {
					return nserr.Wrap(nserr.Fail, err)
				}
				return printJSON(cmd, decoded)
			}
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", authd.ServerNS,
		"authorization server: ns (api://neuronsphere) or services (api://neuronsphere-services)")
	cmd.Flags().StringVar(&subject, "sub", "", "subject; also the default client id")
	cmd.Flags().StringVar(&audience, "aud", "", "override the server's audience")
	cmd.Flags().StringVar(&clientID, "client-id", "", "client id, when it differs from the subject")
	cmd.Flags().StringArrayVar(&groups, "group", nil, "a groups claim entry; repeatable")
	cmd.Flags().StringArrayVar(&scopes, "scope", nil, "a scp claim entry; repeatable")
	cmd.Flags().StringArrayVar(&claims, "claim", nil, "an extra claim as key=value; repeatable, JSON values allowed")
	cmd.Flags().DurationVar(&lifetime, "lifetime", authd.DefaultLifetime, "how long the token is valid")
	cmd.Flags().BoolVar(&showClaims, "show-claims", false, "print the claim set instead of the token")
	cmd.Flags().BoolVar(&decode, "decode", false, "decode a token given on stdin instead of minting one")
	return cmd
}

// decodeToken prints the claims of a token on stdin.
//
// Unverified, and says so where it matters: this is for reading what a token
// carries, not for deciding anything. It is here because the alternative during
// a debugging session is a base64 pipeline that everyone reinvents wrong.
func decodeToken(cmd *cobra.Command, args []string) error {
	var raw []byte
	var err error
	if len(args) > 0 {
		raw = []byte(args[0])
	} else {
		raw, err = io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nserr.Wrap(nserr.Fail, err)
		}
	}
	claims, err := authd.DecodeClaims(strings.TrimSpace(string(raw)))
	if err != nil {
		return nserr.Wrap(nserr.Usage, err)
	}
	return printJSON(cmd, claims)
}

// parseClaims turns key=value pairs into claims, parsing the value as JSON when
// it is valid JSON.
//
// So --claim 'groups=["a","b"]' is a list and --claim tenant=acme is a string,
// without a second flag to say which. A policy that reads a numeric or boolean
// claim is otherwise untestable from the command line.
func parseClaims(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return nil, nserr.New(nserr.Usage, "--claim %q is not key=value", pair)
		}
		var parsed any
		if err := json.Unmarshal([]byte(value), &parsed); err == nil {
			out[key] = parsed
		} else {
			out[key] = value
		}
	}
	return out, nil
}

func printJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
