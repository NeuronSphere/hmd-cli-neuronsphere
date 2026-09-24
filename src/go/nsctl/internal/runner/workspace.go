package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// overlayIgnore are directories never worth copying into an overlay workspace:
// build output, VCS metadata and dependency caches.
//
// resources_output is excluded for a different and sharper reason: outputs must
// never become inputs. The runner submits every resources_output/*.json it
// finds in the workspace, so copying a prior run's forward would republish
// Resources for a deploy that did not happen.
var overlayIgnore = map[string]bool{
	".git": true, "node_modules": true, "build": true, "target": true,
	"dist": true, "cdktf.out": true, ".terraform": true, "__pycache__": true,
	"resources_output": true,
}

// nodeCommand is what a node runs.
//
// Two shapes, told apart by Argv. A NeuronSphere repo class runs Script under
// bash in projectbuilder: the generated `hmd ... deploy`, localized, or the
// repo's own src/local/deploy_local.sh. A foreign repo class (NERD009) runs
// Argv -- its manifest's ["exec", ...] entry -- as the container command, in
// Image when the manifest names one and in projectbuilder otherwise.
type nodeCommand struct {
	Script string
	Argv   []string
	Image  string
	// Override is set when Script is a src/local/deploy_local.sh replacing the
	// generated deploy script, which then has to be handed the configuration
	// the generated script carried inline.
	Override bool
}

// prepareWorkspace resolves the workspace to mount and the command to run.
//
// Four shapes, in precedence order:
//
//  1. deploy.commands is [["exec", ...]] in the repo's manifest: that argv is
//     the node, and the generated script is discarded. A repo that has
//     written down its deploy command has said something more specific than
//     a conventional filename, so this outranks the next shape. Nothing in
//     src/local applies to it.
//  2. src/local/deploy_local.sh replaces the generated command entirely.
//  3. src/local/{cdktf,helm} and src/local/config_local.json overlay their
//     cloud counterparts.
//  4. None of those, in which case the repo is mounted as it is.
//
// The middle two copy the repo to a temp directory first. The workspace is
// mounted read-write and these deploys write meta-data/resources_output/ --
// into the developer's own checkout, if it were mounted directly.
//
// isolate forces the copy for the other two shapes too. A bundled tree is
// shared by every environment on this HMD_HOME and is vouched for by the
// digest in its directory name, so a deploy writing its outputs into it would
// leave a cache that no longer matches what the binary carries -- and hand
// the next environment the last one's resources_output.
func (r *Runner) prepareWorkspace(repoPath, script string, isolate bool) (workspace string, cmd nodeCommand, cleanup func(), err error) {
	noop := func() {}

	// The manifest is read from the tree about to be mounted and no other:
	// the resolver's tiers exist to pick a tree, and once one is picked,
	// everything about the node comes from it.
	manifest, err := repoclass.ReadManifest(repoPath)
	if err != nil {
		return "", nodeCommand{}, noop, err
	}
	argv, err := manifest.ExecCommand()
	if err != nil {
		return "", nodeCommand{}, noop, fmt.Errorf("%s: %w", filepath.Join(repoPath, "meta-data", "manifest.json"), err)
	}
	if argv != nil {
		cmd := nodeCommand{Argv: argv, Image: manifest.Deploy.Image}
		workspace, cleanup := repoPath, noop
		if isolate {
			tmp, err := r.overlayWorkspace(repoPath, "")
			if err != nil {
				return "", nodeCommand{}, noop, err
			}
			workspace, cleanup = tmp, func() { os.RemoveAll(filepath.Dir(tmp)) }
		}
		// The directory the produced Resources are collected from. hmd-cli-helm
		// creates it on the native path; a foreign toolset cannot be expected
		// to know the convention, and without it the first thing a command
		// writes fails with "nonexistent directory".
		if err := os.MkdirAll(filepath.Join(workspace, "meta-data", "resources_output"), 0o755); err != nil {
			cleanup()
			return "", nodeCommand{}, noop, fmt.Errorf("preparing %s: %w", workspace, err)
		}
		return workspace, cmd, cleanup, nil
	}

	overlay := filepath.Join(repoPath, "src", "local")
	if info, statErr := os.Stat(overlay); statErr != nil || !info.IsDir() {
		if isolate {
			tmp, err := r.overlayWorkspace(repoPath, "")
			if err != nil {
				return "", nodeCommand{}, noop, err
			}
			return tmp, nodeCommand{Script: Localize(script)}, func() { os.RemoveAll(filepath.Dir(tmp)) }, nil
		}
		return repoPath, nodeCommand{Script: Localize(script)}, noop, nil
	}

	if info, statErr := os.Stat(filepath.Join(overlay, "deploy_local.sh")); statErr == nil && !info.IsDir() {
		tmp, err := r.overlayWorkspace(repoPath, overlay)
		if err != nil {
			return "", nodeCommand{}, noop, err
		}
		// A full override: the generated command is replaced, so it is not
		// localized either.
		return tmp, nodeCommand{Script: "bash src/local/deploy_local.sh", Override: true}, func() { os.RemoveAll(filepath.Dir(tmp)) }, nil
	}

	if overlayHasToolFiles(overlay) || isolate {
		tmp, err := r.overlayWorkspace(repoPath, overlay)
		if err != nil {
			return "", nodeCommand{}, noop, err
		}
		return tmp, nodeCommand{Script: Localize(script)}, func() { os.RemoveAll(filepath.Dir(tmp)) }, nil
	}
	return repoPath, nodeCommand{Script: Localize(script)}, noop, nil
}

func overlayHasToolFiles(overlay string) bool {
	for _, tool := range []string{"cdktf", "helm"} {
		if info, err := os.Stat(filepath.Join(overlay, tool)); err == nil && info.IsDir() {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(overlay, "config_local.json")); err == nil && !info.IsDir() {
		return true
	}
	return false
}

// overlayWorkspace copies the repo and lays its src/local alternates on top:
//
//	src/local/cdktf/*           -> src/cdktf/
//	src/local/helm/*            -> src/helm/
//	src/local/config_local.json -> meta-data/config_local.json
func (r *Runner) overlayWorkspace(repoPath, overlay string) (string, error) {
	root, err := os.MkdirTemp(r.Config.workDir(), "nsctl-overlay-")
	if err != nil {
		return "", fmt.Errorf("creating an overlay workspace: %w", err)
	}
	workspace := filepath.Join(root, filepath.Base(strings.TrimRight(repoPath, "/")))
	if err := copyTree(repoPath, workspace); err != nil {
		os.RemoveAll(root)
		return "", fmt.Errorf("copying %s: %w", repoPath, err)
	}

	// An empty overlay is a plain isolated copy: the caller wanted the tree
	// somewhere writable, not an overlay. Falling through would stat "cdktf"
	// and "helm" as relative paths against the process's own directory.
	if overlay == "" {
		return workspace, nil
	}

	var applied []string
	for _, tool := range []string{"cdktf", "helm"} {
		src := filepath.Join(overlay, tool)
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		if err := copyTree(src, filepath.Join(workspace, "src", tool)); err != nil {
			os.RemoveAll(root)
			return "", fmt.Errorf("overlaying src/local/%s: %w", tool, err)
		}
		applied = append(applied, "src/local/"+tool+" -> src/"+tool)
	}
	cfg := filepath.Join(overlay, "config_local.json")
	if info, err := os.Stat(cfg); err == nil && !info.IsDir() {
		dest := filepath.Join(workspace, "meta-data", "config_local.json")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err == nil {
			if err := copyFile(cfg, dest); err == nil {
				applied = append(applied, "src/local/config_local.json -> meta-data/config_local.json")
			}
		}
	}
	if len(applied) > 0 {
		r.step("    applied the local overlay: %s", strings.Join(applied, ", "))
	}
	return workspace, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if info.IsDir() && overlayIgnore[info.Name()] && rel != "." {
			return filepath.SkipDir
		}
		if strings.HasSuffix(info.Name(), ".egg-info") && info.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Skip rather than follow: a symlink out of the tree would copy
			// something the deploy has no business seeing.
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFileMode(path, target, info.Mode())
	})
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return copyFileMode(src, dst, info.Mode())
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// submitProducedResources posts the NERD0004 Resources a deploy rendered.
//
// hmd-cli-helm writes them under meta-data/resources_output/ during the deploy,
// so they are collected from the runner while the (possibly temporary)
// workspace still exists. Best effort: a Resource that fails to submit is worth
// a warning, not a failed deploy that actually succeeded.
func (r *Runner) submitProducedResources(ctx context.Context, workspace string, node msdeploy.DeploymentNode) int {
	dir := filepath.Join(workspace, "meta-data", "resources_output")
	entries, _ := os.ReadDir(dir)
	// Every file is collected into one submission. submit_resources takes the
	// RepoInstanceDeployment the Resources belong to plus a list -- posting a
	// bare document answers 500, because the id it keys them by is missing.
	var resources []any
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			r.warn("%s produced an unreadable resource file %s", node.InstanceName, entry.Name())
			continue
		}
		// hmd-cli-helm writes one document per Resource, but the legacy shape
		// is a list; both are accepted.
		if list, ok := doc.([]any); ok {
			resources = append(resources, list...)
			continue
		}
		resources = append(resources, doc)
	}

	// The legacy single-file shape, still written by some repos.
	if data, err := os.ReadFile(filepath.Join(workspace, "meta-data", "resources_output.json")); err == nil {
		var doc any
		if err := json.Unmarshal(data, &doc); err == nil {
			if list, ok := doc.([]any); ok {
				resources = append(resources, list...)
			} else {
				resources = append(resources, doc)
			}
		}
	}

	if len(resources) == 0 {
		return 0
	}
	// Recorded before submission, and independently of it: a node whose
	// resources could not be posted still produced them, and the local copy is
	// what `env credentials` reads.
	r.recordProducedResources(node.InstanceName, resources)

	if r.Client == nil || node.RIDNid == "" {
		return 0
	}
	if _, err := r.Client.APIOp(ctx, "submit_resources", map[string]any{
		"repo_instance_deployment_id": node.RIDNid,
		"resources":                   resources,
	}); err != nil {
		r.warn("could not submit the resources %s produced: %v", node.InstanceName, err)
		return 0
	}
	return len(resources)
}

// recordProducedResources keeps a node's resource outputs beside the
// environment. Best effort and quiet on failure: the copy is a convenience for
// a later read, and a deploy that worked must not be reported as failed because
// a cache write did not.
func (r *Runner) recordProducedResources(instance string, resources []any) {
	if r.OutputDir == "" || instance == "" {
		return
	}
	if err := os.MkdirAll(r.OutputDir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(resources, "", "  ")
	if err != nil {
		return
	}
	// Whole-file replacement: a node redeploys as a unit, so its previous
	// outputs are superseded rather than merged with.
	_ = os.WriteFile(filepath.Join(r.OutputDir, instance+".json"), data, 0o644)
}
