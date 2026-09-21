package oci

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/opencontainers/go-digest"
)

// ErrNoCredential is a push with nothing to present. Raised before any
// request, naming the three ways to supply one. SPEC005.
var ErrNoCredential = errors.New("pushing needs a credential: pass --token, set " + TokenEnv +
	", or add registry_url to a profile in nsctl.toml and `nsctl login`")

// Error is a registry's refusal, with the operation, the status, the body,
// and enough context to say what to do about it. It mirrors librarian.Error
// and msdeploy.Error: the body is where a registry says what was wrong.
type Error struct {
	Operation string
	Status    int
	Body      string
	Ref       Ref
	// Source is the credential's SPEC006 source, or "anonymous".
	Source string
}

func (e *Error) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "..."
	}
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Operation, e.Ref, e.Status)
	if body != "" {
		msg += ": " + body
	}
	switch {
	case e.NotFound():
		// A private ghcr.io package is indistinguishable from a missing one
		// from outside, and packages there are private by default.
		msg += "\n  not found: the package may not exist under that name, or may be private " +
			"(ghcr.io packages are private by default) and need a credential"
		if e.Source == "" || e.Source == "anonymous" {
			msg += "; set " + TokenEnv + " or add registry_url to a profile and `nsctl login`"
		}
	case e.Unauthorized():
		if e.Source == "" || e.Source == "anonymous" {
			msg += "\n  the registry requires a credential, or the package is private: set " +
				TokenEnv + " (and " + UserEnv + " if the registry needs a username), " +
				"or add registry_url to a profile in nsctl.toml and `nsctl login`"
		} else {
			msg += "\n  the credential from " + e.Source + " was rejected"
		}
	}
	return msg
}

// Unauthorized reports a refused credential rather than a wrong request.
func (e *Error) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// NotFound reports a manifest, blob or repository the registry does not have.
func (e *Error) NotFound() bool { return e.Status == http.StatusNotFound }

// MismatchError is bytes that do not match their descriptor. SPEC003: nothing
// partially verified is handed over, and the message names both sides.
type MismatchError struct {
	What     string // "manifest", "config", "layer"
	Expected digest.Digest
	Actual   digest.Digest
	// ExpectedSize and ActualSize are set when the size disagreed; Actual is
	// then the digest of what was received, which may be a prefix.
	ExpectedSize, ActualSize int64
}

func (e *MismatchError) Error() string {
	if e.ExpectedSize != 0 && e.ExpectedSize != e.ActualSize {
		return fmt.Sprintf("%s %s: received %d bytes, descriptor says %d; the registry's copy does not match its manifest",
			e.What, e.Expected, e.ActualSize, e.ExpectedSize)
	}
	return fmt.Sprintf("%s digest mismatch: descriptor says %s, received bytes are %s; the registry's copy does not match its manifest",
		e.What, e.Expected, e.Actual)
}

// ErrBoundToHost is a reference to a host other than the one the client's
// credential was resolved for. SPEC006: a credential goes to its host and to
// nothing else.
var ErrBoundToHost = errors.New("credential is bound to another host")
