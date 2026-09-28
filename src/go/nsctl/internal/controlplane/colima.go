package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// colimaSocketMarker identifies a Colima endpoint by the path it serves on.
// Colima's socket lives at ~/.colima/<profile>/docker.sock, and the profile
// directory beside it holds the configuration this file reads.
const colimaSocketMarker = "/.colima/"

// colimaForwarderLine matches the one setting that decides whether a UDP
// listener published on the host is reachable from macOS.
var colimaForwarderLine = regexp.MustCompile(`(?m)^\s*portForwarder\s*:\s*(\S+)`)

// ColimaUDPUnreachable explains why a UDP listener on the host cannot be
// reached, when the engine is Colima using its default port forwarder.
//
// It returns "" when this is not that situation, so a caller can append it to a
// message unconditionally.
//
// Colima's default `portForwarder: ssh` carries TCP only. Every TCP port the
// platform publishes works, so the engine looks entirely healthy -- and the one
// UDP listener, the local DNS resolver, is silently unreachable from the Mac
// while answering perfectly inside the VM. The resulting message told a
// first-run user their resolver was not running and to start the control plane,
// which was already running: a correct observation, the wrong cause, and a
// remedy that could not work. This exists so that message names the real one.
func ColimaUDPUnreachable(endpointHost string) string {
	profile := colimaProfileDir(endpointHost)
	if profile == "" {
		return ""
	}
	// An unreadable or absent config is Colima's default, which is `ssh`.
	forwarder := "ssh"
	if b, err := os.ReadFile(filepath.Join(profile, "colima.yaml")); err == nil {
		if m := colimaForwarderLine.FindSubmatch(b); m != nil {
			forwarder = strings.Trim(string(m[1]), `"'`)
		}
	}
	if forwarder != "ssh" {
		return ""
	}
	return "This engine is Colima with `portForwarder: ssh`, which forwards TCP only, so a UDP\n" +
		"  listener published on the host is unreachable from macOS however healthy it is inside\n" +
		"  the VM -- which is what the local resolver is. Set `portForwarder: grpc` in\n" +
		"  " + filepath.Join(profile, "colima.yaml") + " and run `colima stop && colima start`."
}

// colimaProfileDir returns the Colima profile directory behind an endpoint, or
// "" when the endpoint is not Colima's.
//
// The whole path matters, not the part from the marker onwards: the profile
// lives under the user's home, so slicing at "/.colima/" would produce a path
// that exists for nobody.
func colimaProfileDir(endpointHost string) string {
	path := strings.TrimPrefix(endpointHost, "unix://")
	if !strings.Contains(path, colimaSocketMarker) {
		return ""
	}
	return filepath.Dir(path)
}

// ColimaUDPNotice resolves the engine nsctl would use and explains, if it is
// Colima with the default port forwarder, why a UDP listener on the host is
// unreachable. It returns "" when that is not the situation.
//
// The wrapper exists so a command can append the explanation without resolving
// an endpoint itself; getting the endpoint wrong is the defect NERD021 is about.
func ColimaUDPNotice(ctx context.Context, opts *Options) string {
	return ColimaUDPUnreachable(currentEndpointHost(ctx, opts))
}
