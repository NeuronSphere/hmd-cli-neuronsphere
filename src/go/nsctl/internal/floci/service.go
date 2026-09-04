package floci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
)

// ImagePresence reports whether an image reference is in the local cache.
type ImagePresence interface {
	ImagePresent(ctx context.Context, ref string) bool
}

// ServiceImage finds the image to run a foundation service from.
//
// Local only, deliberately. Floci spawns Lambda containers through the mounted
// host docker.sock, so any image in the host's cache is usable directly, and a
// registry round trip would make a start depend on the network.
func ServiceImage(ctx context.Context, d ImagePresence, repoClass, version string, lookup func(string) string) (string, error) {
	var candidates []string
	for _, key := range []string{"HMD_CONTAINER_REGISTRY", "HMD_LOCAL_NS_CONTAINER_REGISTRY"} {
		if prefix := lookup(key); prefix != "" {
			candidates = append(candidates, prefix+"/"+repoClass+":"+version)
		}
	}
	candidates = append(candidates, repoClass+":"+version)

	for _, ref := range candidates {
		if d.ImagePresent(ctx, ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf(
		"no local image for %s@%s (looked for %v). Build it with `hmd build` in that repo, or set %s to a version you have",
		repoClass, version, candidates, repoclass.VersionEnvVar(repoClass))
}

// ServiceConfig reads a repo's meta-data/config_local.json.
//
// Absent or malformed yields nil: a service without one is the ordinary case,
// and the caller supplies the rest of the environment either way.
func ServiceConfig(repoDir string) map[string]any {
	if repoDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "meta-data", "config_local.json"))
	if err != nil {
		return nil
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config
}

// PostgresMajor is the PostgreSQL major version of the image Floci is
// configured to spawn database backends from.
//
// Read rather than hardcoded so the version an instance declares and the
// binary that initialises its data directory cannot drift apart -- the drift
// that leaves a data directory a newer server refuses to start against.
//
// Returns "" when it cannot be determined, which a caller should treat as "do
// not declare one" rather than as a default.
func PostgresMajor(ctx context.Context, d ImageChecker, container string) string {
	return MajorFromImageRef(d.ContainerEnv(ctx, container)[rdsImageEnv])
}

// MajorFromImageRef reads the major version out of an image tag.
//
// hmd-postgres-base tags look like `<registry>/hmd-postgres-base:0.2.11`, and
// the leading component of the version is not the PostgreSQL major -- so a tag
// is only usable when it carries one explicitly, as `pg16` or `postgres-16`.
// Anything else yields "", because guessing here produces an engine_version
// that disagrees with the running binary.
func MajorFromImageRef(ref string) string {
	if ref == "" {
		return ""
	}
	tag := ref
	if at := strings.LastIndex(ref, ":"); at > strings.LastIndex(ref, "/") {
		tag = ref[at+1:]
	}
	for _, prefix := range []string{"pg", "postgres-", "postgresql-"} {
		if rest, found := strings.CutPrefix(tag, prefix); found {
			if major := leadingDigits(rest); major != "" {
				return major
			}
		}
	}
	return ""
}

func leadingDigits(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	if _, err := strconv.Atoi(s[:end]); err != nil {
		return ""
	}
	return s[:end]
}
