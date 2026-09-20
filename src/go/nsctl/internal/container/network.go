// Package container talks to Docker. nsctl shells out to the docker CLI for
// the inspection-shaped work here; the Engine API is used where creating and
// reconciling containers makes parsing CLI output fragile (internal/compose).
package container

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// DefaultNetworkName is the local NeuronSphere network's name when HMD_HOME is
// unset. With HMD_HOME set, a per-home hash is appended so two HMD_HOMEs on one
// machine never share containers, volumes or networks.
const DefaultNetworkName = "neuronsphere_default"

// DefaultComposeProject is the control plane's compose project name, likewise
// suffixed with the HMD_HOME hash when there is one.
const DefaultComposeProject = "local_neuronsphere"

// HMDHomeHash mirrors env_registry._hmd_home_hash and
// floci_deployer._HMD_HOME_HASH: the first 8 hex digits of the SHA-256 of the
// absolute HMD_HOME path.
//
// It uses filepath.Abs and deliberately NOT filepath.EvalSymlinks. Python's
// os.path.abspath is normpath(join(cwd, p)) and does not resolve symlinks, so
// resolving here would compute a different name than every other HMD tool and
// quietly put nsctl's containers on a network of their own.
//
// This is a FALLBACK. SPEC003 requires reading the persisted registry rather
// than re-deriving any of it -- the algorithm already exists in three
// independent copies (floci_deployer, hmd-cli-bender, and nsx's
// internal/container/network.go, which carries a "keep in sync" comment
// counting them) and a fourth would be the one nobody updates. It is here only
// for an HMD_HOME that has never been bootstrapped, where there is nothing
// persisted to read.
func HMDHomeHash(hmdHome string) string {
	if hmdHome == "" {
		return ""
	}
	abs, err := filepath.Abs(hmdHome)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:8]
}

// DefaultNetwork is the network name for an HMD_HOME with no persisted one.
func DefaultNetwork(hmdHome string) string {
	if h := HMDHomeHash(hmdHome); h != "" {
		return DefaultNetworkName + "-" + h
	}
	return DefaultNetworkName
}

// DefaultProject is the control-plane compose project for an HMD_HOME with no
// persisted one.
func DefaultProject(hmdHome string) string {
	if h := HMDHomeHash(hmdHome); h != "" {
		return DefaultComposeProject + "-" + h
	}
	return DefaultComposeProject
}
