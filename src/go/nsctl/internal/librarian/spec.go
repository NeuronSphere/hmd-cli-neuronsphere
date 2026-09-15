// Package librarian reads and writes build artifacts in an HMD Artifact Librarian.
//
// It exists so the Go build can obtain the trees nsctl embeds without a
// committed copy of them. BACON declares those trees as
// build.pre_build_artifacts, `hmd build` unpacks them into
// src/python/hmd_cli_neuronsphere/external/, and tools/repopack packs them --
// but `make generate` also runs from GoReleaser's before.hooks on a CI runner
// with no `hmd` installed, and that is the case this package serves.
//
// It is a port of three small Python pieces and deliberately nothing more:
// hmd_lib_librarian_client.artifact_tools (the endpoint, the content-path
// grammar), hmd_cli_tools.okta_tools.get_auth_token (the credential, which is
// not an Okta flow -- it is two environment variables and a YAML file that `hmd
// login` writes), and HmdLibrarianClient.put_file (the three-leg upload).
//
// Publishing to a *cloud* librarian has no counterpart here on purpose: `hmd
// build` with HMD_AUTO_PUBLISH is the one way a shared store is written, and
// NERD005 SPEC010 records the same boundary. Put and NewLocal add the other
// direction only -- writing to the control plane's own librarian on loopback,
// which is what makes a developer's build deployable without the cloud being
// involved at any point.
package librarian

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Spec is a parsed pre_build_artifacts source string.
//
// Note that Name is the *repo class* -- `hmd-inf-ext-secrets`, not the
// `ext-secrets` directory the manifest unpacks it into. The two diverge for
// every aliased artifact, and keying anything off the directory is the bug
// repopack's manifestIndex and bom_seeder._artifact_version_index both exist
// to avoid.
type Spec struct {
	Name     string
	Version  string
	ItemType string
	// FilePart is the optional fourth field. Nothing in this repository's
	// manifest uses one; it is parsed so a spec that carries one is rejected
	// by whoever cannot honour it rather than silently truncated.
	FilePart string
}

// ParseSpec reads `<name>@<version>:<item_type>{:<file_part>}`, the grammar
// content_item_path_from_spec parses. BACON's pre_build_artifacts and the
// librarian's content paths are one vocabulary, not two compatible ones.
func ParseSpec(spec string) (Spec, error) {
	at := strings.Index(spec, "@")
	if at <= 0 {
		return Spec{}, fmt.Errorf("artifact spec %q: expected <name>@<version>:<item_type>", spec)
	}
	rest := spec[at+1:]
	parts := strings.Split(rest, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Spec{}, fmt.Errorf("artifact spec %q: expected <name>@<version>:<item_type>", spec)
	}
	s := Spec{Name: spec[:at], Version: parts[0], ItemType: parts[1]}
	if len(parts) > 2 {
		s.FilePart = strings.Join(parts[2:], ":")
	}
	return s, nil
}

// ContentPath is the librarian address of the artifact, built the way
// content_item_path_from_parts builds it.
func (s Spec) ContentPath() string {
	return fmt.Sprintf("repository:/%s/%s/%s_%s_%s.zip", s.Name, s.Version, s.Name, s.Version, s.ItemType)
}

// String renders the spec back, for error messages that should name what was
// asked for rather than the address it was translated into.
func (s Spec) String() string { return s.Name + "@" + s.Version + ":" + s.ItemType }

// PreBuildArtifacts reads build.pre_build_artifacts out of a BACON manifest.
//
// The specs are returned in declaration order and keyed by nothing: the <name>
// is the repo class and the destination beside it is the plugin alias, and a
// caller that wants a map should say which of the two it is keying on. Getting
// that wrong silently mismatches every aliased artifact, which is the bug
// repopack's manifestIndex and bom_seeder._artifact_version_index both exist to
// avoid.
func PreBuildArtifacts(manifestJSON []byte) ([]Spec, error) {
	var m struct {
		Build struct {
			PreBuildArtifacts [][]json.RawMessage `json:"pre_build_artifacts"`
		} `json:"build"`
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}
	var specs []Spec
	for _, entry := range m.Build.PreBuildArtifacts {
		if len(entry) == 0 {
			continue
		}
		var raw string
		if json.Unmarshal(entry[0], &raw) != nil {
			continue
		}
		spec, err := ParseSpec(raw)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
