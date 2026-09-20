// Package bundled embeds the files nsctl ships inside the binary.
//
// The compose file is not duplicated here. It lives where it always has, at
// src/python/hmd_cli_neuronsphere/services/, because the Python CLI reads the
// same file and two copies would be one copy and one stale copy. The
// repository-root Makefile stages it into services/ (gitignored) before every
// build and test, and go:embed picks it up from there.
//
// SPEC006 says the same of the pre_build_artifacts trees: the mechanism is
// unchanged, only the unpack destination moves.
//
// The runner sources are here for a different reason. The DAG-runner image is
// not published anywhere -- nsctl builds it on demand -- and the Dockerfile
// that builds it compiles this module from source, which a `brew install`ed
// binary does not have. tools/runnersrc packs that source, `make generate`
// runs it, and controlplane.EnsureRunnerImage unpacks it into a build context.
package bundled

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed services/*.yml image/*.tar.gz repos/*.tar.gz
var files embed.FS

// ControlPlaneComposeFile is the compose file describing the control plane:
// hmd_proxy, floci and the Deployment GUI.
const ControlPlaneComposeFile = "services/docker-compose.control-plane.yml"

// NsctlSourceArchive is this module's own sources, gzipped, which the nsctl
// image (the identity provider, authd, runs from it) is built from.
//
// Inside that image it is an *empty* archive: the file has to exist for the
// //go:embed above to compile, but the sources cannot contain themselves. A
// binary running inside the image therefore cannot build a further image,
// which is the right answer rather than a missing feature.
const NsctlSourceArchive = "image/source.tar.gz"

// RepoArchive is the packed working tree for a repo class, or false when this
// binary carries none.
//
// A deploy node bind-mounts a repo's tree into a sibling projectbuilder, so a
// machine with no $HMD_REPO_HOME could not deploy anything at all: the
// substrate's first node failed with "no working tree for hmd-vpc". These are
// what nsctl unpacks to stand in, which is what makes "a single binary whose
// only prerequisite is Docker" true of more than `control-plane start`.
func RepoArchive(repoClass string) ([]byte, bool) {
	if repoClass == "" {
		return nil, false
	}
	data, err := files.ReadFile("repos/" + repoClass + ".tar.gz")
	if err != nil {
		return nil, false
	}
	return data, true
}

// RepoClasses lists every repo class this binary carries a tree for.
func RepoClasses() []string {
	entries, err := fs.ReadDir(files, "repos")
	if err != nil {
		return nil
	}
	classes := make([]string, 0, len(entries))
	for _, e := range entries {
		classes = append(classes, strings.TrimSuffix(e.Name(), ".tar.gz"))
	}
	sort.Strings(classes)
	return classes
}

// Read returns one bundled file.
func Read(name string) ([]byte, error) { return files.ReadFile(name) }

// FS exposes the embedded tree for callers that want to walk it.
func FS() fs.FS { return files }
