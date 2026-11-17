package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// ImageEnv overrides the DuckDB image, for someone iterating on
// hmd-img-duckdb with a local build.
const ImageEnv = "HMD_DUCKDB_IMAGE"

// DefaultImage is the pinned DuckDB engine (NERD039 SPEC002). Its DuckDB
// version is also the catalog's storage version, which `data connect` states.
const DefaultImage = "ghcr.io/hmdlabs/hmd-img-duckdb:0.1.0"

// DuckDBVersion is the DuckDB release DefaultImage carries.
const DuckDBVersion = "1.5.3"

// prelude starts every session. The extensions are installed in the image, so
// nothing is ever fetched: a run works offline, as any user id.
const prelude = "SET extension_directory='/opt/duckdb/extensions';\n" +
	"SET autoinstall_known_extensions=false;\n" +
	"SET autoload_known_extensions=true;\n"

// Mount is a host path the engine sees at the same absolute path, so that a
// view written into the catalog resolves unchanged from a DuckDB on the host.
type Mount struct {
	Path     string
	ReadOnly bool
}

// Request is one engine session.
type Request struct {
	// Network is the Docker network the engine joins; empty is none. Only a
	// workspace with storage configured sets it (SPEC009).
	Network string
	// Setup runs after the prelude and before Script, on stdin, so what it
	// holds (an S3 secret) never appears on a command line.
	Setup string
	// DB is the database file; empty is in-memory.
	DB       string
	ReadOnly bool
	Script   string
	Mounts   []Mount
	// ContinueOnError runs every statement even after one fails, and returns
	// what succeeded with the failures' messages in Output.Stderr. Scan uses
	// it so one unreadable file does not hide the rest.
	ContinueOnError bool
}

// Output is a session's result sets and, for a ContinueOnError session, the
// messages of the statements that failed.
type Output struct {
	Results Result
	// Columns is each result set's column names, in the order DuckDB
	// returned them; a map loses it.
	Columns [][]string
	Stderr  string
}

// Result is each statement's rows, in order, for the statements that
// returned rows. Numbers are json.Number so a count is not rounded.
type Result [][]map[string]any

// Engine runs DuckDB sessions.
type Engine interface {
	Run(ctx context.Context, req Request) (Output, error)
}

// DockerEngine runs the DuckDB CLI from an image.
type DockerEngine struct {
	// Bin is the docker binary; empty means "docker".
	Bin   string
	Image string
}

func (d DockerEngine) bin() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "docker"
}

// args is the docker command line for a request. The engine has no network:
// nothing it does in this release needs one (SPEC002).
func (d DockerEngine) args(req Request) []string {
	network := req.Network
	if network == "" {
		network = "none"
	}
	a := []string{"run", "--rm", "-i", "--network", network}
	if runtime.GOOS == "linux" {
		// Files the engine writes into the workspace belong to the person, not
		// to root. Docker Desktop maps ownership itself.
		a = append(a, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	seen := map[string]bool{}
	for _, m := range req.Mounts {
		if seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		v := m.Path + ":" + m.Path
		if m.ReadOnly {
			v += ":ro"
		}
		a = append(a, "-v", v)
	}
	a = append(a, d.Image, "-json")
	if !req.ContinueOnError {
		a = append(a, "-bail")
	}
	if req.ReadOnly {
		a = append(a, "-readonly")
	}
	if req.DB != "" {
		a = append(a, req.DB)
	}
	return a
}

// Run sends the script on stdin and decodes each result set.
func (d DockerEngine) Run(ctx context.Context, req Request) (Output, error) {
	c := exec.CommandContext(ctx, d.bin(), d.args(req)...)
	c.Stdin = strings.NewReader(prelude + req.Setup + req.Script)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	runErr := c.Run()
	msg := strings.TrimSpace(errb.String())
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			if msg == "" {
				msg = runErr.Error()
			}
			return Output{}, nserr.New(nserr.Fail, "running the DuckDB engine (%s): %s. Is the container engine running? `nsctl doctor` checks", d.Image, msg)
		}
		if !req.ContinueOnError {
			if msg == "" {
				msg = runErr.Error()
			}
			return Output{}, nserr.New(nserr.Fail, "DuckDB: %s", msg)
		}
	}
	res, cols, err := decodeResults(out.Bytes())
	if err != nil {
		return Output{}, err
	}
	return Output{Results: res, Columns: cols, Stderr: msg}, nil
}

// Interactive runs a DuckDB shell on the catalog with the terminal attached.
func (d DockerEngine) Interactive(ctx context.Context, req Request) error {
	a := d.args(req)
	// -it rather than -i, and the prelude as an init file is not possible
	// with a read-only mount, so it is passed as -cmd statements.
	for i, s := range a {
		if s == "-i" {
			a[i] = "-it"
		}
	}
	// The setup (a storage secret for a local environment's emulator, whose
	// credentials are fixed and not secret) goes the same way.
	for _, stmt := range strings.Split(strings.TrimSpace(prelude+req.Setup), "\n") {
		if strings.HasPrefix(stmt, ".output") {
			continue
		}
		a = append(a, "-cmd", stmt)
	}
	// -json is for machine output; a person wants the default table mode.
	filtered := a[:0]
	for _, s := range a {
		if s != "-json" && s != "-bail" {
			filtered = append(filtered, s)
		}
	}
	c := exec.CommandContext(ctx, d.bin(), filtered...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// decodeResults reads the concatenated JSON arrays `duckdb -json` prints,
// one per statement that returned rows, keeping each set's column order.
func decodeResults(b []byte) (Result, [][]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var res Result
	var cols [][]string
	bad := func(err error) (Result, [][]string, error) {
		return nil, nil, nserr.New(nserr.Fail, "reading DuckDB output: %v", err)
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return res, cols, nil
		}
		if err != nil {
			return bad(err)
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return bad(fmt.Errorf("expected a result array, got %v", tok))
		}
		var rows []map[string]any
		var order []string
		for dec.More() {
			if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
				return bad(fmt.Errorf("expected a row object"))
			}
			row := map[string]any{}
			var keys []string
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return bad(err)
				}
				k, _ := kt.(string)
				var v any
				if err := dec.Decode(&v); err != nil {
					return bad(err)
				}
				row[k] = v
				keys = append(keys, k)
			}
			if _, err := dec.Token(); err != nil {
				return bad(err)
			}
			if order == nil {
				order = keys
			}
			rows = append(rows, row)
		}
		if _, err := dec.Token(); err != nil {
			return bad(err)
		}
		res = append(res, rows)
		cols = append(cols, order)
	}
}

// str renders a JSON value from a result row as a string.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// num renders a JSON value as an int64, or 0.
func num(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return int64(f)
		}
	case string:
		var n json.Number = json.Number(x)
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}
