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
package bundled

import (
	"embed"
	"io/fs"
)

//go:embed services/*.yml
var files embed.FS

// ControlPlaneComposeFile is the compose file describing the control plane:
// hmd_proxy, floci and the Deployment GUI.
const ControlPlaneComposeFile = "services/docker-compose.control-plane.yml"

// Read returns one bundled file.
func Read(name string) ([]byte, error) { return files.ReadFile(name) }

// FS exposes the embedded tree for callers that want to walk it.
func FS() fs.FS { return files }
