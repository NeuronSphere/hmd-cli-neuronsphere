package data

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/nserr"
)

// Storage (NERD039 SPEC009) is opt-in and always the S3 service of a local
// NeuronSphere environment. Once a table is landed, its view reads the
// Parquet in the bucket, and every engine session joins the environment's
// network with the environment's credentials. nsctl data never deploys
// anything: the bucket is the person's, or an hmd-inf-s3bucket instance they
// added with the storage bundle.

// Storage is data.yaml's storage target.
type Storage struct {
	Env string `yaml:"env" json:"env"`
	// Target is what the person named: s3://bucket[/prefix] or
	// instance:<name>.
	Target string `yaml:"target" json:"target"`
	// Bucket and Prefix are where tables land, resolved from Target.
	Bucket string `yaml:"bucket" json:"bucket"`
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
}

// Remote is how the engine reaches an environment's S3 from inside the
// environment's network. It is resolved from the environment each run and
// never written to data.yaml.
type Remote struct {
	Network string
	// Endpoint is host:port inside the network, e.g. floci:4566.
	Endpoint string
	Region   string
	KeyID    string
	Secret   string
}

// session completes a request: the workspace's mounts, and, when storage is
// configured, the environment's network and an S3 secret.
func (w *Workspace) session(r Request) Request { return w.Session(r) }

// Session is session for callers outside the package (the interactive shell).
func (w *Workspace) Session(r Request) Request {
	r.Mounts = w.Mounts()
	if w.Remote != nil {
		r.Network = w.Remote.Network
		r.Setup = w.Remote.secretSQL()
	}
	return r
}

// secretSQL is a session-scoped S3 secret for the environment's endpoint.
// It is never persisted in the catalog. CREATE SECRET returns a row, which
// would shift every result set after it, so its output is discarded.
func (r *Remote) secretSQL() string {
	return ".output /dev/null\n" + r.SecretStatement() + ".output\n"
}

// SecretStatement is the CREATE SECRET a DuckDB needs to read the landed
// tables, for an endpoint as seen from inside the environment's network.
func (r *Remote) SecretStatement() string {
	return fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET ns_storage (TYPE s3, KEY_ID %s, SECRET %s, REGION %s, ENDPOINT %s, URL_STYLE 'path', USE_SSL false);\n",
		lit(r.KeyID), lit(r.Secret), lit(r.Region), lit(r.Endpoint))
}

// ParseTarget reads --to: s3://bucket[/prefix] or instance:<name>.
func ParseTarget(to string) (bucket, prefix, instance string, err error) {
	if name, ok := strings.CutPrefix(to, "instance:"); ok {
		if name == "" {
			return "", "", "", nserr.New(nserr.Usage, "--to instance:<name> needs the instance's name")
		}
		return "", "", name, nil
	}
	u, err := url.Parse(to)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return "", "", "", nserr.New(nserr.Usage, "--to %q: want s3://<bucket>[/<prefix>] or instance:<name>", to)
	}
	return u.Host, strings.Trim(u.Path, "/"), "", nil
}

// LandedURL is where a table's Parquet lands: <target>/raw/<table>/data.parquet.
func (s *Storage) LandedURL(table string) string {
	parts := []string{"s3://" + s.Bucket}
	if s.Prefix != "" {
		parts = append(parts, s.Prefix)
	}
	parts = append(parts, "raw", table, "data.parquet")
	return strings.Join(parts, "/")
}

// LandReport is one table after land.
type LandReport struct {
	Table string `json:"table"`
	URL   string `json:"url"`
	Rows  int64  `json:"rows"`
	// Unchanged is true when the sources have not changed since the last
	// land, so nothing was written.
	Unchanged bool `json:"unchanged"`
}

// Land writes a table as Parquet into the storage target and points its
// view there. It is idempotent by the sources' content hashes unless force.
// The caller sets w.Config.Storage and w.Remote first.
func (w *Workspace) Land(ctx context.Context, eng Engine, name string, force bool) (*LandReport, error) {
	st := w.Config.Storage
	if st == nil || w.Remote == nil {
		return nil, nserr.New(nserr.Usage, "no storage target; pass --env and --to")
	}
	t, err := w.Table(name)
	if err != nil {
		return nil, err
	}
	hashes, err := w.sourceHashes(t)
	if err != nil {
		return nil, err
	}
	dest := st.LandedURL(name)
	if prev, ok := w.Config.Landed[name]; ok && !force && prev.Path == dest && sameHashes(prev.Hashes, hashes) {
		return &LandReport{Table: name, URL: dest, Rows: prev.Rows, Unchanged: true}, nil
	}
	// Read from the files (or a snapshot), never from an earlier landing.
	delete(w.Config.Landed, name)
	src, err := w.viewSource(t)
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf("COPY (%s) TO %s (FORMAT parquet);\nSELECT count(*) AS n FROM read_parquet(%s);\n", src, lit(dest), lit(dest))
	out, err := eng.Run(ctx, w.session(Request{Script: script}))
	if err != nil {
		return nil, err
	}
	rep := &LandReport{Table: name, URL: dest}
	if len(out.Results) > 0 && len(out.Results[0]) > 0 {
		rep.Rows = num(out.Results[0][0]["n"])
	}
	if w.Config.Landed == nil {
		w.Config.Landed = map[string]Snapshot{}
	}
	w.Config.Landed[name] = Snapshot{Path: dest, Hashes: hashes, Rows: rep.Rows, At: time.Now().UTC().Format(time.RFC3339)}
	w.Mark(name, StepLanded)
	if err := w.Save(); err != nil {
		return nil, err
	}
	return rep, nil
}

// sourceHashes fingerprints a table's source files.
func (w *Workspace) sourceHashes(t *Table) (map[string]string, error) {
	files, err := w.tableFiles(t)
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for _, f := range files {
		root, _ := w.SourcePath(f.Source)
		h, err := fileHash(filepath.Join(root, filepath.FromSlash(f.Rel)))
		if err != nil {
			return nil, nserr.Wrap(nserr.Fail, err)
		}
		hashes[f.Rel] = h
	}
	return hashes, nil
}

func sameHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
