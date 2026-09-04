// Package nserr carries a command's exit code alongside its error.
//
// SPEC004 fixes the exit codes and is explicit that they are distinct rather
// than overloaded as counters, so a script can branch on "refused because
// something is still using it" without parsing a message. Cement had no
// equivalent -- the Python CLI exits 1 for everything except the one place it
// exits 1 for a degraded start -- so this is new behaviour, not a port.
package nserr

import (
	"errors"
	"fmt"
)

// Code is a process exit status. The values are SPEC004's, verbatim.
type Code int

const (
	// OK is a successful run.
	OK Code = 0
	// Fail is any error nsctl itself hit: a failed API call, an unreachable
	// service, a file it could not write.
	Fail Code = 1
	// Usage is a malformed command line, or a precondition the caller could
	// have satisfied -- an unset HMD_HOME, an environment that does not exist.
	Usage Code = 2
	// InUse is a refusal to proceed because something else still depends on
	// the target: `control-plane stop` while an environment is running.
	InUse Code = 3
	// DeployFailed is reserved for a deploy node that failed. It is distinct
	// from Fail because the platform is up and one entry did not deploy, which
	// is a different thing to retry.
	DeployFailed Code = 4
)

// Error is an error with an exit code attached.
type Error struct {
	Code Code
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

// Unwrap keeps errors.Is/As working through the code wrapper.
func (e *Error) Unwrap() error { return e.Err }

// New builds a coded error from a format string.
func New(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Err: fmt.Errorf(format, a...)}
}

// Wrap attaches a code to an existing error. A nil error stays nil so callers
// can wrap unconditionally.
func Wrap(code Code, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// CodeOf reports the exit status for an error: the code of the innermost
// *Error in the chain, or Fail for any other non-nil error.
//
// Fail rather than Usage is the default on purpose. An uncoded error is one
// nobody classified, and reporting it as a usage problem would send the user
// to re-read the flags for what is actually a runtime failure.
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return Fail
}
