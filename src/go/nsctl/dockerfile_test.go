package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The service image is built on the user's machine from sources carried in the
// binary, so a builder older than go.mod's go directive fails every first
// control-plane start that needs it -- released binaries included. go.mod
// moved to 1.26 on 2026-10-01 and the Dockerfile stayed on 1.25 until a live
// session acquire hit it.
func TestTheImageBuilderIsAtLeastGoModsGo(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	need := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindSubmatch(gomod)
	have := regexp.MustCompile(`(?m)^FROM golang:(\d+)\.(\d+)`).FindSubmatch(dockerfile)
	if need == nil || have == nil {
		t.Fatalf("could not read the versions: go.mod %q, Dockerfile %q", need, have)
	}
	n := func(b []byte) int { v, _ := strconv.Atoi(string(b)); return v }
	if n(have[1]) < n(need[1]) || (n(have[1]) == n(need[1]) && n(have[2]) < n(need[2])) {
		t.Errorf("Dockerfile builds with golang:%s.%s, but go.mod needs go %s.%s",
			have[1], have[2], need[1], need[2])
	}
}
