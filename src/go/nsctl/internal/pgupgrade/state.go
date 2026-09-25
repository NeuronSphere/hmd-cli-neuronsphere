package pgupgrade

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// State is the stage manifest, kept in the dump volume beside the dump it
// describes.
//
// In the dump volume rather than on the host because the two must be lost
// together: a manifest saying "dumped" that outlives its dump would send a
// resume past the point where the data still exists. Removing the volume
// removes the claim.
type State struct {
	Stage     Stage  `json:"stage"`
	Volume    string `json:"volume"`
	From      string `json:"from"`
	To        string `json:"to"`
	Image     string `json:"image"`
	DumpImage string `json:"dump_image"`
	// Databases and Roles are the inventory read from the old cluster before
	// the dump. The restore is checked against them, which is a check that
	// depends on parsing nothing.
	Databases []string  `json:"databases,omitempty"`
	Roles     []string  `json:"roles,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReadState returns the manifest in a dump volume, or a zero State when there
// is no volume, no manifest, or one this version did not write.
//
// A manifest that cannot be understood is deliberately not an error: it means
// "no progress", which sends the caller down the full path from the top. That
// path is safe from any starting point; guessing at a half-read manifest is
// not.
func ReadState(ctx context.Context, d Docker, dumpVolume, image string) (State, error) {
	if dumpVolume == "" || image == "" {
		return State{}, nil
	}
	out, err := runIn(ctx, d, dumpVolume, image, time.Minute,
		fmt.Sprintf("cat %s/%s 2>/dev/null || true", DumpDir, StateFile))
	if err != nil {
		// Nothing to read from: no such volume is the overwhelmingly common
		// case and is not a fault.
		return State{}, nil
	}
	body := strings.TrimSpace(out)
	if body == "" {
		return State{}, nil
	}
	var s State
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		return State{}, nil
	}
	if !s.Stage.Valid() {
		return State{}, nil
	}
	return s, nil
}

// WriteState records the manifest in the dump volume.
//
// Base64 on the way in so the JSON never meets a shell. The alternative is
// quoting a document that contains quotes, braces and user-chosen database
// names inside a `sh -c`, which is one unusual role name away from writing a
// different file than intended.
func WriteState(ctx context.Context, d Docker, dumpVolume, image string, s State) error {
	s.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding the migration state: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(body)
	_, err = runIn(ctx, d, dumpVolume, image, time.Minute,
		fmt.Sprintf("printf %%s %s | base64 -d > %s/%s", encoded, DumpDir, StateFile))
	if err != nil {
		return fmt.Errorf("recording the migration state in %s: %w", dumpVolume, err)
	}
	return nil
}

// volumeHasData reports whether a volume exists and holds anything.
//
// Used to tell a real backup from the empty volume `docker volume create`
// leaves behind, which is the difference between preserving the only copy of
// the data and copying an empty data directory over it.
func volumeHasData(ctx context.Context, d Docker, volume, image string) bool {
	if volume == "" || image == "" {
		return false
	}
	out, err := runIn(ctx, d, volume, image, time.Minute,
		"ls -A /dump 2>/dev/null | head -n 1")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// dumpIsComplete reports whether the dump in a volume carries pg_dumpall's
// trailer and is not empty.
//
// Both, because neither alone is enough: pg_dumpall exits 0 on a truncated
// file when the disk fills under it, so only the trailer proves it ran to the
// end -- and a zero-length file trivially has no trailer but is worth naming
// separately, since it means the redirect failed rather than the dump.
func dumpIsComplete(ctx context.Context, d Docker, dumpVolume, image string) (bool, int64, error) {
	out, err := runIn(ctx, d, dumpVolume, image, 5*time.Minute, fmt.Sprintf(
		"wc -c < %s/%s 2>/dev/null || echo 0; tail -c 4096 %s/%s 2>/dev/null || true",
		DumpDir, DumpFile, DumpDir, DumpFile))
	if err != nil {
		return false, 0, err
	}
	size, tail, _ := strings.Cut(strings.TrimLeft(out, " \t\n"), "\n")
	n, _ := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	return n > 0 && strings.Contains(tail, DumpTrailer), n, nil
}

// runIn runs a shell command in a throwaway container with one volume mounted
// at DumpDir.
func runIn(ctx context.Context, d Docker, volume, image string, timeout time.Duration, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := d.Run(ctx, "run", "--rm", "--entrypoint", "sh",
		"-v", volume+":"+DumpDir, image, "-c", script)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return string(stdout), nil
}
