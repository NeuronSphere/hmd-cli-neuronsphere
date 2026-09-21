// Package plugin installs and runs declared CLI plugins. NERD018.
//
// A plugin is an executable named nsctl-<noun> that nsctl runs as one new
// top-level noun. It exists because $HMD_HOME/.config/nsctl.toml declares it
// (nsconfig.Plugin) and for no other reason: nothing here scans PATH, the
// cache, or anywhere else. Installing is fetching an OCI artifact (NERD016),
// choosing the layer for this platform, unpacking it under the cache, and
// writing the declaration; running is exec with the contract in SPEC005.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nsconfig"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/repoclass"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// Media types and annotation keys of the plugin artifact. SPEC002.
const (
	ArtifactType    = "application/vnd.neuronsphere.plugin.v1+json"
	BinaryMediaType = "application/vnd.neuronsphere.plugin.binary.v1.tar+gzip"

	AnnotationOS   = "io.neuronsphere.plugin.os"
	AnnotationArch = "io.neuronsphere.plugin.arch"
	// AnnotationTitle is the OCI-standard file name annotation.
	AnnotationTitle = "org.opencontainers.image.title"

	// DescriptorFile is the descriptor's name in a directory handed to push.
	DescriptorFile = "plugin.json"
)

// DefaultNamespace is where a bare plugin name expands to. Derived from the
// distribution registry, the same org stacks expand into, so the two cannot
// drift; a verb prints the expansion before fetching (NERD016 SPEC001).
var DefaultNamespace = repoclass.DistributionRegistry + "/plugins"

// Platform is one published binary.
type Platform struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Descriptor is the artifact's config blob.
type Descriptor struct {
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	Summary         string              `json:"summary,omitempty"`
	MinNsctlVersion string              `json:"min_nsctl_version,omitempty"`
	Platforms       map[string]Platform `json:"platforms"`
}

// ParseDescriptor decodes and validates a descriptor.
func ParseDescriptor(data []byte) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal(data, &d); err != nil {
		return Descriptor{}, fmt.Errorf("plugin descriptor: %w", err)
	}
	if err := nsconfig.ValidPluginName(d.Name); err != nil {
		return Descriptor{}, fmt.Errorf("plugin descriptor: %w", err)
	}
	if !versionspec.IsVersion(d.Version) {
		return Descriptor{}, fmt.Errorf("plugin descriptor: version %q is not a version", d.Version)
	}
	if d.MinNsctlVersion != "" && !versionspec.IsVersion(d.MinNsctlVersion) {
		return Descriptor{}, fmt.Errorf("plugin descriptor: min_nsctl_version %q is not a version", d.MinNsctlVersion)
	}
	return d, nil
}

// PlatformKey is this build's GOOS_GOARCH, the key into Platforms.
func PlatformKey() string { return runtime.GOOS + "_" + runtime.GOARCH }

// PlatformKeys lists the descriptor's platforms, sorted.
func (d Descriptor) PlatformKeys() []string {
	keys := make([]string, 0, len(d.Platforms))
	for k := range d.Platforms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrTooOld is a plugin whose min_nsctl_version is newer than this binary.
var ErrTooOld = errors.New("nsctl is too old for this plugin")

// CheckNsctlVersion refuses a plugin that needs a newer nsctl. A "dev" build
// has no version to compare and is let through with warn set.
func (d Descriptor) CheckNsctlVersion(nsctlVersion string) (warn string, err error) {
	if d.MinNsctlVersion == "" {
		return "", nil
	}
	if !versionspec.IsVersion(nsctlVersion) {
		return fmt.Sprintf("plugin %s needs nsctl >= %s; this is a %q build, so that is not checked",
			d.Name, d.MinNsctlVersion, nsctlVersion), nil
	}
	if versionspec.Compare(nsctlVersion, d.MinNsctlVersion) < 0 {
		return "", fmt.Errorf("%w: %s %s needs nsctl >= %s, this is %s",
			ErrTooOld, d.Name, d.Version, d.MinNsctlVersion, nsctlVersion)
	}
	return "", nil
}

// Root is where installed plugins live.
func Root(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "neuronsphere", "plugins")
}

// Dir is one installed version's directory.
func Dir(home, name, version string) string {
	if home == "" || name == "" || version == "" {
		return ""
	}
	return filepath.Join(Root(home), name+"@"+version)
}

// BinaryName is the executable's file name for a noun.
func BinaryName(name string) string { return "nsctl-" + name }

// Binary is the executable's path inside an installed directory.
func Binary(dir, name string) string { return filepath.Join(dir, BinaryName(name)) }
