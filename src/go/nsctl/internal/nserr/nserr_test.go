package nserr

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOf(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want Code
	}{
		{"nil is success", nil, OK},
		{"uncoded errors default to Fail", sentinel, Fail},
		{"a coded error reports its code", New(InUse, "still running"), InUse},
		{"Wrap attaches a code", Wrap(Usage, sentinel), Usage},
		{"a code survives further wrapping", fmt.Errorf("context: %w", New(DeployFailed, "node failed")), DeployFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CodeOf(tt.err); got != tt.want {
				t.Errorf("CodeOf() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWrapNilStaysNil(t *testing.T) {
	t.Parallel()
	if err := Wrap(Fail, nil); err != nil {
		t.Errorf("Wrap(nil) = %v, want nil", err)
	}
}

func TestUnwrapReachesTheCause(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("cause")
	err := Wrap(InUse, fmt.Errorf("outer: %w", sentinel))
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is could not reach the cause through the code wrapper")
	}
}
