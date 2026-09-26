package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/dockerhost"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/doctor"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
)

// bridgeProbeImage is the throwaway container the probe runs in. The same nginx
// the control plane's proxy and every environment router use, so nothing new is
// ever pulled -- and nothing is pulled here at all: an absent image is no row
// (NERD028 SPEC003).
//
// Deliberately not the k3s wrapper the start path probes with. That one is the
// right image where a cluster is being built and is certain to be present
// there; here it would be ~200 MB of image that a machine may not have, to
// answer a question this 20 MB one answers identically.
const bridgeProbeImage = "nginx:stable-alpine"

// bridgeProbeTimeout bounds a `cat` in a container whose image is already
// cached. It is generous by an order of magnitude; the case it exists for is an
// engine that has stopped answering, which the rows above already report.
const bridgeProbeTimeout = 20 * time.Second

// BridgeChecks reports whether the engine's kernel applies netfilter to bridged
// frames.
func BridgeChecks(ctx context.Context, ep dockerhost.Endpoint) []doctor.Check {
	ctx, cancel := context.WithTimeout(ctx, bridgeProbeTimeout)
	defer cancel()
	return bridgeCheck(ctx, container.New(), ep.Host)
}

// bridgeCheck is the pure decision: every row it returns is a function of what
// the runner said.
func bridgeCheck(ctx context.Context, d floci.DockerRunner, host string) []doctor.Check {
	// No image, no row. A preflight that pulls cannot run offline, and an
	// offline machine is not a misconfigured one.
	if _, _, err := d.Run(ctx, floci.HostArgs(host,
		"image", "inspect", "-f", "{{.Id}}", bridgeProbeImage)...); err != nil {
		return nil
	}

	c := doctor.Check{Name: "bridge netfilter"}
	answer, err := floci.ProbeBridgeNetfilter(ctx, d, host, bridgeProbeImage)
	if err != nil {
		c.Status = doctor.StatusWarn
		c.Detail = "could not ask the engine whether br_netfilter is loaded: " + err.Error()
		return []doctor.Check{c}
	}

	switch {
	case answer == "absent":
		c.Status = doctor.StatusWarn
		c.Detail = "the kernel behind this engine has no br_netfilter loaded, so bridged frames " +
			"bypass netfilter. Every pod on the local cluster shares one bridge, so a reply to a " +
			"ClusterIP comes back with the pod's own address and is dropped: in-cluster DNS, every " +
			"Service call, ingress-backed interfaces and the LoadBalancer ports all fail while the " +
			"node still reports Ready. Modules are host-wide, so nothing running in a container can " +
			"load it for you."
		c.Remedy = "nsctl loads it itself when it builds a cluster. To do it by hand on Colima:\n" +
			"  colima ssh -- sudo modprobe br_netfilter\n" +
			"It does not survive `colima stop`. Docker Desktop's kernel loads it already."
	case answer == "present:0":
		// A different fault with a different fix, so never the same row: this
		// one needs a sysctl, not a module, and k3s will most likely set it
		// before anything notices.
		c.Status = doctor.StatusWarn
		c.Detail = "br_netfilter is loaded, but net.bridge.bridge-nf-call-iptables is 0 in a fresh " +
			"network namespace, so bridged frames bypass netfilter. k3s sets this for its own " +
			"namespace at boot and will probably recover; it is reported because nothing else would " +
			"say so."
		c.Remedy = "colima ssh -- sudo sysctl -w net.bridge.bridge-nf-call-iptables=1 " +
			"net.bridge.bridge-nf-call-ip6tables=1"
	case answer == "unreadable":
		c.Status = doctor.StatusWarn
		c.Detail = "could not read " + floci.BridgePath + " on this engine, so whether br_netfilter " +
			"is loaded is unknown. A cluster whose Services do not answer is the thing to suspect."
	case strings.HasPrefix(answer, "present:"):
		c.Status = doctor.StatusOK
		c.Detail = "br_netfilter is loaded; bridged frames reach iptables"
	default:
		// Reading an answer nobody planned for as "fine" would be worse than
		// not asking.
		c.Status = doctor.StatusWarn
		c.Detail = fmt.Sprintf("could not ask the engine whether br_netfilter is loaded: "+
			"unexpected answer %q", answer)
	}
	return []doctor.Check{c}
}
