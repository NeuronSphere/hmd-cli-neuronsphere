package main

import (
	"bytes"
	"testing"
)

func TestRenderIsDeterministicAndUsesRootTree(t *testing.T) {
	first := render()
	if !bytes.Equal(first, render()) {
		t.Fatal("command reference output is not deterministic")
	}
	for _, want := range []string{"nsctl env", "nsctl control-plane", "nsctl agent skills"} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("generated reference does not contain %q", want)
		}
	}
}
