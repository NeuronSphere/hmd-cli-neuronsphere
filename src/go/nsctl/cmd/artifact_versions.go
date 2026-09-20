package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versions"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// listed is how many versions `artifact versions` prints before summarising.
//
// A long-lived repo class has hundreds, and a screen of them buries the answer.
// The count that was not printed is always stated: a silent truncation reads as
// "that is all of them".
const listed = 20

func newArtifactVersionsCommand(opts *Options) *cobra.Command {
	var libs librarians
	var spec, itemType string
	var offline, all bool

	cmd := &cobra.Command{
		Use:   "versions <repo-class>",
		Short: "List the versions a repo class has published",
		Long: `Asks a cloud Artifact Librarian what a repo class has published, newest first,
and -- with --spec -- what a BACON version specifier resolves to today.

This is the question you have when you are standing an environment up and have
to choose a version, and again when you want to know what is newer. A manifest
almost never names a version: it names a range, and nothing else in the platform
turns a range into a version.

--spec takes the grammar a manifest writes. Note that ` + "`~= 0.1`" + ` is not "the 0.1
series": it means major 0 and minor at least 1, so 0.2.5 satisfies it. Write
` + "`== 0.1.*`" + ` for the series.

The answer is cached under $HMD_HOME/.cache/neuronsphere/versions and never
refreshed behind your back: this command queries, --offline reads what the last
query left and says how old it is. Nothing that deploys resolves a version at
all -- a deploy whose result depends on the day it ran is the thing a lock
exists to prevent.`,
		Example: `  nsctl artifact versions hmd-inf-trino
  nsctl artifact versions hmd-inf-trino --spec "~= 0.1"
  nsctl artifact versions hmd-inf-trino --offline`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoClass := args[0]
			if itemType == "" {
				itemType = manifest.DefaultArtifactType
			}
			var wanted versionspec.Spec
			if spec != "" {
				parsed, err := versionspec.Parse(spec)
				if err != nil {
					return specError(repoClass, spec, err)
				}
				wanted = parsed
			}

			found, err := published(cmd, opts, &libs, repoClass, offline)
			if err != nil {
				return err
			}
			return reportVersions(cmd, found, wanted, itemType, all)
		},
	}
	libs.bindCloud(cmd)
	libs.bindTenant(cmd)
	cmd.Flags().StringVar(&spec, "spec", "",
		"Report what this version specifier resolves to, as a manifest writes it")
	cmd.Flags().StringVar(&itemType, "type", "",
		"Content item type (default "+manifest.DefaultArtifactType+")")
	cmd.Flags().BoolVar(&offline, "offline", false,
		"Read the last query's answer instead of asking, reporting how old it is")
	cmd.Flags().BoolVar(&all, "all", false, "List every version rather than the newest few")
	return cmd
}

// published gets an enumeration, from the librarian or from the cache.
//
// Online it always re-queries and then writes the cache, because this is the
// command that asks. Offline it reads and never falls back to asking: an
// implicit query would make --offline mean "usually offline", which is not a
// promise anyone can rely on.
func published(cmd *cobra.Command, opts *Options, libs *librarians,
	repoClass string, offline bool) (*versions.Published, error) {

	if offline {
		p, err := versions.Load(opts.Home, repoClass)
		if errors.Is(err, versions.ErrNoCache) {
			return nil, nserr.New(nserr.Usage,
				"nothing has asked what %s has published on this machine, and --offline says not to ask."+
					"\nDrop it to query the librarian: `nsctl artifact versions %s`", repoClass, repoClass)
		}
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		return p, nil
	}

	cloud, err := libs.cloud(cmd, opts)
	if err != nil {
		return nil, err
	}
	p, err := enumerate(cmd.Context(), cmd, cloud, repoClass)
	if err != nil {
		return nil, err
	}
	if err := versions.Save(opts.Home, p); err != nil {
		// A cache that could not be written costs a re-query and nothing else,
		// so it is worth a word and not worth failing a command that has the
		// answer in hand.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not cache the answer: %v\n", err)
	}
	return p, nil
}

// enumerate queries a librarian, reporting progress as it goes.
//
// Progress goes to stderr and the answer to stdout, so a script capturing the
// answer does not have to filter the reassurance out of it. It is reassurance
// rather than decoration: the last leg takes half a minute for a repo class with
// several hundred versions, and silence that long reads as a hang.
func enumerate(ctx context.Context, cmd *cobra.Command, cloud *librarian.Client,
	repoClass string) (*versions.Published, error) {

	fmt.Fprintf(cmd.ErrOrStderr(), "Asking %s what %s has published...\n", cloud.BaseURL, repoClass)
	p, err := versions.Enumerate(ctx, cloud, repoClass, func(step string) {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", step)
	})
	if errors.Is(err, librarian.ErrNoSuchRepo) {
		return nil, nserr.New(nserr.Usage,
			"%s: the librarian has no repo of that name."+
				"\nCheck the spelling, or publish it: a repo class appears here the first time"+
				" `hmd build` publishes one of its artifacts", repoClass)
	}
	if err != nil {
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return p, nil
}

// reportVersions prints the enumeration, and the resolution when one was asked
// for.
func reportVersions(cmd *cobra.Command, p *versions.Published, wanted versionspec.Spec,
	itemType string, all bool) error {

	out := cmd.OutOrStdout()
	found := p.Versions(itemType)
	age := humanAge(p.Age(time.Now()))

	if len(found) == 0 {
		// Present, and with nothing of this type. Distinct from absent, and
		// distinct again from present with nothing *satisfying*: the three have
		// three different fixes.
		other := p.ItemTypes()
		if len(other) == 0 {
			return nserr.New(nserr.Fail,
				"%s has published nothing (%s).", p.RepoClass, age)
		}
		return nserr.New(nserr.Fail,
			"%s has published no %s artifacts (%s).\nIt has published: %s. Pass --type to ask about one.",
			p.RepoClass, itemType, age, strings.Join(other, ", "))
	}

	fmt.Fprintf(out, "%s has %s, %s.\n", p.RepoClass, count(len(found), "published "+itemType+" version"), age)

	if !wanted.Empty() {
		resolved, ok := wanted.Highest(found)
		if !ok {
			// Not an occasion for a lecture about what `~= 0.1` means -- that
			// belongs in --help, where it is read once, rather than in a
			// failure, where it is noise around the two facts that can be acted
			// on: how many exist and which is newest.
			return nserr.New(nserr.Usage,
				"%s has %s, none satisfying %q. The newest is %s.",
				p.RepoClass, count(len(found), "published "+itemType+" version"), wanted.String(), found[0])
		}
		fmt.Fprintf(out, "  %s resolves to %s\n", wanted.String(), resolved)
	}

	shown := found
	if !all && len(shown) > listed {
		shown = shown[:listed]
	}
	fmt.Fprintf(out, "  %s\n", strings.Join(shown, "  "))
	if len(shown) < len(found) {
		fmt.Fprintf(out, "  ... and %d more; --all lists them.\n", len(found)-len(shown))
	}
	return nil
}

// specError reports a specifier this cannot resolve, before anything is queried.
func specError(repoClass, spec string, err error) error {
	return nserr.New(nserr.Usage, "%s declares `version_spec: %q`, which nsctl cannot resolve.\n  %v",
		repoClass, spec, err)
}

// count says "1 version" or "4 versions", over env.go's plural, rather than the
// "(s)" that makes a message look generated.
func count(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, plural(n, noun, noun+"s"))
}

// humanAge says how stale an answer is in the terms somebody would say it in.
//
// This is the whole reason the enumeration records when it was taken: the
// difference between "this is the newest version" and "this was the newest
// version when somebody last asked" is not visible in the versions themselves.
func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		return "queried in the future, which means a clock is wrong"
	case d < time.Minute:
		return "queried just now"
	case d < time.Hour:
		return "queried " + count(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return "queried " + count(int(d.Hours()), "hour") + " ago"
	default:
		return "queried " + count(int(d.Hours()/24), "day") + " ago"
	}
}

// resolveRef settles a pull's reference to a concrete Spec, asking the librarian
// only when the reference does not already name a version.
//
// A bare repo class means the newest published, and a specifier means the newest
// satisfying it. Both are reported rather than assumed: a command that quietly
// chose 0.2.5 when you typed a range has told you nothing you could check.
func resolveRef(cmd *cobra.Command, opts *Options, libs *librarians,
	cloud *librarian.Client, req artifactRequest) (librarian.Spec, error) {

	if req.Version != "" {
		return librarian.Spec{Name: req.Name, Version: req.Version, ItemType: req.ItemType}, nil
	}

	p, err := enumerate(cmd.Context(), cmd, cloud, req.Name)
	if err != nil {
		return librarian.Spec{}, err
	}
	if err := versions.Save(opts.Home, p); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not cache the answer: %v\n", err)
	}

	found := p.Versions(req.ItemType)
	if len(found) == 0 {
		return librarian.Spec{}, nserr.New(nserr.Fail,
			"%s has published no %s artifacts.", req.Name, req.ItemType)
	}
	resolved, ok := req.Spec.Highest(found)
	if !ok {
		return librarian.Spec{}, nserr.New(nserr.Usage,
			"%s has %s, none satisfying %q. The newest is %s.",
			req.Name, count(len(found), "published "+req.ItemType+" version"), req.Spec.String(), found[0])
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Resolved %s to %s (newest of %d)\n",
		req.Describe(), resolved, len(found))
	return librarian.Spec{Name: req.Name, Version: resolved, ItemType: req.ItemType}, nil
}
