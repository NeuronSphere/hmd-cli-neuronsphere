package dnsd

import (
	"encoding/binary"
	"strings"
	"testing"
)

// query builds a minimal, well-formed DNS question on the wire.
func query(name string, qtype uint16) []byte {
	out := make([]byte, headerLen)
	binary.BigEndian.PutUint16(out[0:2], 4242)             // ID
	binary.BigEndian.PutUint16(out[2:4], flagRecursionReq) // RD
	binary.BigEndian.PutUint16(out[4:6], 1)                // QDCOUNT
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	out = binary.BigEndian.AppendUint16(out, qtype)
	return binary.BigEndian.AppendUint16(out, classINET)
}

type reply struct {
	id      uint16
	rcode   uint16
	answers uint16
	ip      [4]byte
}

func decode(t *testing.T, raw []byte) reply {
	t.Helper()
	if len(raw) < headerLen {
		t.Fatalf("reply is %d bytes, too short for a header", len(raw))
	}
	r := reply{
		id:      binary.BigEndian.Uint16(raw[0:2]),
		rcode:   binary.BigEndian.Uint16(raw[2:4]) & 0x000F,
		answers: binary.BigEndian.Uint16(raw[6:8]),
	}
	if binary.BigEndian.Uint16(raw[2:4])&flagResponse == 0 {
		t.Error("the QR bit is not set, so this is not a reply")
	}
	if r.answers > 0 {
		// The answer's RDATA is the last four bytes: pointer(2) type(2)
		// class(2) ttl(4) rdlength(2) rdata(4).
		copy(r.ip[:], raw[len(raw)-4:])
	}
	return r
}

// The whole point: a name nobody has registered anywhere still resolves. This
// is what /etc/hosts cannot do at any price.
func TestAnUnregisteredNameUnderTheSuffixResolves(t *testing.T) {
	t.Parallel()

	out, err := New(DefaultSuffix).respond(query("nothing-has-ever-deployed-this.local.neuronsphere.io.", typeA))
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	r := decode(t, out)
	if r.rcode != rcodeOK {
		t.Fatalf("RCODE = %d, want %d", r.rcode, rcodeOK)
	}
	if r.answers != 1 {
		t.Fatalf("got %d answers, want 1", r.answers)
	}
	if r.ip != [4]byte{127, 0, 0, 1} {
		t.Errorf("answered %v, want 127.0.0.1", r.ip)
	}
	if r.id != 4242 {
		t.Errorf("reply ID = %d, want the query's 4242", r.id)
	}
}

// Case is not significant in DNS, and a browser may send any of it.
func TestTheSuffixMatchIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	out, err := New(DefaultSuffix).respond(query("Airflow.Local.NeuronSphere.IO.", typeA))
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if r := decode(t, out); r.answers != 1 {
		t.Errorf("got %d answers for a mixed-case name, want 1", r.answers)
	}
}

// This server is authoritative for one subtree and must never answer for
// anything else -- most of all not for the real neuronsphere.io.
func TestANameOutsideTheSuffixIsRefused(t *testing.T) {
	t.Parallel()

	s := New(DefaultSuffix)
	for _, name := range []string{
		"www.neuronsphere.io.",
		"neuronsphere.io.",
		"example.com.",
		"local.neuronsphere.io.evil.com.",
		"notlocal.neuronsphere.io.",
	} {
		out, err := s.respond(query(name, typeA))
		if err != nil {
			t.Fatalf("respond(%s): %v", name, err)
		}
		r := decode(t, out)
		if r.answers != 0 {
			t.Errorf("%s was answered with %d records; this server is not authoritative for it", name, r.answers)
		}
		if r.rcode != rcodeRefus {
			t.Errorf("%s got RCODE %d, want %d so the resolver moves on", name, r.rcode, rcodeRefus)
		}
	}
}

// The suffix itself resolves, not only names under it.
func TestTheSuffixItselfResolves(t *testing.T) {
	t.Parallel()

	out, err := New(DefaultSuffix).respond(query("local.neuronsphere.io.", typeA))
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if r := decode(t, out); r.answers != 1 {
		t.Errorf("the suffix itself got %d answers, want 1", r.answers)
	}
}

// AAAA must come back empty-but-successful, not as a failure: NXDOMAIN or a
// refusal for AAAA makes some resolvers give up rather than ask for A.
func TestAAAAIsEmptyButSuccessful(t *testing.T) {
	t.Parallel()

	const typeAAAA = 28
	out, err := New(DefaultSuffix).respond(query("airflow.local.neuronsphere.io.", typeAAAA))
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	r := decode(t, out)
	if r.rcode != rcodeOK {
		t.Errorf("AAAA RCODE = %d, want %d with no answers", r.rcode, rcodeOK)
	}
	if r.answers != 0 {
		t.Errorf("AAAA returned %d answers, want none", r.answers)
	}
}

// A packet that is not a question is dropped, never answered.
func TestGarbageIsNotFatal(t *testing.T) {
	t.Parallel()

	s := New(DefaultSuffix)
	for _, name := range []string{"two bytes", "header only", "truncated label", "compression pointer"} {
		var raw []byte
		switch name {
		case "two bytes":
			raw = []byte{0x01, 0x02}
		case "header only":
			raw = make([]byte, headerLen)
			binary.BigEndian.PutUint16(raw[4:6], 1)
		case "truncated label":
			raw = append(make([]byte, headerLen), 0x05, 'a', 'b')
			binary.BigEndian.PutUint16(raw[4:6], 1)
		case "compression pointer":
			raw = append(make([]byte, headerLen), 0xC0, 0x0C)
			binary.BigEndian.PutUint16(raw[4:6], 1)
		}
		if _, err := s.respond(raw); err == nil {
			t.Errorf("%s produced a reply; want an error the caller drops", name)
		}
	}
}

// The resolver file must claim the narrowest subtree that works. Named for
// neuronsphere.io it would capture the company's real public website and answer
// 127.0.0.1 for it.
func TestTheResolverFileIsScopedToTheSubtree(t *testing.T) {
	t.Parallel()

	path, body := ResolverFile(DefaultSuffix, DefaultPort)
	if path != "/etc/resolver/local.neuronsphere.io" {
		t.Errorf("resolver file is %q; want it scoped to local.neuronsphere.io", path)
	}
	if path == "/etc/resolver/neuronsphere.io" {
		t.Error("the resolver file would capture the whole of neuronsphere.io, including the public website")
	}
	for _, want := range []string{"nameserver 127.0.0.1", "port 19153"} {
		if !strings.Contains(body, want) {
			t.Errorf("resolver file should contain %q, got:\n%s", want, body)
		}
	}
}

// 5353 is the obvious port and the wrong one: mDNS holds it on macOS, shared
// between processes with SO_REUSEPORT, which Docker's publisher does not set.
// A default of 5353 would fail to start on a typical Mac.
func TestTheDefaultPortAvoidsMDNS(t *testing.T) {
	t.Parallel()

	if DefaultPort == 5353 || DefaultPort == 53 {
		t.Errorf("DefaultPort = %d, which mDNS or the privileged range owns", DefaultPort)
	}
	// Below the ephemeral floor, so the OS never hands it out at random.
	if DefaultPort >= 49152 {
		t.Errorf("DefaultPort = %d is in the ephemeral range", DefaultPort)
	}
	// Outside the band hmd_proxy publishes, which a second container could not
	// bind at all.
	if DefaultPort >= 19000 && DefaultPort <= 19111 {
		t.Errorf("DefaultPort = %d collides with the published environment band", DefaultPort)
	}
}

func TestInstallStepIsPlatformSpecific(t *testing.T) {
	t.Parallel()

	mac := InstallStep("darwin", DefaultSuffix, DefaultPort)
	if !strings.Contains(mac, "/etc/resolver/local.neuronsphere.io") {
		t.Errorf("the macOS step should write the resolver file, got:\n%s", mac)
	}
	linux := InstallStep("linux", DefaultSuffix, DefaultPort)
	if strings.Contains(linux, "/etc/resolver") {
		t.Errorf("Linux has no /etc/resolver; got:\n%s", linux)
	}
	if !strings.Contains(linux, "/etc/hosts") && !strings.Contains(linux, "resolvectl") {
		t.Errorf("the Linux step should name systemd-resolved or the hosts fallback, got:\n%s", linux)
	}
	if other := InstallStep("plan9", DefaultSuffix, DefaultPort); !strings.Contains(other, "plan9") {
		t.Errorf("an unknown platform should say so by name, got:\n%s", other)
	}
}
