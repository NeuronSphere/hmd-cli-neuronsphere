package nserr

import (
	"errors"
	"testing"
)

// NERD018 SPEC005: a plugin's exit status comes back unchanged with no
// message from nsctl.

func TestSilentCarriesTheCodeAndIsSilent(t *testing.T) {
	t.Parallel()
	err := Silent(7)
	if CodeOf(err) != 7 {
		t.Errorf("CodeOf = %d, want 7", CodeOf(err))
	}
	if !errors.Is(err, ErrSilent) {
		t.Error("must be ErrSilent")
	}
	if !IsSilent(err) {
		t.Error("IsSilent must be true")
	}
	if IsSilent(New(Fail, "loud")) {
		t.Error("an ordinary error is not silent")
	}
	if Silent(0) != nil {
		t.Error("exit 0 is no error")
	}
}
