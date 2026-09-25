package floci

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
)

// SentinelName is the file the host writes into Floci's data directory so the
// running container can be asked whether it is still looking at it.
//
// Hidden, not `*.s3data`, not under `s3/`, and not matched by the API Gateway
// store's glob, so neither Floci's loader nor PruneAPIGatewayGhosts has any
// opinion about it.
const SentinelName = ".nsctl-datadir"

// WriteSentinel stamps the data directory with a token that has never been
// used before, and returns it.
//
// Fresh on every start rather than stable: a stable token would still be there
// in the orphaned directory a stale container is holding, and would therefore
// match. The property being tested is "the container can see what the host
// just wrote", and only a value the container cannot already have tests it.
func WriteSentinel(dataDir string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating the data directory's token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := atomicfile.Write(filepath.Join(dataDir, SentinelName), []byte(token), 0o644, 0o755); err != nil {
		return "", err
	}
	return token, nil
}

// SentinelMatches reports whether the running container is looking at the
// directory the host just wrote to.
//
// A bind mount is resolved once, when the container is created, and nothing
// re-resolves it or reports that it has gone stale. So a container whose host
// directory was deleted out from under it keeps writing into an unlinked one,
// its in-memory state keeps answering, and the damage surfaces much later and
// somewhere else (NERD001 SPEC014). This is the question that separates the
// two, and there is no way to ask it from the host alone.
//
// The command is written to exit 0 whether or not the file is there, and the
// *output* carries the answer. That is not fussiness: Exec reports any non-zero
// exit as an error, so a bare `cat` would make "the file is not there" and "the
// container is not reachable" the same result -- and the first of those is
// exactly what the failure looks like. A directory deleted and remade under a
// running container leaves it holding the empty, unlinked one, so the probe
// reads no file at all rather than an old token.
//
// Conservative in the direction that matters: only an answer that came back
// (empty or stale) is a mismatch the caller may act on, and a probe that could
// not run at all returns (true, err) -- "do not act" -- because the cost of a
// false positive is recreating a Floci that was working.
func SentinelMatches(ctx context.Context, d Execer, container, want string) (bool, error) {
	out, err := d.Exec(ctx, container, "sh", "-c", "cat /app/data/"+SentinelName+" 2>/dev/null; exit 0")
	if err != nil {
		return true, fmt.Errorf("reading %s inside %s: %w", SentinelName, container, err)
	}
	return strings.TrimSpace(string(out)) == want, nil
}
