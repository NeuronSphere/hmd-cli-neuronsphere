// Package nsexport inspects NeuronSphere export producers and the Artifact
// Librarian content types that connect them to their consumers:
//
//   - a Python producer (hmd-tf-*) that reads graph nouns with a Gremlin
//     hasLabel(...).project(...) query and uploads the result to the
//     Librarian under a literal content item type;
//   - content item type entities (src/entities/hmd_lang_librarian.content_item_type/*)
//     that declare those types.
//
// The content item type name is the join key: the export is a noun
// librarian.<content type>, derived from the graph noun it projects, and a
// transform that queries that item_type (see nstransform) reads it. Python is
// not parsed; these are the literal shapes the producers use, and everything
// learned here is evidence, not a decision.
package nsexport

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/inspect"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
)

// Namespace is where content item types live in the model.
const Namespace = "librarian"

// ContentTypeID is the noun a Librarian content item type stands for.
func ContentTypeID(name string) model.ID { return model.ID{Namespace: Namespace, Name: name} }

// Inspector is the export inspector.
type Inspector struct{}

func (Inspector) Name() string { return "nsexport" }

const citDir = "src/entities/hmd_lang_librarian.content_item_type"

func (Inspector) CanInspect(_ context.Context, src inspect.Source) bool {
	for _, d := range []string{"src/python", citDir} {
		if info, err := fs.Stat(src.FS, d); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

var (
	contentTypeRE = regexp.MustCompile(`content_item_(?:type|item)\s*=\s*["']([A-Za-z0-9_]+)["']`)
	hasLabelRE    = regexp.MustCompile(`hasLabel\(\s*["']([\w.]+)["']\s*\)`)
	projectRE     = regexp.MustCompile(`\.project\(([^)]*)\)`)
	quotedRE      = regexp.MustCompile(`["']([^"']+)["']`)
)

func (Inspector) Inspect(_ context.Context, src inspect.Source) ([]model.Observation, error) {
	var obs []model.Observation
	if _, err := fs.Stat(src.FS, "src/python"); err == nil {
		files, err := inspect.Walk(src, "src/python", func(p string) bool {
			return strings.HasSuffix(p, ".py") && !strings.Contains(p, "/tests/")
		})
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, err := fs.ReadFile(src.FS, f)
			if err != nil {
				return nil, err
			}
			obs = append(obs, inspectProducer(f, string(data))...)
		}
	}
	if _, err := fs.Stat(src.FS, citDir); err == nil {
		files, err := inspect.Walk(src, citDir, func(p string) bool { return strings.HasSuffix(p, ".hmdentity") })
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			data, err := fs.ReadFile(src.FS, f)
			if err != nil {
				return nil, err
			}
			var cit struct {
				ShortName  string `json:"short_name"`
				MatchRegex string `json:"match_regex"`
				MimeType   string `json:"mime_type"`
			}
			if err := json.Unmarshal(data, &cit); err != nil || cit.ShortName == "" {
				continue
			}
			p := model.Provenance{File: f, Line: 1, Authority: model.AuthDDL, Confidence: model.Decided, Why: "content item type entity"}
			obs = append(obs, model.Observation{Kind: model.KindProvide, Provenance: p,
				Named: &model.NamedObs{Kind: "content-type", Key: cit.ShortName}})
			if strings.HasSuffix(cit.ShortName, "_parquet") && cit.MimeType != "" && !strings.Contains(cit.MimeType, "parquet") {
				obs = append(obs, model.Observation{Kind: model.KindFinding, Subject: ContentTypeID(cit.ShortName), Provenance: p,
					Finding: &model.FindingObs{Severity: model.SevInfo, Code: "cit-mime-type",
						Message: fmt.Sprintf("content type %s is parquet but declares mime_type %q", cit.ShortName, cit.MimeType)}})
			}
		}
	}
	return obs, nil
}

func inspectProducer(file, text string) []model.Observation {
	types := contentTypeRE.FindAllStringSubmatchIndex(text, -1)
	if len(types) == 0 {
		return nil
	}
	var obs []model.Observation
	label := hasLabelRE.FindStringSubmatchIndex(text)
	var columns []string
	if label != nil {
		if pm := projectRE.FindStringSubmatch(text[label[1]:]); pm != nil {
			for _, q := range quotedRE.FindAllStringSubmatch(pm[1], -1) {
				columns = append(columns, q[1])
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range types {
		name := text[m[2]:m[3]]
		if seen[name] {
			continue
		}
		seen[name] = true
		line := strings.Count(text[:m[0]], "\n") + 1
		id := ContentTypeID(name)
		key := "librarian:" + name
		p := model.Provenance{File: file, Line: line, Authority: model.AuthInferred, Confidence: model.Evidence,
			Why: "a producer uploads Librarian content of this type"}
		obs = append(obs,
			model.Observation{Kind: model.KindManifestation, Subject: id, Provenance: p,
				Manifest: &model.ManifestObs{Key: key, Tech: "librarian-content", Format: "parquet", Primary: true}},
			model.Observation{Kind: model.KindReference, Provenance: p,
				Named: &model.NamedObs{Kind: "content-type", Key: name, Via: "uploaded as"}},
		)
		if label == nil {
			continue
		}
		from := model.ParseID(text[label[2]:label[3]])
		lp := p
		lp.Line = strings.Count(text[:label[0]], "\n") + 1
		lp.Why = "graph query projects this noun into the export"
		obs = append(obs, model.Observation{Kind: model.KindLineage, Provenance: lp,
			Lineage: &model.LineageObs{From: model.Endpoint{ID: from}, To: model.Endpoint{ID: id}, Via: "graph-export"}})
		for i, c := range columns {
			obs = append(obs, model.Observation{Kind: model.KindAttribute, Subject: id, Provenance: lp,
				Attr: &model.AttrObs{Name: c, Manifestation: key, Position: i + 1, Type: model.Unknown}})
		}
	}
	return obs
}
