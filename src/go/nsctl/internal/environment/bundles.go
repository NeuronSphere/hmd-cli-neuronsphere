package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/localspec"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/msdeploy"
)

// recordBundles tells the control plane which bundles this environment holds
// (NERD0019 SPEC0003): it registers each bundle's declaration and attaches it
// to the environment, so "which bundle put this instance here" is answered by
// the graph rather than by this machine's manifest.
//
// Best effort, after a successful apply: the deploy already happened and is
// not undone by a record that could not be written. A control plane that
// predates the routes answers 404, which is said once per bundle and moved
// past.
func recordBundles(ctx context.Context, opts *Options, client *msdeploy.Client, envSlug string, records []manifest.BundleRecord) {
	for _, rec := range records {
		upsert, attach, ok, err := bundleRecording(opts.Home, envSlug, rec)
		if err != nil {
			opts.warn("bundle %s: %v", rec.Name, err)
			continue
		}
		if !ok {
			continue
		}
		result, err := client.APIOp(ctx, "upsert_bundle_versions", upsert)
		if err != nil {
			opts.warn("bundle %s: not recorded in the control plane: %v", rec.Name, err)
			continue
		}
		if failure := upsertFailure(result); failure != "" {
			opts.warn("bundle %s: not recorded in the control plane: %s", rec.Name, failure)
			continue
		}
		if _, err := client.APIOp(ctx, "attach_bundle_to_environment", attach); err != nil {
			opts.warn("bundle %s: not attached to %s in the control plane: %v", rec.Name, envSlug, err)
			continue
		}
		opts.step("  bundle %s %s recorded in %s", rec.Name, rec.Version, envSlug)
	}
}

// upsertFailure is the error upsert_bundle_versions reported for its one
// bundle, or "".
func upsertFailure(result map[string]any) string {
	rows, _ := result["results"].([]any)
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if row["status"] == "error" {
			return fmt.Sprint(row["error"])
		}
	}
	return ""
}

// bundleRecording is the two payloads recordBundles sends for one record: the
// upsert_bundle_versions body, and the attach_bundle_to_environment body.
// ok is false for a stack whose RepoClass ships no bundle file -- a pre-NERD0019
// `local.stack` repository -- which has no bundle to record.
func bundleRecording(home, envSlug string, rec manifest.BundleRecord) (upsert, attach map[string]any, ok bool, err error) {
	dir := artifact.Dir(home, rec.Class, rec.Version)
	if dir == "" {
		return nil, nil, false, nil
	}
	names, err := localspec.BundleNames(dir)
	if err != nil || len(names) == 0 {
		return nil, nil, false, err
	}
	file := names[0]
	for _, n := range names {
		if n == rec.Name {
			file = n
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, localspec.BundleDir, file+".json"))
	if err != nil {
		return nil, nil, false, err
	}
	var declaration map[string]any
	if err := json.Unmarshal(raw, &declaration); err != nil {
		return nil, nil, false, fmt.Errorf("parsing %s.json: %w", file, err)
	}
	bundleName, _ := declaration["bundle_name"].(string)
	if bundleName == "" {
		bundleName = file
		declaration["bundle_name"] = file
	}

	// The record's bindings are keyed by want: a provisioned role by its
	// instance_name when it has one, anything else by its role. The graph
	// keys them by role.
	roleOfKey := map[string]string{}
	roles, _ := declaration["roles"].(map[string]any)
	for role, v := range roles {
		decl, _ := v.(map[string]any)
		key := role
		if provision, _ := decl["provision"].(string); provision != "false" {
			if name, _ := decl["instance_name"].(string); name != "" {
				key = name
			}
		}
		roleOfKey[key] = role
	}
	bindings := map[string]string{}
	for key, instance := range rec.Bindings {
		if role, known := roleOfKey[key]; known {
			bindings[role] = instance
		}
	}
	declared := rec.Owned()
	sort.Strings(declared)

	upsert = map[string]any{
		"source_repo_class_name": rec.Class,
		"source_version":         rec.Version,
		"bundles":                []map[string]any{declaration},
	}
	attach = map[string]any{
		"environment": envSlug,
		"bundle_name": bundleName,
		"version":     rec.Version,
		"bindings":    bindings,
		"declared":    declared,
		"profiles":    append([]string{}, rec.Profiles...),
	}
	if rec.Ref != "" {
		attach["source_ref"] = rec.Ref
	}
	if rec.Digest != "" {
		// Installed from a published artifact, whose lock is a release of
		// this bundle at the same version (NERD0019 SPEC0004).
		attach["digest"] = rec.Digest
		attach["release_version"] = rec.Version
	}
	return upsert, attach, true, nil
}
