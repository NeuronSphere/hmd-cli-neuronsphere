package floci

import (
	"context"
	"strings"
)

// BridgePath is the sysctl whose existence is the whole question.
//
// br_netfilter registers it per network namespace when it is loaded, and in no
// namespace when it is not -- so "does this file exist" and "is the module
// loaded" are the same question, asked in the one place a container can reach.
//
// Without the module, bridged frames bypass netfilter and conntrack. Every pod
// on a single-node cluster shares the cni0 bridge, so a pod dialling a ClusterIP
// is DNAT'd on the way in and the reply is forwarded back at layer 2 carrying
// the backend pod's own address rather than being reverse-NAT'd -- and the
// client drops it. Cluster DNS is the first casualty, then every Service call,
// while the node reports Ready throughout (NERD028).
const BridgePath = "/proc/sys/net/bridge/bridge-nf-call-iptables"

// BridgeProbeScript emits exactly one of `present:<value>`, `absent` or
// `unreadable`, and always exits 0.
//
// Always exiting 0 is what lets a non-zero exit mean one unambiguous thing --
// the engine would not run a container -- instead of being a second spelling of
// "the file is missing" that the caller would have to tell apart by parsing
// whatever busybox chose to say. `unreadable` is a separate answer from
// `absent` because a read refused for permission on a user-namespaced daemon is
// not a missing module, and reporting it as one would be a false red.
const BridgeProbeScript = `p=` + BridgePath + `
if [ ! -e "$p" ]; then echo absent; exit 0; fi
v=$(cat "$p" 2>/dev/null) || { echo unreadable; exit 0; }
[ -n "$v" ] || { echo unreadable; exit 0; }
echo "present:$v"`

// DockerRunner is the one thing the kernel probe needs from an engine, so the
// decision can be tested without one.
type DockerRunner interface {
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// HostArgs prepends docker's global --host so a command runs on a named
// endpoint rather than on whatever DOCKER_HOST or the current context resolves.
//
// The two differ on exactly the machines these checks matter on, which is the
// defect NERD021 exists to prevent. An empty host leaves the arguments alone,
// for callers that have no resolved endpoint to insist on.
func HostArgs(host string, rest ...string) []string {
	if host == "" {
		return rest
	}
	return append([]string{"--host", host}, rest...)
}

// ProbeBridgeNetfilter asks the engine's kernel whether it filters bridged
// frames, returning one of `present:<value>`, `absent` or `unreadable`.
//
// The container gets a fresh network namespace, deliberately: these sysctls are
// per-namespace, and a fresh one is what the k3s container gets -- so this
// samples the value k3s will see rather than the engine host's, which is a
// different question with a different answer.
func ProbeBridgeNetfilter(ctx context.Context, d DockerRunner, host, image string) (string, error) {
	stdout, stderr, err := d.Run(ctx, HostArgs(host,
		"run", "--rm", "--network", "none", "--pull", "never",
		"--entrypoint", "sh", image, "-c", BridgeProbeScript)...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return "", &BridgeProbeError{Msg: msg}
	}
	return strings.TrimSpace(string(stdout)), nil
}

// BridgeProbeError is an engine that would not run the probe at all, which is a
// different finding from any answer the probe could have given.
type BridgeProbeError struct{ Msg string }

func (e *BridgeProbeError) Error() string { return e.Msg }

// ClusterBridgeNetfilter reads the sysctl inside a running container's own
// network namespace.
//
// This is the authoritative reading, and the only one that decides whether a
// cluster's Services work. These values are per-namespace, and the k3s
// container has its own: setting the sysctl on the engine's host changes
// nothing for the cluster, and reading it there would report a working engine
// as broken and a broken one as fine. The cni0 bridge every pod hangs off lives
// in this namespace, so this is where the question has to be asked.
//
// Measured during NERD028's acceptance run, where a host-namespace value of 0
// left a live cluster resolving names perfectly and the same 0 set inside the
// container broke every ClusterIP immediately.
func ClusterBridgeNetfilter(ctx context.Context, d K3sExecer, name string) (string, error) {
	out, err := d.Exec(ctx, name, "sh", "-c", BridgeProbeScript)
	if err != nil {
		return "", &BridgeProbeError{Msg: strings.TrimSpace(err.Error())}
	}
	return strings.TrimSpace(string(out)), nil
}

// BridgeState is what EnsureBridgeNetfilter found, or did.
type BridgeState int

const (
	// BridgeAlreadyLoaded means there was nothing to do.
	BridgeAlreadyLoaded BridgeState = iota
	// BridgeLoaded means the module was missing and we loaded it.
	BridgeLoaded
	// BridgeUnavailable means it is missing and could not be loaded here.
	BridgeUnavailable
	// BridgeUnknown means the engine would not answer. Never treated as a
	// problem: a question docker did not answer is not a diagnosis.
	BridgeUnknown
	// BridgeSkipped means the caller opted out.
	BridgeSkipped
)

// EnsureBridgeNetfilter loads br_netfilter on the engine's kernel when it is
// missing, using a privileged one-shot container that carries the module tree.
//
// Modules are host-wide, so loading one from any container loads it for the
// whole machine -- which is the only reason this can work from here at all. The
// k3s container is itself already privileged and would load the module on its
// own if Floci bound /lib/modules into it; Floci has no knob for that, so the
// platform does it one container over (NERD028 SPEC007).
//
// The image is the k3s wrapper: it is the image that needs the module, it ships
// a modprobe, and it is present by definition wherever a cluster is being built.
//
// Nothing here fails a start. A kernel that cannot be prepared falls through to
// the doctor row and, at the point it actually matters, to the wrapper image's
// own refusal -- both of which name the remedy. A failed repair is not a failed
// start.
func EnsureBridgeNetfilter(ctx context.Context, d DockerRunner, host, image string, optOut bool) (BridgeState, error) {
	if optOut {
		return BridgeSkipped, nil
	}
	answer, err := ProbeBridgeNetfilter(ctx, d, host, image)
	if err != nil {
		return BridgeUnknown, err
	}
	if answer != "absent" {
		// Includes `present:0`, which is not this function's business: k3s sets
		// the value for its own namespace at boot, and the module -- the part
		// only the host can supply -- is already there.
		return BridgeAlreadyLoaded, nil
	}

	// Privileged and host-networked because loading a module is a host
	// operation; /lib/modules read-only because that is where the module lives
	// and nothing here should be able to write it. On an engine whose VM ships
	// no module tree this simply fails, which is a fall-through, not an error.
	_, stderr, runErr := d.Run(ctx, HostArgs(host,
		"run", "--rm", "--privileged", "--network", "host",
		"-v", "/lib/modules:/lib/modules:ro", "--pull", "never",
		"--entrypoint", "modprobe", image, "br_netfilter")...)
	if runErr != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = runErr.Error()
		}
		return BridgeUnavailable, &BridgeProbeError{Msg: msg}
	}

	// Ask again rather than trust the exit code: a modprobe that reports
	// success and leaves the sysctl absent would otherwise be recorded as a fix.
	switch again, err := ProbeBridgeNetfilter(ctx, d, host, image); {
	case err != nil:
		return BridgeUnknown, err
	case again == "absent":
		return BridgeUnavailable, nil
	default:
		return BridgeLoaded, nil
	}
}
