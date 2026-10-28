package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bom"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/registry"
)

// instanceRow is one declared instance of an inspected repo class.
type instanceRow struct {
	// Environment is the environment's name, or "control-plane".
	Environment  string              `json:"environment"`
	Instance     string              `json:"instance"`
	RepoClass    string              `json:"repo_class"`
	Version      string              `json:"version,omitempty"`
	From         string              `json:"from"`
	Dependencies map[string][]string `json:"dependencies,omitempty"`
}

// dependencyTargets reads a dependency's wiring: one instance name or a
// list of them.
func dependencyTargets(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// instanceSection is where the inspected repo classes are instantiated
// (NERD032 SPEC010), read from the environment and control-plane manifests
// under HMD_HOME alone: no control plane is asked.
func instanceSection(_ context.Context, opts *Options, a inspectArgs) section {
	if opts.Home == "" {
		return section{Skipped: "HMD_HOME is not set, so there are no environments to read"}
	}
	roots := map[string][]string{}
	for _, s := range a.sources {
		if s.Class != "" {
			roots[s.Class] = append(roots[s.Class], s.Root)
		}
	}
	reg, err := registry.Load(opts.Home, opts.Lookup)
	if err != nil {
		return section{Skipped: err.Error()}
	}
	type scope struct {
		label string
		m     *manifest.Manifest
	}
	var scopes []scope
	var fs []finding
	for _, name := range reg.Names() {
		if a.env != "" && name != a.env {
			continue
		}
		env, err := reg.Environment(name, opts.Lookup)
		if err != nil {
			continue
		}
		m, err := manifest.Load(opts.Home, env.Slug, opts.Lookup)
		if err != nil {
			fs = append(fs, finding{Severity: sevError, Code: "instance-manifest-unreadable", Subject: name, Message: err.Error()})
			continue
		}
		if m != nil {
			scopes = append(scopes, scope{name, m})
		}
	}
	cp, err := manifest.LoadControlPlane(opts.Home, opts.Lookup)
	if err != nil {
		fs = append(fs, finding{Severity: sevError, Code: "instance-manifest-unreadable", Subject: "control-plane", Message: err.Error()})
	}
	cpNames := map[string]bool{}
	if cp != nil {
		for _, r := range cp.Repos {
			cpNames[r.InstanceName] = true
		}
		if a.env == "" {
			scopes = append(scopes, scope{"control-plane", cp})
		}
	}

	var rows []instanceRow
	declared := map[string]bool{}
	for _, sc := range scopes {
		names := map[string]bool{}
		for _, r := range sc.m.Repos {
			names[r.InstanceName] = true
		}
		for _, r := range sc.m.Repos {
			inspected, ok := roots[r.RepoClassName]
			if !ok {
				continue
			}
			declared[r.RepoClassName] = true
			row := instanceRow{Environment: sc.label, Instance: r.InstanceName, RepoClass: r.RepoClassName, Version: r.Version}
			subject := sc.label + "/" + r.InstanceName
			if r.SourceType() == manifest.SourceArtifact {
				row.From = "artifact"
			} else if p := r.RepoPath(opts.Lookup); p != "" {
				row.From = p
				here := false
				for _, root := range inspected {
					here = here || sameDir(p, root)
				}
				if !here {
					fs = append(fs, finding{Severity: sevInfo, Code: "instance-other-checkout", Subject: subject,
						Message: fmt.Sprintf("deploys %s from %s, not from the repository inspected (%s)",
							r.RepoClassName, p, strings.Join(inspected, ", "))})
				}
			} else {
				row.From = "local (no path: HMD_REPO_HOME is unset)"
			}
			roles := make([]string, 0, len(r.Dependencies))
			for role := range r.Dependencies {
				roles = append(roles, role)
			}
			sort.Strings(roles)
			for _, role := range roles {
				targets := dependencyTargets(r.Dependencies[role])
				if row.Dependencies == nil {
					row.Dependencies = map[string][]string{}
				}
				row.Dependencies[role] = targets
				for _, t := range targets {
					if names[t] || cpNames[t] || bom.IsSubstrate(t) {
						continue
					}
					fs = append(fs, finding{Severity: sevError, Code: "instance-dependency-undeclared", Subject: subject,
						Message: fmt.Sprintf("dependency %s is wired to %q, which %s does not declare and is not substrate",
							role, t, sc.label),
						Where: sc.m.Path})
				}
			}
			rows = append(rows, row)
		}
	}
	classes := make([]string, 0, len(roots))
	for c := range roots {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, c := range classes {
		if !declared[c] {
			where := "any environment"
			if a.env != "" {
				where = a.env
			}
			fs = append(fs, finding{Severity: sevInfo, Code: "instance-none", Subject: c,
				Message: fmt.Sprintf("%s is not declared in %s", c, where)})
		}
	}
	return section{Summary: rows, Findings: fs, text: func(out io.Writer) {
		if len(rows) == 0 {
			fmt.Fprintln(out, "  no environment declares an instance of these repo classes")
			return
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  ENVIRONMENT\tINSTANCE\tREPO CLASS\tVERSION\tFROM\tDEPENDENCIES")
		for _, r := range rows {
			var deps []string
			for role, ts := range r.Dependencies {
				deps = append(deps, role+"="+strings.Join(ts, ","))
			}
			sort.Strings(deps)
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", r.Environment, r.Instance, r.RepoClass, orDash(r.Version), r.From,
				orDash(strings.Join(deps, " ")))
		}
		w.Flush()
	}}
}

func newInstanceInspectCommand(opts *Options) *cobra.Command {
	var asJSON, info bool
	var env string
	cmd := &cobra.Command{
		Use:   "inspect [path...]",
		Short: "Where the repo classes in these directories are declared, and whether their wiring resolves",
		Long: `For each repo class among the repositories in the named directories (default:
the current one), lists every instance of it an environment manifest under
HMD_HOME declares, and the control-plane manifest: the environment, instance
name, version, where it deploys from and its dependency wiring. Reads the
manifests only; no control plane is asked.

Findings: a dependency wired to an instance its manifest does not declare and
that is not substrate (error -- the deploy would fail on it); a repo class no
environment declares (info); an instance deploying from a checkout other than
the repository inspected (info). Exits 1 on an error. This is the instance
section of ` + "`nsctl inspect`" + `. NERD032 SPEC010.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := opts.RequireHome(); err != nil {
				return err
			}
			srcs, err := inspect.Discover(pathsOr(args))
			if err != nil {
				return nserr.Wrap(nserr.Usage, err)
			}
			s := instanceSection(cmd.Context(), opts, inspectArgs{paths: pathsOr(args), sources: srcs, env: env})
			s.Noun = "instance"
			return printSection(cmd, s, asJSON, info)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the section as JSON")
	cmd.Flags().BoolVar(&info, "info", false, "List info findings too, not only count them")
	cmd.Flags().StringVar(&env, "env", "", "Only this environment (default: every environment, and the control plane)")
	return cmd
}
