// Package dnsd is a wildcard resolver for the local NeuronSphere suffix.
//
// It answers every name under one suffix with the loopback address and refuses
// everything else. That single property is what /etc/hosts cannot provide at
// any price: a hosts file has no wildcards, so it can only name hosts that
// already exist, and every new environment, extension or registry format is
// another privileged edit (NERD026).
//
// It is authoritative and forwards nothing, so it works with no network at all
// and exposes nothing. The suffix is deliberately a *subtree* --
// local.neuronsphere.io, not neuronsphere.io -- because the resolver
// configuration that points a machine here captures everything below the name
// it is filed under, and the broader name would capture the real public
// website.
//
// The wire format is encoded by hand rather than with golang.org/x/net/dns.
// That package is a fine one; requiring it pulls golang.org/x/net into the
// build, whose own go.mod forces golang.org/x/term up from the version this
// module pins for internal/tty. One question and one A record is a hundred
// lines, and a dependency bump on a repository where a push to main cuts a
// release is not worth saving them.
package dnsd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	// DefaultSuffix is the subtree this server owns.
	DefaultSuffix = "local.neuronsphere.io"
	// DefaultPort is where it listens on loopback.
	//
	// Not 53: binding a privileged port is the thing this whole design exists
	// to avoid. Not 5353 either, which is the obvious choice and does not work.
	// mDNS lives there, and on macOS it is not only mDNSResponder -- Chrome and
	// Spotify were both found holding *:5353 on a developer machine. They share
	// it with SO_REUSEPORT; Docker's port publisher does not set that, so
	// publishing 127.0.0.1:5353 fails with EADDRINUSE on a typical Mac. Checked
	// rather than assumed: a plain bind to 127.0.0.1:5353 was refused while the
	// same bind with SO_REUSEPORT succeeded.
	//
	// 19153 instead: above the 19000-19079 band hmd_proxy publishes -- a port
	// inside it could not be bound by a second container at all -- below the
	// 49152 ephemeral floor, so the OS never hands it out at random, and
	// leaving 19080-19152 as headroom if the published band ever grows.
	DefaultPort = 19153
	// PortEnv overrides it. Declared here, beside the port it overrides, so the
	// name is one constant rather than a literal repeated by each reader --
	// which is what lets a refusal that names it be tested against a real read
	// (NERD025 SPEC007).
	PortEnv = "HMD_LOCAL_DNS_PORT"
	// ttl is short, so a machine that stops pointing here recovers quickly.
	ttl = 60

	// probeTimeout is how long a status probe waits. Short: a resolver on
	// loopback answers immediately or is not there.
	probeTimeout = 500 * time.Millisecond
	// probeID is the query ID a probe uses. Fixed: one question, one answer, no
	// multiplexing to tell apart.
	probeID = 0x4e53

	headerLen = 12
	maxName   = 255

	typeA      = 1
	classINET  = 1
	rcodeOK    = 0
	rcodeRefus = 5

	flagResponse      = 0x8000
	flagAuthoritative = 0x0400
	flagRecursionReq  = 0x0100
)

// errMalformed is returned for a packet that is not a question. The caller
// drops it rather than answering: replying to garbage with a well-formed error
// is how a resolver becomes an amplifier.
var errMalformed = errors.New("malformed DNS query")

// Server answers for one suffix.
type Server struct {
	// Suffix is the subtree owned, without a trailing dot.
	Suffix string
	// Answer is the address every name under it resolves to.
	Answer [4]byte
}

// New builds a server for a suffix, answering on loopback.
func New(suffix string) *Server {
	return &Server{Suffix: strings.Trim(strings.ToLower(suffix), "."), Answer: [4]byte{127, 0, 0, 1}}
}

// owns reports whether a queried name falls inside the suffix.
//
// The dot is required, so `local.neuronsphere.io.evil.com` does not match and
// neither does a name that merely ends in the same letters.
func (s *Server) owns(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	return n == s.Suffix || strings.HasSuffix(n, "."+s.Suffix)
}

// question reads the single question from a query, returning the name, the
// type, and the offset just past the question section.
func question(raw []byte) (name string, qtype uint16, end int, err error) {
	if len(raw) < headerLen {
		return "", 0, 0, errMalformed
	}
	if binary.BigEndian.Uint16(raw[4:6]) != 1 {
		// Exactly one question. Zero is not a query; more than one is not
		// something any resolver in this path sends.
		return "", 0, 0, errMalformed
	}
	var labels []string
	i := headerLen
	for {
		if i >= len(raw) {
			return "", 0, 0, errMalformed
		}
		n := int(raw[i])
		// A compression pointer in a question is invalid, and following one
		// while parsing untrusted input is how parsers are made to loop.
		if n&0xC0 != 0 {
			return "", 0, 0, errMalformed
		}
		i++
		if n == 0 {
			break
		}
		if i+n > len(raw) {
			return "", 0, 0, errMalformed
		}
		labels = append(labels, string(raw[i:i+n]))
		i += n
		if len(strings.Join(labels, ".")) > maxName {
			return "", 0, 0, errMalformed
		}
	}
	if i+4 > len(raw) {
		return "", 0, 0, errMalformed
	}
	qtype = binary.BigEndian.Uint16(raw[i : i+2])
	return strings.Join(labels, "."), qtype, i + 4, nil
}

// respond builds the reply to one query.
func (s *Server) respond(raw []byte) ([]byte, error) {
	name, qtype, end, err := question(raw)
	if err != nil {
		return nil, err
	}

	flags := uint16(flagResponse | flagAuthoritative)
	if binary.BigEndian.Uint16(raw[2:4])&flagRecursionReq != 0 {
		flags |= flagRecursionReq
	}

	answer := s.owns(name) && qtype == typeA
	switch {
	case !s.owns(name):
		// Refused rather than NXDOMAIN: NXDOMAIN is a statement about the name
		// not existing, which this server is in no position to make about a
		// subtree it does not own.
		flags |= rcodeRefus
	default:
		flags |= rcodeOK
	}

	out := make([]byte, 0, end+16)
	out = append(out, raw[:end]...) // header and question, echoed back
	binary.BigEndian.PutUint16(out[2:4], flags)
	binary.BigEndian.PutUint16(out[6:8], 0)  // ANCOUNT, set below
	binary.BigEndian.PutUint16(out[8:10], 0) // NSCOUNT
	binary.BigEndian.PutUint16(out[10:12], 0)

	// A non-A query under the suffix gets success with no answers. A refusal or
	// NXDOMAIN for AAAA makes some resolvers stop rather than go on to ask
	// for A.
	if !answer {
		return out, nil
	}

	binary.BigEndian.PutUint16(out[6:8], 1)
	// 0xC00C is a pointer to offset 12: the name in the question we just
	// echoed, so the answer does not repeat it.
	out = append(out, 0xC0, 0x0C)
	out = binary.BigEndian.AppendUint16(out, typeA)
	out = binary.BigEndian.AppendUint16(out, classINET)
	out = binary.BigEndian.AppendUint32(out, ttl)
	out = binary.BigEndian.AppendUint16(out, 4)
	return append(out, s.Answer[:]...), nil
}

// ListenAndServe answers queries until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	defer pc.Close()

	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	buf := make([]byte, 512)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading: %w", err)
		}
		out, err := s.respond(buf[:n])
		if err != nil {
			continue
		}
		if _, err := pc.WriteTo(out, from); err != nil && ctx.Err() != nil {
			return nil
		}
	}
}

// Answers reports whether the resolver itself answers for a name, asked directly
// over UDP rather than through the system resolver.
//
// This is half of what `dns status` and the doctor row report, and the half that
// separates the two failures needing opposite fixes: the resolver is not running
// (nothing answers here), or the machine is not pointed at it (this answers and
// the system resolver still does not). One message covering both would tell a
// user whose control plane is down to edit a resolver file that was already
// correct (NERD026 SPEC003).
//
// A REFUSED answer is a failure too, and a distinct one: the resolver is up but
// does not own this suffix, which is what a suffix mismatch looks like from here.
func Answers(ctx context.Context, addr, name string) error {
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("reaching the resolver at %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(probeTimeout))
	}

	if _, err := conn.Write(encodeQuestion(name)); err != nil {
		return fmt.Errorf("asking the resolver at %s: %w", addr, err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("no answer from the resolver at %s: %w", addr, err)
	}
	if n < headerLen {
		return fmt.Errorf("short answer from the resolver at %s", addr)
	}
	if rcode := binary.BigEndian.Uint16(buf[2:4]) & 0x000F; rcode != rcodeOK {
		return fmt.Errorf("the resolver at %s does not answer for %s (rcode %d)", addr, name, rcode)
	}
	if binary.BigEndian.Uint16(buf[6:8]) == 0 {
		return fmt.Errorf("the resolver at %s returned no address for %s", addr, name)
	}
	return nil
}

// encodeQuestion builds a minimal A question for name. Hand-rolled for the same
// reason the server is: adding a DNS library to this module would drag
// golang.org/x/net and bump golang.org/x/term with it.
func encodeQuestion(name string) []byte {
	out := make([]byte, headerLen)
	binary.BigEndian.PutUint16(out[0:2], probeID)
	binary.BigEndian.PutUint16(out[2:4], flagRecursionReq)
	binary.BigEndian.PutUint16(out[4:6], 1)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	out = binary.BigEndian.AppendUint16(out, typeA)
	return binary.BigEndian.AppendUint16(out, classINET)
}

// ResolverFile is the macOS resolver file that points this machine here: the
// path it must be written to, and what goes in it.
//
// Filed under the suffix itself rather than its parent. A resolver file
// captures the whole subtree below the name it is filed under, so
// /etc/resolver/neuronsphere.io would route the real public website into this
// server -- which answers 127.0.0.1 for everything it owns.
func ResolverFile(suffix string, port int) (string, string) {
	suffix = strings.Trim(strings.ToLower(suffix), ".")
	return "/etc/resolver/" + suffix,
		fmt.Sprintf("nameserver 127.0.0.1\nport %d\n", port)
}

// InstallStep is the one privileged command for a platform.
//
// Printed, never run: nsctl does not take root and does not edit the user's
// files (NERD023, NERD026 SPEC002).
func InstallStep(goos, suffix string, port int) string {
	path, body := ResolverFile(suffix, port)
	switch goos {
	case "darwin":
		return fmt.Sprintf("sudo mkdir -p /etc/resolver && printf %q | sudo tee %s >/dev/null", body, path)
	case "linux":
		// systemd-resolved takes a routing domain on the interface rather than
		// a file, and there is no /etc/resolver. Where it is not in use, the
		// hosts file remains the fallback it has always been.
		return fmt.Sprintf(
			"sudo resolvectl dns lo 127.0.0.1:%d && sudo resolvectl domain lo '~%s'\n"+
				"  (systemd-resolved only. Without it, add the names to /etc/hosts as before.)",
			port, suffix)
	default:
		return fmt.Sprintf(
			"No automatic step is known for %s. Point this machine's resolver for %s at 127.0.0.1:%d,\n"+
				"  or add the names to /etc/hosts.", goos, suffix, port)
	}
}
