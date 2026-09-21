package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/oci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// registryCredential is NERD016 SPEC006 for a verb: every profile in
// nsctl.toml is offered for a registry_url match, and a missing or unreadable
// config contributes nothing rather than failing an anonymous pull.
func registryCredential(opts *Options, host, flagToken string) oci.Credential {
	var profiles []nsconfig.Profile
	if cfg, err := nsconfig.Load(opts.Home, opts.Lookup); err == nil {
		for _, p := range cfg.Profiles {
			profiles = append(profiles, p)
		}
	}
	return oci.ResolveCredential(host, flagToken, opts.Lookup, opts.Home, profiles)
}

// expandRef parses a reference, expanding a bare name against namespace and
// reporting that it did so the verb can print the expansion (SPEC001's
// never-guess rule: a compiled-in namespace is a build identity, and the
// user sees it applied).
func expandRef(s, namespace string) (ref oci.Ref, expanded bool, err error) {
	ref, err = oci.ParseRef(s)
	if err == nil {
		return ref, false, nil
	}
	if !errors.Is(err, oci.ErrNoHost) || strings.Contains(s, "/") {
		return oci.Ref{}, false, nserr.Wrap(nserr.Usage, err)
	}
	ref, err = oci.ParseRef(namespace + "/" + s)
	if err != nil {
		return oci.Ref{}, false, nserr.Wrap(nserr.Usage, err)
	}
	return ref, true, nil
}

// resolveVersion pins an untagged reference to the newest published version,
// or the newest satisfying spec, and says which (SPEC004: the chosen version
// is always printed).
func resolveVersion(ctx context.Context, w io.Writer, f oci.Fetcher, ref oci.Ref, spec string) (oci.Ref, error) {
	if ref.Versioned() && spec == "" {
		return ref, nil
	}
	if ref.Versioned() && spec != "" {
		return oci.Ref{}, nserr.New(nserr.Usage, "%s names a version and --spec was given; use one or the other", ref)
	}
	versions, err := f.Tags(ctx, ref)
	if err != nil {
		return oci.Ref{}, nserr.Wrap(nserr.Fail, err)
	}
	if len(versions) == 0 {
		return oci.Ref{}, nserr.New(nserr.Fail, "%s has no version-shaped tags", ref)
	}
	chosen := versions[0]
	if spec != "" {
		parsed, err := versionspec.Parse(spec)
		if err != nil {
			return oci.Ref{}, nserr.Wrap(nserr.Usage, err)
		}
		v, ok := parsed.Highest(versions)
		if !ok {
			return oci.Ref{}, nserr.New(nserr.Fail, "no published version of %s satisfies %q; published: %s",
				ref, spec, strings.Join(versions, ", "))
		}
		chosen = v
	}
	fmt.Fprintf(w, "Resolved %s to %s\n", ref, chosen)
	return ref.WithTag(chosen), nil
}

// reportRef prints an expansion the user did not type.
func reportRef(w io.Writer, typed string, ref oci.Ref, expanded bool) {
	if expanded {
		fmt.Fprintf(w, "Resolving %s as %s\n", typed, ref)
	}
}
