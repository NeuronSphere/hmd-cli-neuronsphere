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
)

//go:embed services/*.yml runner/*.tar.gz
var files embed.FS

// ControlPlaneComposeFile is the compose file describing the control plane:
// hmd_proxy, floci and the Deployment GUI.
const ControlPlaneComposeFile = "services/docker-compose.control-plane.yml"

// RunnerSourceArchive is this module's own sources, gzipped, which the
// DAG-runner image is built from.
//
// Inside that image it is an *empty* archive: the file has to exist for the
// //go:embed above to compile, but the sources cannot contain themselves. A
// binary running as the runner therefore cannot build a further runner image,
// which is the right answer rather than a missing feature.
const RunnerSourceArchive = "runner/source.tar.gz"

// Read returns one bundled file.
func Read(name string) ([]byte, error) { return files.ReadFile(name) }

// FS exposes the embedded tree for callers that want to walk it.
func FS() fs.FS { return files }
