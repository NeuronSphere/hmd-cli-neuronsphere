package cmd

import (
	"strings"
	"testing"
)

// The help no longer promises that doctor creates nothing: it says the bridge
// check runs one throwaway container with no network and removes it.
//
// Requirements: HMD_CLI_NEURONSPHERE_NERD028_SPEC008
func TestDoctorHelpAmendsTheNothingCreatedPromise(t *testing.T) {
	long := strings.Join(strings.Fields(newDoctorCommand(&Options{}).Long), " ")
	for _, want := range []string{
		"one throwaway container with no network and removes it",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("doctor help lacks %q:\n%s", want, long)
		}
	}
	for _, old := range []string{"creates nothing", "starts nothing", "pulls nothing, and removes nothing"} {
		if strings.Contains(long, old) {
			t.Errorf("doctor help still carries the falsified promise %q", old)
		}
	}
}
