package pgupgrade

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/container"
)

// fakeDocker models just enough of a daemon to run a whole migration: which
// containers mount which volume, what each volume holds, and what psql would
// answer. Every argv is recorded, because the thing worth asserting about a
// destructive command is the order in which it did things.
type fakeDocker struct {
	calls [][]string

	// files is volume -> path -> contents.
	files map[string]map[string]string
	// users is volume -> containers mounting it.
	users map[string][]container.VolumeUser
	// running is container name -> up.
	running map[string]bool
	// databases and roles are what psql reports, by the image that is up.
	databases, roles []string
	// dumpBody is what a pg_dumpall writes. Empty means a truncated dump.
	dumpBody string
	// failures maps an argv substring to the stderr it should fail with.
	failures map[string]string
	// pulled records the images asked for.
	pulled []string
}

func newFake() *fakeDocker {
	return &fakeDocker{
		files:     map[string]map[string]string{},
		users:     map[string][]container.VolumeUser{},
		running:   map[string]bool{},
		databases: []string{"hmd", "trino"},
		roles:     []string{"hmd", "reader"},
		dumpBody:  "-- a dump\n--\n-- " + DumpTrailer + "\n",
		failures:  map[string]string{},
	}
}

func (f *fakeDocker) put(volume, path, body string) {
	if f.files[volume] == nil {
		f.files[volume] = map[string]string{}
	}
	f.files[volume][path] = body
}

func (f *fakeDocker) VolumesMatching(context.Context, string) []string { return nil }
func (f *fakeDocker) ImageEnv(context.Context, string) map[string]string {
	return map[string]string{"PG_MAJOR": "14"}
}
func (f *fakeDocker) VolumeUserImage(context.Context, string) string { return "" }
func (f *fakeDocker) Logs(context.Context, string, int) string       { return "" }

func (f *fakeDocker) PullImage(_ context.Context, ref string) error {
	f.calls = append(f.calls, []string{"pull", ref})
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *fakeDocker) Running(_ context.Context, name string) (bool, error) {
	return f.running[name], nil
}

func (f *fakeDocker) RemoveContainer(_ context.Context, name string) error {
	f.calls = append(f.calls, []string{"rm", "-f", name})
	delete(f.running, name)
	for volume, us := range f.users {
		var kept []container.VolumeUser
		for _, u := range us {
			if u.Name != name {
				kept = append(kept, u)
			}
		}
		f.users[volume] = kept
	}
	return nil
}

func (f *fakeDocker) VolumeContainers(_ context.Context, volume string) []container.VolumeUser {
	return f.users[volume]
}

func (f *fakeDocker) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	for needle, stderr := range f.failures {
		if strings.Contains(joined, needle) {
			return nil, []byte(stderr), fmt.Errorf("exit status 1")
		}
	}

	switch args[0] {
	case "run":
		return f.run(args)
	case "exec":
		return f.exec(args)
	case "volume":
		if len(args) > 2 && args[1] == "rm" {
			delete(f.files, args[2])
		}
		return nil, nil, nil
	}
	return nil, nil, nil
}

// run handles both `docker run -d --name ...` (start a server) and
// `docker run --rm --entrypoint sh ... -c <script>`.
func (f *fakeDocker) run(args []string) ([]byte, []byte, error) {
	mounts := map[string]string{} // mountpoint -> volume
	var name, script string
	for i, a := range args {
		switch a {
		case "-v":
			volume, at, _ := strings.Cut(args[i+1], ":")
			mounts[at] = volume
		case "--name":
			name = args[i+1]
		case "-c":
			script = args[i+1]
		}
	}

	if script == "" { // a detached server
		f.running[name] = true
		image := args[len(args)-1]
		for at, volume := range mounts {
			_ = at
			f.users[volume] = append(f.users[volume], container.VolumeUser{
				Name: name, Running: true, Image: image,
			})
		}
		return nil, nil, nil
	}
	return f.script(script, mounts)
}

// script answers the handful of shell one-liners this package runs.
func (f *fakeDocker) script(script string, mounts map[string]string) ([]byte, []byte, error) {
	dump := mounts[DumpDir]
	switch {
	case strings.HasPrefix(script, "cat "):
		return []byte(f.files[dump][StateFile]), nil, nil

	case strings.Contains(script, "base64 -d"):
		encoded := strings.Fields(script)[2]
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, []byte("bad base64"), err
		}
		f.put(dump, StateFile, string(body))
		return nil, nil, nil

	case strings.HasPrefix(script, "ls -A"):
		if len(f.files[dump]) == 0 {
			return nil, nil, nil
		}
		return []byte("something\n"), nil, nil

	case strings.HasPrefix(script, "wc -c"):
		body := f.files[dump][DumpFile]
		return fmt.Appendf(nil, "%d\n%s", len(body), body), nil, nil

	case strings.HasPrefix(script, "cp -a"):
		from, to := mounts["/from"], mounts["/to"]
		if f.files[to] == nil {
			f.files[to] = map[string]string{}
		}
		for k, v := range f.files[from] {
			f.files[to][k] = v
		}
		return nil, nil, nil

	case strings.HasPrefix(script, "rm -rf"):
		delete(f.files, mounts[PGData])
		return nil, nil, nil
	}
	return nil, nil, nil
}

func (f *fakeDocker) exec(args []string) ([]byte, []byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "pg_isready"):
		if !f.running[args[1]] {
			return nil, []byte("no server"), fmt.Errorf("exit status 1")
		}
		return nil, nil, nil

	case strings.Contains(joined, "pg_dumpall"):
		// The dump volume is mounted in the helper that is running.
		f.put(f.dumpVolumeOf(args[1]), DumpFile, f.dumpBody)
		return nil, nil, nil

	case strings.Contains(joined, databasesQuery):
		return []byte(strings.Join(f.databases, "\n")), nil, nil

	case strings.Contains(joined, rolesQuery):
		return []byte(strings.Join(f.roles, "\n")), nil, nil

	case strings.Contains(joined, "-f "+DumpDir):
		return nil, nil, nil
	}
	return nil, nil, nil
}

// dumpVolumeOf finds the dump volume the named helper has mounted.
func (f *fakeDocker) dumpVolumeOf(helper string) string {
	for volume, us := range f.users {
		for _, u := range us {
			if u.Name == helper && strings.HasPrefix(volume, dumpPrefix) {
				return volume
			}
		}
	}
	return ""
}

// ran reports the recorded argvs joined, for order assertions.
func (f *fakeDocker) ran() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// firstMatching is the index of the first recorded call containing needle.
func (f *fakeDocker) firstMatching(needle string) int {
	for i, line := range f.ran() {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}
