package controlplane

import (
	"context"
	"errors"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/hostlock"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// substrateWait bounds how long one control-plane operation waits for
// another. A cold bootstrap is the longest holder and takes minutes, not
// this long; anything past it is a wedged process the user should look at.
const substrateWait = 45 * time.Minute

// substrateNotice is how long a waiter stays quiet before saying it is
// waiting, so the common uncontended case prints nothing.
const substrateNotice = 2 * time.Second

// acquireSubstrate takes the host's substrate lock, which serialises every
// operation that starts, stops or recreates the shared control-plane
// containers (Floci, hmd_proxy, ms-deployment) or runs the bootstrap.
func acquireSubstrate(ctx context.Context, opts *Options, holder string) (*hostlock.Lock, error) {
	lock, err := hostlock.Acquire(ctx, opts.Home, hostlock.Substrate, holder, substrateNotice)
	var busy *hostlock.BusyError
	if errors.As(err, &busy) {
		opts.step("Waiting for %s to finish with the control plane...", busy.Holder)
		lock, err = hostlock.Acquire(ctx, opts.Home, hostlock.Substrate, holder, substrateWait)
	}
	if err != nil {
		if errors.As(err, &busy) {
			return nil, nserr.Wrap(nserr.InUse, err)
		}
		return nil, nserr.Wrap(nserr.Fail, err)
	}
	return lock, nil
}
