package floci

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// fakeWake is a WakeDocker over a fixed set of containers per account.
type fakeWake struct {
	byAccount map[string][]string
	running   map[string]bool
	stopped   []string
}

func (f *fakeWake) ContainersWithLabelOnNetwork(_ context.Context, _, account, _ string) []string {
	return f.byAccount[account]
}

func (f *fakeWake) Running(_ context.Context, name string) (bool, error) {
	return f.running[name], nil
}

func (f *fakeWake) Stop(_ context.Context, name string) error {
	f.running[name] = false
	f.stopped = append(f.stopped, name)
	return nil
}

func TestStopWokenLeavesExemptEnvironmentsAlone(t *testing.T) {
	t.Parallel()

	d := &fakeWake{
		byAccount: map[string][]string{
			"111": {"db-local"},
			"222": {"db-cc1"},
			"333": {"db-old"},
		},
		running: map[string]bool{"db-local": true, "db-cc1": true, "db-old": true},
	}
	envs := []EnvAccount{{"local", "111"}, {"cc-1", "222"}, {"old", "333"}}
	// Nothing was running before. `local` is the caller's own start, `cc-1` is
	// another session's start in the same window; only `old` was Floci waking
	// a persisted environment.
	exempt := func(slug string) bool { return slug == "local" || slug == "cc-1" }

	stopped, failures := StopWoken(context.Background(), d, envs, "net", map[string]bool{}, exempt)
	if len(failures) > 0 {
		t.Fatalf("failures = %v", failures)
	}
	if !reflect.DeepEqual(stopped, []string{"old"}) {
		t.Errorf("stopped environments = %v, want [old]", stopped)
	}
	sort.Strings(d.stopped)
	if !reflect.DeepEqual(d.stopped, []string{"db-old"}) {
		t.Errorf("stopped containers = %v, want [db-old]", d.stopped)
	}
}

func TestStopWokenWithNoExemptionStillSparesWhatWasRunning(t *testing.T) {
	t.Parallel()

	d := &fakeWake{
		byAccount: map[string][]string{"111": {"db-a"}, "222": {"db-b"}},
		running:   map[string]bool{"db-a": true, "db-b": true},
	}
	envs := []EnvAccount{{"a", "111"}, {"b", "222"}}
	stopped, _ := StopWoken(context.Background(), d, envs, "net", map[string]bool{"db-a": true}, nil)
	if !reflect.DeepEqual(stopped, []string{"b"}) {
		t.Errorf("stopped = %v, want [b]", stopped)
	}
}
