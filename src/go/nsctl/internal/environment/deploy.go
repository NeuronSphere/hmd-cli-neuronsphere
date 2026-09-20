package environment

import (
	"strconv"
	"strings"
)

// RunnerParallelism is how many nodes the in-process runner may deploy at once,
// when the user has asked for something other than the default.
//
// The runner's own default is the cloud path's spec.parallelism: 4, and nothing
// here needs to restate it. What this exists for is the way back:
// HMD_LOCAL_RUNNER_PARALLELISM=1 makes a deploy sequential again, which is what
// someone bisecting a deploy that only misbehaves under concurrency needs.
func RunnerParallelism(lookup func(string) string) int {
	if lookup == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(lookup("HMD_LOCAL_RUNNER_PARALLELISM")))
	if err != nil || n < 1 {
		return 0
	}
	return n
}
