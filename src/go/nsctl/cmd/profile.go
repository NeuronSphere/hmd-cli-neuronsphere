package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// profileFlag is the --profile every verb that reaches a cloud service shares.
//
// It is not `nsctl login`'s flag with a wider audience: login *requires* a
// profile, because an issuer cannot be guessed, while these verbs have worked
// for a year on environment variables alone and must keep doing so. So a
// missing configuration file is not an error here, and a profile contributes
// endpoints rather than supplying them.
type profileFlag struct {
	name string
}

func (p *profileFlag) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.name, "profile", "",
		"profile in "+nsconfig.Name+" whose endpoints to use")
}

// named reports whether the user typed a profile name.
//
// This is what decides whether a shadowed endpoint is worth reporting. Somebody
// who asked for a named tenant and reached a different one needs to know;
// somebody who never mentioned profiles is being told about a file they may not
// have.
func (p *profileFlag) named() bool { return p.name != "" }

// resolve reads the profile, tolerating every way there might not be one.
//
// A named profile that cannot be resolved is an error: the user asked for a
// specific tenant and must not silently get another. An unnamed one degrades to
// the zero Profile, which contributes nothing and leaves the environment
// variables to answer -- including the ambiguous case of several profiles and
// no default_profile, where guessing which tenant was meant is exactly what
// nsconfig.Profile refuses to do.
func (p *profileFlag) resolve(opts *Options) (nsconfig.Profile, error) {
	cfg, err := nsconfig.Load(opts.Home, opts.Lookup)
	if err != nil {
		if errors.Is(err, nsconfig.ErrNoConfig) {
			if p.named() {
				return nsconfig.Profile{}, nserr.New(nserr.Usage,
					"--profile %s: %v.\nWrite one:\n%s",
					p.name, err, nsconfig.TenantExample(p.name))
			}
			return nsconfig.Profile{}, nil
		}
		return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, err)
	}
	profile, err := cfg.Profile(p.name)
	if err != nil {
		if p.named() {
			return nsconfig.Profile{}, nserr.Wrap(nserr.Usage, err)
		}
		return nsconfig.Profile{}, nil
	}
	return profile, nil
}

// reportShadowed warns when an environment variable overrode an address the
// named profile also carries.
//
// Only for a profile the user named, and only on stderr: the override still
// wins, because an environment variable beating a file is the convention
// everywhere else. What must not happen is for it to win invisibly.
func reportShadowed(cmd *cobra.Command, p *profileFlag, service nsconfig.Service, e nsconfig.Endpoint) {
	if e.Shadowed == "" || !p.named() {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"warning: profile %q names %s %s, but $%s is set and wins; using %s\n",
		p.name, service.ProfileKey, e.Shadowed, service.URLEnv, e.URL)
}
