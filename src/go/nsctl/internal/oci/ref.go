// Package oci fetches and publishes artifacts through the OCI Distribution
// API. NERD016.
//
// It is a mechanism with no verbs of its own: stacks (NERD017) and CLI
// plugins (NERD018) give the bytes it moves their meaning. It speaks seven
// endpoints with net/http, verifies every byte it hands back against a
// content digest, and resolves a credential by one rule (SPEC006) that never
// consults anything the user did not point it at.
//
// # Free and paid on one code path
//
// A public namespace answers an anonymous pull; a private one answers a bearer
// token. The client cannot tell which it is talking to and does not try: it
// asks, is challenged, presents what it has, and reports the source of what
// was rejected. That is the whole of the licensing posture as it applies to
// distribution, and it is why this package has no notion of "free" or "paid".
//
// # Nothing here is on the resolve path
//
// internal/repoclass, internal/environment, internal/artifact and
// internal/runner never import this package (SPEC007, enforced by a test on
// the consumer side). A fetch happens because a verb whose name says so ran.
package oci

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
)

// Scheme is the one scheme a reference may carry.
const Scheme = "oci://"

var (
	// ErrNoHost is a reference with no registry host. This package never
	// guesses one; a verb may expand a bare name against a namespace compiled
	// into that verb, and prints the expansion when it does. SPEC001.
	ErrNoHost = errors.New("no registry host in the reference")

	// ErrUnsupportedScheme is a reference to a source this package does not
	// speak. github.com is recognised and refused by name: SPEC008 defers it.
	ErrUnsupportedScheme = errors.New("unsupported source")

	// ErrInvalidRef is a reference that does not parse.
	ErrInvalidRef = errors.New("invalid reference")
)

// Ref names an artifact: a registry host, a repository within it, and at most
// one of a tag or a manifest digest.
type Ref struct {
	Host       string
	Repository string
	Tag        string
	Digest     string
}

var (
	// repositoryRe is the Distribution specification's <name> grammar: lower
	// case path components separated by one of . _ - or __.
	repositoryRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

// ParseRef parses SPEC001's grammar:
//
//	ref      := [ "oci://" ] host "/" repository [ version ]
//	version  := ":" tag | "@" tag | "@" "sha256:" hex64
//
// "@<tag>" is accepted beside the conventional ":<tag>" because every
// reference a user of nsctl has typed so far is <class>@<version>.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("%w: empty", ErrInvalidRef)
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if s[:i+3] != Scheme {
			return Ref{}, fmt.Errorf("%w: %q; only %s references are supported", ErrUnsupportedScheme, s[:i+3], Scheme)
		}
		s = s[i+3:]
	}

	host, rest, ok := strings.Cut(s, "/")
	if !ok || rest == "" {
		return Ref{}, fmt.Errorf("%w: %q. A reference is <host>/<repository>[:<tag>], e.g. ghcr.io/neuronsphere/stacks/analytics:0.3", ErrNoHost, s)
	}
	if !isHost(host) {
		return Ref{}, fmt.Errorf("%w: %q looks like a repository path with no registry in front of it. "+
			"A reference is <host>/<repository>[:<tag>], e.g. ghcr.io/neuronsphere/stacks/analytics:0.3", ErrNoHost, s)
	}
	if strings.EqualFold(host, "github.com") {
		return Ref{}, fmt.Errorf("%w: github.com/<owner>/<repo> is a GitHub Releases source, which NERD016 SPEC008 defers; "+
			"publish the artifact to an OCI registry (ghcr.io/<owner>/...) instead", ErrUnsupportedScheme)
	}

	r := Ref{Host: host}
	repo := rest
	// A digest pin: "@sha256:<hex>". Checked before "@<tag>" so the colon in
	// the algorithm prefix is not mistaken for a tag separator.
	if at := strings.LastIndex(repo, "@"); at >= 0 {
		version := repo[at+1:]
		repo = repo[:at]
		switch {
		case version == "":
			return Ref{}, fmt.Errorf("%w: %q ends in @ with no version", ErrInvalidRef, s)
		case strings.Contains(version, ":"):
			d, err := digest.Parse(version)
			if err != nil {
				return Ref{}, fmt.Errorf("%w: %q: %v", ErrInvalidRef, s, err)
			}
			r.Digest = d.String()
		default:
			if !tagRe.MatchString(version) {
				return Ref{}, fmt.Errorf("%w: %q is not a tag", ErrInvalidRef, version)
			}
			r.Tag = version
		}
	} else if colon := strings.LastIndex(repo, ":"); colon >= 0 && colon > strings.LastIndex(repo, "/") {
		tag := repo[colon+1:]
		repo = repo[:colon]
		if tag == "" {
			return Ref{}, fmt.Errorf("%w: %q ends in : with no tag", ErrInvalidRef, s)
		}
		if !tagRe.MatchString(tag) {
			return Ref{}, fmt.Errorf("%w: %q is not a tag", ErrInvalidRef, tag)
		}
		r.Tag = tag
	}
	if !repositoryRe.MatchString(repo) {
		return Ref{}, fmt.Errorf("%w: repository %q must be lower-case path segments (a-z, 0-9, separated by . _ - or /)", ErrInvalidRef, repo)
	}
	r.Repository = repo
	return r, nil
}

// isHost is the Distribution rule for telling a registry from the first
// segment of a repository path: it has a dot or a port, or is localhost.
func isHost(s string) bool {
	if s == "" {
		return false
	}
	return s == "localhost" || strings.HasPrefix(s, "localhost:") ||
		strings.ContainsAny(s, ".:")
}

// Name is host/repository: what the registry API calls the artifact.
func (r Ref) Name() string { return r.Host + "/" + r.Repository }

// Reference is the tag or digest the manifest is addressed by, or "" for a
// reference with neither.
func (r Ref) Reference() string {
	if r.Digest != "" {
		return r.Digest
	}
	return r.Tag
}

// Versioned reports whether the reference names one manifest.
func (r Ref) Versioned() bool { return r.Tag != "" || r.Digest != "" }

// WithTag returns the reference addressed by a tag (or none, for ""), with
// any digest dropped.
func (r Ref) WithTag(tag string) Ref {
	r.Tag, r.Digest = tag, ""
	return r
}

// String is the canonical spelling, always with the scheme.
func (r Ref) String() string {
	s := Scheme + r.Name()
	switch {
	case r.Digest != "":
		return s + "@" + r.Digest
	case r.Tag != "":
		return s + ":" + r.Tag
	}
	return s
}

// Versioned is the schemaVersion every manifest this package writes carries.
func Versioned() specs.Versioned { return specs.Versioned{SchemaVersion: 2} }
