package installitems

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/agentskills"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
)

// Env is what an install needs from the host.
type Env struct {
	// Home is HMD_HOME.
	Home string
	// RepoHome is $HMD_REPO_HOME, where git items are cloned; "" is unset.
	RepoHome string
	// Environ is the environment children run in: the process environment,
	// hmd.env beneath it and HMD_HOME over it, as a plugin sees (NERD018
	// SPEC005). Its PATH is where requirements are looked up.
	Environ []string
	// Stdout takes progress; Stderr takes the children's redacted output.
	Stdout, Stderr io.Writer
	// Skills is where agent-skills items go.
	Skills SkillTarget
	// CheckNoun refuses a noun the host will not attach: a built-in, a
	// reserved name, one another plugin owns. Nil accepts every noun.
	CheckNoun func(noun string) error
}

// SkillTarget is the NERD015 host and scope skills are installed for.
type SkillTarget struct {
	Host     agentskills.Host
	Scope    agentskills.Scope
	Project  string
	UserHome string
}

// InstallsRoot holds every installed plugin's items.
func InstallsRoot(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "installs")
}

// VersionDir is one installed version of one plugin.
func VersionDir(home, class, version string) string {
	return filepath.Join(InstallsRoot(home), class+"@"+version)
}

// PlatformKey is this build's GOOS_GOARCH, the key into a binary item.
func PlatformKey() string { return runtime.GOOS + "_" + runtime.GOARCH }

// Install carries out sec for class@version, whose artifact tree is root,
// and returns the ledger nsctl.toml records (SPEC003, SPEC004).
//
// Nothing is written until every check has passed. Items are placed into a
// staged version directory that is renamed into place at the end, so a
// failure at any point removes what this run placed and leaves a previous
// version as it was.
func Install(ctx context.Context, env Env, class, version, root string, sec *Section) (_ []nsconfig.PluginItem, err error) {
	if env.Home == "" {
		return nil, fmt.Errorf("installing a plugin needs an HMD_HOME")
	}
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	if err := sec.Validate(root); err != nil {
		return nil, fmt.Errorf("%s@%s: %w", class, version, err)
	}
	if err := preflight(ctx, env, class, root, sec); err != nil {
		return nil, fmt.Errorf("%s@%s cannot be installed:\n  %w", class, version, err)
	}

	final := VersionDir(env.Home, class, version)
	stage := final + ".installing"
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	if err := os.RemoveAll(stage); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}
	undo = append(undo, func() { os.RemoveAll(stage) })

	items := make([]nsconfig.PluginItem, len(sec.Items))
	clones := map[string]string{}
	var skillItems []int
	var docs []KnowledgeEntry
	for i, it := range sec.Items {
		key := it.Key(i)
		itemStage, itemFinal := filepath.Join(stage, key), filepath.Join(final, key)

		// A git item's content is its clone, made first so every kind can
		// read from it.
		src := root
		if it.FromGit() {
			dest, ok := clones[it.Source.Git]
			if !ok {
				var created bool
				dest, created, err = ensureClone(ctx, env, it.Source)
				if err != nil {
					return nil, err
				}
				if created {
					undo = append(undo, func() { os.RemoveAll(dest) })
				}
				clones[it.Source.Git] = dest
			}
			src = dest
		}

		switch it.Kind {
		case nsconfig.KindCommand:
			items[i], err = placeCommand(ctx, env, src, itemStage, itemFinal, it)
		case nsconfig.KindDocs:
			items[i], err = placeDocs(src, itemStage, itemFinal, it)
			if err == nil {
				docs = append(docs, KnowledgeEntry{Class: class, Version: version, Title: it.Title,
					Format: it.Format, Path: items[i].Path, Clone: it.FromGit()})
			}
		case nsconfig.KindSkills:
			skillItems = append(skillItems, i)
		}
		if err != nil {
			return nil, err
		}
	}

	// Skills go last: they are the only writes outside HMD_HOME and the repo
	// folder, so everything that can fail for an ordinary reason has already
	// succeeded by the time one is written.
	for _, i := range skillItems {
		it := sec.Items[i]
		dir := filepath.Join(root, filepath.FromSlash(it.Dir))
		if it.FromGit() {
			if dir, err = inClone(clones[it.Source.Git], it.Dir); err != nil {
				return nil, err
			}
		}
		items[i], err = placeSkills(env, class, dir, it, &undo)
		if err != nil {
			return nil, err
		}
	}

	prevIndex, err := setKnowledge(env.Home, class, docs)
	if err != nil {
		return nil, err
	}
	undo = append(undo, func() { _ = writeKnowledge(env.Home, prevIndex) })

	if err := os.RemoveAll(final); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, final); err != nil {
		return nil, err
	}
	undo = append(undo, func() { os.RemoveAll(final) })
	return items, nil
}

// preflight collects every reason the install cannot go ahead, so the user
// fixes them in one pass rather than one per run.
func preflight(ctx context.Context, env Env, class, root string, sec *Section) error {
	var problems []string
	for _, r := range sec.Required() {
		if _, ok := lookPath(r.Binary, env.Environ); !ok {
			problems = append(problems, fmt.Sprintf("%s is not on PATH. %s", r.Binary, r.InstallHint))
		}
	}
	for i, it := range sec.Items {
		if it.Kind == nsconfig.KindCommand && env.CheckNoun != nil {
			if err := env.CheckNoun(it.Noun); err != nil {
				problems = append(problems, fmt.Sprintf("noun %q: %v", it.Noun, err))
			}
		}
		if it.Kind == nsconfig.KindCommand && it.Runtime != nil && it.Runtime.Kind == nsconfig.RuntimeBinary {
			if _, ok := it.Runtime.Binaries[PlatformKey()]; !ok {
				problems = append(problems, fmt.Sprintf("item %d: %s is not built for %s; it is built for %s",
					i+1, it.Noun, PlatformKey(), strings.Join(sortedKeys(it.Runtime.Binaries), ", ")))
			}
		}
		if it.FromGit() {
			dest, err := clonePath(env.RepoHome, it.Source)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if _, ok := lookPath("git", env.Environ); ok {
				if _, err := cloneState(ctx, env, dest, it.Source.Git); err != nil {
					problems = append(problems, err.Error())
				}
			}
		}
		if it.Kind == nsconfig.KindSkills && !it.FromGit() {
			if err := checkSkills(env, class, filepath.Join(root, filepath.FromSlash(it.Dir))); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(dedupe(problems), "\n  "))
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// placeCommand places a command item by runtime.
func placeCommand(ctx context.Context, env Env, src, stage, final string, it Item) (nsconfig.PluginItem, error) {
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nsconfig.PluginItem{}, err
	}
	switch it.Runtime.Kind {
	case nsconfig.RuntimePython:
		return placePython(ctx, env, src, stage, final, it)
	case nsconfig.RuntimeScripts:
		return placeScripts(src, stage, final, it)
	case nsconfig.RuntimeBinary:
		return placeBinary(src, stage, final, it)
	}
	return nsconfig.PluginItem{}, fmt.Errorf("unknown runtime %q", it.Runtime.Kind)
}

// placeScripts copies the named scripts (SPEC007). From a clone they are
// run where they are, so a team's edits take effect without a reinstall.
func placeScripts(src, stage, final string, it Item) (nsconfig.PluginItem, error) {
	out := nsconfig.PluginItem{Kind: nsconfig.KindCommand, Runtime: nsconfig.RuntimeScripts, Noun: it.Noun,
		Summary: it.Summary, Path: final, Interpreter: it.Runtime.Interpreter, Scripts: map[string]string{}}
	for _, name := range sortedKeys(it.Runtime.Scripts) {
		rel := filepath.FromSlash(it.Runtime.Scripts[name])
		if it.FromGit() {
			p, err := inClone(src, it.Runtime.Scripts[name])
			if err != nil {
				return out, err
			}
			if _, err := os.Stat(p); err != nil {
				return out, fmt.Errorf("%s: script %s: %w", it.Noun, name, err)
			}
			out.Scripts[name] = p
			out.Path, out.Scripts[name] = src, p
			continue
		}
		file := name + filepath.Ext(rel)
		if err := copyFile(filepath.Join(src, rel), filepath.Join(stage, file)); err != nil {
			return out, err
		}
		if err := os.Chmod(filepath.Join(stage, file), 0o755); err != nil {
			return out, err
		}
		out.Scripts[name] = filepath.Join(final, file)
	}
	return out, nil
}

// placeBinary copies this platform's binary (SPEC008).
func placeBinary(src, stage, final string, it Item) (nsconfig.PluginItem, error) {
	rel, ok := it.Runtime.Binaries[PlatformKey()]
	if !ok {
		return nsconfig.PluginItem{}, fmt.Errorf("%s is not built for %s", it.Noun, PlatformKey())
	}
	if err := copyFile(filepath.Join(src, filepath.FromSlash(rel)), filepath.Join(stage, it.Noun)); err != nil {
		return nsconfig.PluginItem{}, err
	}
	if err := os.Chmod(filepath.Join(stage, it.Noun), 0o755); err != nil {
		return nsconfig.PluginItem{}, err
	}
	return nsconfig.PluginItem{Kind: nsconfig.KindCommand, Runtime: nsconfig.RuntimeBinary, Noun: it.Noun,
		Summary: it.Summary, Path: final, Target: filepath.Join(final, it.Noun)}, nil
}

// placeDocs copies an artifact's docs, or records a clone's (SPEC010).
func placeDocs(src, stage, final string, it Item) (nsconfig.PluginItem, error) {
	out := nsconfig.PluginItem{Kind: nsconfig.KindDocs, Title: it.Title, Format: it.Format}
	if it.FromGit() {
		p, err := inClone(src, it.Dir)
		if err != nil {
			return out, err
		}
		out.Path, out.Clone = p, true
		return out, nil
	}
	if err := copyTree(filepath.Join(src, filepath.FromSlash(it.Dir)), stage); err != nil {
		return out, err
	}
	out.Path = final
	return out, nil
}

// checkSkills refuses, before anything is written, a skill the user has
// edited or that is not this plugin's.
func checkSkills(env Env, class, dir string) error {
	src, err := agentskills.FromDir(dir, class)
	if err != nil {
		return err
	}
	dests, err := src.Destinations(nil, env.Skills.Host, env.Skills.Scope, env.Skills.Project, env.Skills.UserHome)
	if err != nil {
		return err
	}
	for _, d := range dests {
		if d.State == agentskills.Modified {
			return fmt.Errorf("skill %s at %s was edited, or is not %s's; remove or rename it first", d.Skill, d.Path, class)
		}
	}
	return nil
}

// placeSkills installs a directory of skills through NERD015's installer
// (SPEC009), recording every destination and undoing any it created.
func placeSkills(env Env, class, dir string, it Item, undo *[]func()) (nsconfig.PluginItem, error) {
	out := nsconfig.PluginItem{Kind: nsconfig.KindSkills}
	src, err := agentskills.FromDir(dir, class)
	if err != nil {
		return out, err
	}
	dests, err := src.Destinations(nil, env.Skills.Host, env.Skills.Scope, env.Skills.Project, env.Skills.UserHome)
	if err != nil {
		return out, err
	}
	for _, d := range dests {
		if d.State == agentskills.Modified {
			return out, fmt.Errorf("skill %s at %s was edited, or is not %s's; remove or rename it first", d.Skill, d.Path, class)
		}
	}
	for _, d := range dests {
		if d.State != agentskills.Installed {
			if err := src.Install(d, false, false); err != nil {
				return out, err
			}
			if d.State == agentskills.Missing {
				path := d.Path
				*undo = append(*undo, func() { os.RemoveAll(path) })
			}
			fmt.Fprintf(env.Stdout, "Installed skill %s -> %s\n", d.Skill, d.Path)
		}
		out.Paths = append(out.Paths, d.Path)
	}
	if len(dests) == 0 {
		fmt.Fprintf(env.Stdout, "warning: %s holds no skills (no directory with a SKILL.md)\n", dir)
	}
	return out, nil
}
