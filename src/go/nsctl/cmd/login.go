package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/authd"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/browser"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/deviceauth"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tokenstore"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/tty"
)

// refreshMargin is how long before expiry a token is renewed rather than used.
//
// A token valid for another two seconds will have expired by the time the next
// request is answered, and the round trip that discovers this costs more than
// the refresh would have.
const refreshMargin = 60 * time.Second

// loginDeps are the seams a test replaces. Real ones are nil.
//
// Options-as-argument, for the reason SPEC004 records: every test in this
// module is t.Parallel(), t.Setenv panics under it, and a command whose
// behaviour is reachable only through package state cannot be tested that way.
type loginDeps struct {
	HTTP    *http.Client
	Now     func() time.Time
	OpenURL browser.Runner
	IsTTY   func() bool
	// Sleep replaces the wait between polls. Only a test sets it: RFC 8628
	// floors the interval at five seconds, which is the right cadence against
	// a real server and far too long to pay once per test run.
	Sleep func(context.Context, time.Duration) error
}

// pollOptions is what to pass Poll, which is nothing at all in production.
func (d *loginDeps) pollOptions() []deviceauth.PollOption {
	if d.Sleep == nil {
		return nil
	}
	return []deviceauth.PollOption{deviceauth.WithSleep(d.Sleep)}
}

func (d *loginDeps) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return deviceauth.NewHTTPClient()
}

func (d *loginDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *loginDeps) isTTY(cmd *cobra.Command) bool {
	if d.IsTTY != nil {
		return d.IsTTY()
	}
	return stdinIsTerminal(cmd)
}

// stdinIsTerminal reports whether there is a person to answer a prompt.
// internal/tty holds the reasoning and the one implementation.
func stdinIsTerminal(cmd *cobra.Command) bool {
	return tty.IsTerminal(cmd.InOrStdin())
}

func newLoginCommand(opts *Options) *cobra.Command {
	var (
		profileName, authURL string
		save, noBrowser      bool
		noPrompt, force      bool
		timeout              time.Duration
	)
	deps := &loginDeps{}

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with the device authorization grant",
		Long: `Signs in through the OAuth 2.0 device authorization grant.

nsctl prints a code and a URL; you open the URL in whatever browser you have,
on whatever machine you have, and type the code. Nothing binds a port on this
machine and nothing needs a browser on it, so this works over SSH and inside a
container.

The endpoint comes from a profile in $HMD_HOME/.config/nsctl.toml. With no
configuration and a terminal to answer, nsctl asks for the URL once and writes
the file; with no terminal it refuses and shows what to write.

The token is cached in $HMD_HOME/.cache/tokens.yaml, which is the same file the
Python ` + "`hmd login`" + ` writes and every HMD tool reads.`,
		Example: `  nsctl login
  nsctl login --profile acme
  nsctl login --auth-url https://auth.example-admin-neuronsphere.io/oauth2/ns --save
  nsctl login --no-browser`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd, opts, deps, loginArgs{
				profile:   profileName,
				authURL:   authURL,
				save:      save,
				noBrowser: noBrowser,
				noPrompt:  noPrompt,
				force:     force,
				timeout:   timeout,
			})
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile in nsctl.toml to sign in with")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "authorization server issuer, overriding the profile")
	cmd.Flags().BoolVar(&save, "save", false, "write --auth-url to the profile before signing in")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL without trying to open a browser")
	cmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "never ask for missing configuration; refuse instead")
	cmd.Flags().BoolVar(&force, "force", false, "sign in again even if the cached token is still valid")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "give up waiting for approval after this long")
	return cmd
}

type loginArgs struct {
	profile   string
	authURL   string
	save      bool
	noBrowser bool
	noPrompt  bool
	force     bool
	timeout   time.Duration
}

func runLogin(cmd *cobra.Command, opts *Options, deps *loginDeps, args loginArgs) error {
	home, err := opts.RequireHome()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	profile, err := resolveProfile(cmd, opts, deps, home, args)
	if err != nil {
		return err
	}

	// A credential that still works is not worth a browser. --force is how to
	// get one anyway, which is what a changed group membership needs.
	cached, err := tokenstore.Load(home)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if !args.force && cached.Login.Valid(deps.now()) && !cached.Login.ExpiresWithin(deps.now(), refreshMargin) {
		fmt.Fprintf(out, "Already signed in%s. Use --force to sign in again.\n", describeLogin(cached.Login))
		return nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if args.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, args.timeout)
		defer cancel()
	}

	meta, err := deviceauth.Discover(ctx, deps.http(), profile.AuthURL)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	// Refresh silently before asking for a browser. This is what
	// offline_access is requested for, and the thing the Python login cannot
	// do at all.
	if !args.force && cached.Login.Refreshable() && cached.Login.Profile == profile.Name {
		token, refreshErr := deviceauth.Refresh(ctx, deps.http(), meta, cached.Login.RefreshToken, profile.ClientID)
		if refreshErr == nil {
			if err := storeToken(home, profile, meta, token, cached.Login, deps.now()); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintln(out, "Renewed your existing session; no sign-in was needed.")
			return nil
		}
		// A rejected refresh is not an error: it is the ordinary end of a
		// refresh token's life, and the answer is the flow below.
		fmt.Fprintf(cmd.ErrOrStderr(), "note: could not renew the existing session (%v); signing in again\n", refreshErr)
	}

	auth, err := deviceauth.Authorize(ctx, deps.http(), meta, deviceauth.Request{
		ClientID: profile.ClientID,
		Scopes:   profile.Scopes,
		Audience: profile.Audience,
	})
	if err != nil {
		return nserr.Wrap(nserr.Fail, explainClientRejection(err, profile))
	}

	// Print before opening anything. The terminal is the real interface and
	// the only one that works when the browser is on another machine.
	fmt.Fprintf(out, "\n  Open:  %s\n  Code:  %s\n\n", auth.VerificationURI, auth.UserCode)
	if !args.noBrowser {
		if err := browser.Open(ctx, auth.BrowserURL(), deps.OpenURL); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: %v; open the URL above yourself\n", err)
		}
	}
	fmt.Fprintln(out, "Waiting for you to finish signing in...")

	token, err := deviceauth.Poll(ctx, deps.http(), meta, auth, profile.ClientID, deps.pollOptions()...)
	if err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	if err := storeToken(home, profile, meta, token, tokenstore.Login{}, deps.now()); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}

	stored, _ := tokenstore.Load(home)
	fmt.Fprintf(out, "Signed in%s.\n", describeLogin(stored.Login))
	fmt.Fprintf(out, "Token cached in %s\n", tokenstore.Path(home))
	return nil
}

// explainClientRejection adds the missing-client_id hint to a rejection that
// almost always means exactly that.
//
// A real provider registers its own applications and rejects a client id it
// does not know, while the local mock registers none and accepts any -- so a
// profile that worked against `nsctl authd` and fails against Okta fails here,
// with a bare "invalid_client" and no indication that the file is missing a
// key it was never required to set.
func explainClientRejection(err error, profile nsconfig.Profile) error {
	var oauthErr *deviceauth.Error
	if !errors.As(err, &oauthErr) {
		return err
	}
	if oauthErr.Code != "invalid_client" && oauthErr.Code != "unauthorized_client" {
		return err
	}
	if profile.ClientID != nsconfig.DefaultClientID {
		return err
	}
	return fmt.Errorf("%w\n"+
		"  Profile %q sets no client_id, so nsctl sent the default %q.\n"+
		"  A real provider only accepts an application it has registered; add its\n"+
		"  client id to the profile:\n\n      [profile.%s]\n      client_id = \"...\"\n",
		err, profile.Name, nsconfig.DefaultClientID, profile.Name)
}

// storeToken writes a token response, keeping the previous refresh token when
// the server did not rotate it.
func storeToken(home string, profile nsconfig.Profile, meta *deviceauth.Metadata,
	token *deviceauth.Token, previous tokenstore.Login, now time.Time) error {

	issuer := meta.Issuer
	if issuer == "" {
		issuer = profile.AuthURL
	}
	login := tokenstore.Login{
		AccessToken:  token.AccessToken,
		IDToken:      token.IDToken,
		RefreshToken: token.RefreshToken,
		Issuer:       issuer,
		Profile:      profile.Name,
		TokenType:    token.TokenType,
		Scope:        token.Scope,
	}
	// A server may rotate the refresh token, return the same one, or return
	// none. Only the third case needs handling, and dropping the old one there
	// would discard a working credential.
	if login.RefreshToken == "" {
		login.RefreshToken = previous.RefreshToken
	}
	login.SetExpiry(token.ExpiresAt(now))
	return tokenstore.Store(home, login)
}

// resolveProfile finds the endpoint to sign in against, prompting or refusing
// when there is no configuration.
func resolveProfile(cmd *cobra.Command, opts *Options, deps *loginDeps, home string, args loginArgs) (nsconfig.Profile, error) {
	name := args.profile

	// An explicit --auth-url needs no file at all, which is what lets a
	// scripted install pass the URL once and never author TOML.
	if args.authURL != "" {
		if name == "" {
			name = "default"
		}
		profile := nsconfig.Profile{Name: name, AuthURL: args.authURL}
		if err := profile.Validate(); err != nil {
			return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, err)
		}
		if args.save {
			if err := saveProfile(cmd, opts, home, profile); err != nil {
				return nsconfig.Profile{}, err
			}
		}
		return normalizeProfile(profile), nil
	}

	cfg, err := nsconfig.Load(home, opts.Lookup)
	switch {
	case err == nil:
		// Resolve consults the file first and the build's compiled-in endpoint
		// second, so a release build signs in with no configuration at all
		// while anyone who wrote a file keeps what they wrote.
		profile, perr := nsconfig.Resolve(cfg, name)
		if perr == nil {
			return profile, nil
		}
		// The file exists and does not describe what was asked for. Prompting
		// here would be reasonable only for an empty file; anything else is a
		// question the user already answered, differently.
		if len(cfg.Profiles) > 0 {
			return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, perr)
		}
	case strings.Contains(err.Error(), nsconfig.ErrNoConfig.Error()):
		// No file. The build's own endpoint answers here if it has one; a
		// development build has none, and that is the prompt-or-refuse case.
		if profile, rerr := nsconfig.Resolve(nil, name); rerr == nil {
			return profile, nil
		}
	default:
		// A file that exists and does not parse. Never overwritten, never
		// prompted over: a typo must not become data loss.
		return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, err)
	}

	if args.noPrompt || !deps.isTTY(cmd) {
		return nsconfig.Profile{}, noConfigError(home, opts, name)
	}
	return promptForProfile(cmd, opts, home, name)
}

// promptForProfile asks for the one value that cannot be defaulted, then
// writes it so the question is asked once.
func promptForProfile(cmd *cobra.Command, opts *Options, home, name string) (nsconfig.Profile, error) {
	if name == "" {
		name = "default"
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  No login endpoint is configured in %s.\n",
		nsconfig.Path(home, opts.Lookup))
	fmt.Fprintln(out, "  This is the OAuth issuer nsctl signs in against: your identity")
	fmt.Fprintln(out, "  provider's issuer URL, such as an Okta authorization server.")
	answer := tty.New(cmd.InOrStdin(), out).Ask(fmt.Sprintf("\n  Endpoint for profile %q", name), "")
	if answer == "" {
		return nsconfig.Profile{}, noConfigError(home, opts, name)
	}
	profile := nsconfig.Profile{Name: name, AuthURL: answer}
	if verr := profile.Validate(); verr != nil {
		return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, verr)
	}
	if err := saveProfile(cmd, opts, home, profile); err != nil {
		return nsconfig.Profile{}, err
	}
	return normalizeProfile(profile), nil
}

// saveProfile adds a profile to the config, creating the file if absent.
func saveProfile(cmd *cobra.Command, opts *Options, home string, profile nsconfig.Profile) error {
	cfg, err := nsconfig.Load(home, opts.Lookup)
	if err != nil {
		if !strings.Contains(err.Error(), nsconfig.ErrNoConfig.Error()) {
			return nserr.Wrap(nserr.Usage, err)
		}
		cfg = &nsconfig.Config{}
	}
	cfg.Set(profile)
	if err := nsconfig.Save(home, opts.Lookup, cfg); err != nil {
		return nserr.Wrap(nserr.Fail, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Saved to %s\n", nsconfig.Path(home, opts.Lookup))
	return nil
}

// normalizeProfile applies the defaults Profile() would have applied to a
// profile that came from a flag or a prompt rather than from the file.
func normalizeProfile(p nsconfig.Profile) nsconfig.Profile {
	cfg := &nsconfig.Config{Profiles: map[string]nsconfig.Profile{p.Name: p}}
	resolved, err := cfg.Profile(p.Name)
	if err != nil {
		return p
	}
	return resolved
}

// noConfigError refuses, and shows what to write. An error that says what is
// missing without showing the shape sends the user to the documentation for
// two lines of TOML.
func noConfigError(home string, opts *Options, name string) error {
	return nserr.New(nserr.Usage,
		"no login endpoint is configured.\n"+
			"  Write %s:\n\n%s\n"+
			"  auth_url is the OAuth issuer -- your identity provider's issuer URL, such as\n"+
			"  an Okta authorization server. Or pass --auth-url once with --save.",
		nsconfig.Path(home, opts.Lookup), indent(nsconfig.Example(name)))
}

func indent(block string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		b.WriteString("      " + line + "\n")
	}
	return b.String()
}

// describeLogin names who and until when, for a one-line confirmation.
func describeLogin(login tokenstore.Login) string {
	var parts []string
	if claims, err := authd.DecodeClaims(login.AccessToken); err == nil {
		if subject := claimString(claims, "sub"); subject != "" {
			parts = append(parts, "as "+subject)
		}
	}
	if expiry := login.Expiry(); !expiry.IsZero() {
		parts = append(parts, "until "+expiry.Local().Format(time.RFC1123))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, ", ")
}

func newLogoutCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "logout",
		Short:         "Discard the cached credential",
		Long:          "Removes the token `nsctl login` cached. The configured endpoints are left alone.",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			file, err := tokenstore.Load(home)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			if !file.Login.Present() {
				fmt.Fprintln(cmd.OutOrStdout(), "Not signed in.")
				return nil
			}
			if err := tokenstore.Clear(home); err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed out; removed the token from %s\n", tokenstore.Path(home))
			return nil
		},
	}
	return cmd
}

func newWhoamiCommand(opts *Options) *cobra.Command {
	var showClaims bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the cached credential",
		Long: `Prints who the cached token says you are.

The token is decoded, not verified -- this reports what the credential carries,
it does not decide anything. Run ` + "`nsctl login`" + ` to replace an expired one.`,
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := opts.RequireHome()
			if err != nil {
				return err
			}
			file, err := tokenstore.Load(home)
			if err != nil {
				return nserr.Wrap(nserr.Fail, err)
			}
			login := file.Login
			if !login.Present() {
				return nserr.New(nserr.Usage,
					"not signed in. Run `nsctl login`.")
			}
			claims, decodeErr := authd.DecodeClaims(login.AccessToken)
			if showClaims {
				if decodeErr != nil {
					return nserr.Wrap(nserr.Fail, decodeErr)
				}
				return printJSON(cmd, claims)
			}

			out := cmd.OutOrStdout()
			if decodeErr == nil {
				printField(out, "Subject", claimString(claims, "sub"))
				printField(out, "Email", claimString(claims, "email"))
				printField(out, "Name", claimString(claims, "name"))
				printField(out, "Groups", strings.Join(claimStrings(claims, "groups"), ", "))
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not decode the token: %v\n", decodeErr)
			}
			printField(out, "Issuer", login.Issuer)
			printField(out, "Profile", login.Profile)
			if expiry := login.Expiry(); !expiry.IsZero() {
				status := "expires"
				if !login.Valid(time.Now()) {
					status = "EXPIRED"
				}
				printField(out, status, expiry.Local().Format(time.RFC1123))
			}
			if login.Refreshable() {
				printField(out, "Renewable", "yes")
			}
			fmt.Fprintf(out, "\nFrom %s. The token is shown as it was decoded, not verified.\n",
				tokenstore.Path(home))
			return nil
		},
	}
	cmd.Flags().BoolVar(&showClaims, "show-claims", false, "print the full claim set as JSON")
	return cmd
}

func printField(out interface{ Write([]byte) (int, error) }, label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(out, "%-10s %s\n", label+":", value)
}

func claimString(claims map[string]any, key string) string {
	if value, ok := claims[key].(string); ok {
		return value
	}
	return ""
}

func claimStrings(claims map[string]any, key string) []string {
	raw, ok := claims[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
