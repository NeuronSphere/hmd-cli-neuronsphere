package bom

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
)

// configLocalPath is where a repo keeps the configuration it wants when it is
// deployed locally, relative to the working tree.
const configLocalPath = "meta-data/config_local.json"

// configLocalWiringKeys are the keys a repo's config_local.json carries for a
// direct `hmd deploy --local`, where nothing else says who the instance is or
// what it depends on. Under nsctl the environment manifest says both, and
// merging these would rename the instance and replace the dependencies nsctl
// bound -- so they are dropped before the merge, not after.
var configLocalWiringKeys = []string{"instance_name", "deployment_id", "dependencies", "details"}

// DeclaredWithLocalDefaults is Declared with each local-source repo's
// meta-data/config_local.json laid beneath the instance's own configuration.
//
// The order is the one a deploy already has: the class's default_configuration,
// then config_local.json, then whatever the manifest sets on the instance, so
// an explicit value always wins. Only a repo deployed from a working tree has a
// file to read; an artifact has none and is left as declared.
//
// An absent file is normal. One that cannot be read or parsed is reported
// through warn and ignored, because a repo whose local defaults silently stop
// applying is a harder thing to find than a warning.
func DeclaredWithLocalDefaults(env Environment, repos []manifest.Repo, lookup manifest.Lookup, warn func(format string, args ...any)) []Entry {
	entries := Declared(env, repos)
	for i, r := range repos {
		root := r.RepoPath(lookup)
		if root == "" {
			continue
		}
		defaults, err := loadConfigLocal(root)
		if err != nil {
			warn("%s: ignoring %s: %v", r.InstanceName, configLocalPath, err)
			continue
		}
		if len(defaults) == 0 {
			continue
		}
		entries[i].InstanceConfiguration = mergeConfig(defaults, entries[i].InstanceConfiguration)
	}
	return entries
}

// loadConfigLocal reads a working tree's config_local.json with the identity
// and wiring keys removed. A tree without one yields nil.
func loadConfigLocal(root string) (map[string]any, error) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configLocalPath)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	for _, key := range configLocalWiringKeys {
		delete(config, key)
	}
	return config, nil
}

// mergeConfig returns base with over laid on top: maps merge key by key, any
// other value in over replaces the one in base. Neither argument is modified.
func mergeConfig(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = cloneValue(v)
	}
	for k, v := range over {
		overMap, overIsMap := v.(map[string]any)
		baseMap, baseIsMap := out[k].(map[string]any)
		if overIsMap && baseIsMap {
			out[k] = mergeConfig(baseMap, overMap)
			continue
		}
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue copies the maps and slices a decoded config is made of, so a
// merged result shares nothing with either input.
func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = cloneValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return v
	}
}
