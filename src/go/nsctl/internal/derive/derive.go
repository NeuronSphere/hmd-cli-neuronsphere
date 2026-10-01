// Package derive turns what format parsers saw into perspective bindings,
// deriving the perspective definitions from the files themselves (NERD033).
//
// It runs between inspect.Run and model.Consolidate. Inspectors report
// physical objects with grammar-named properties (model.ObjectObs) and raw
// perspective sidecars (model.SidecarObs); derive decides which noun each
// object is, which perspective binding, what each property's value is, and
// what definition those values have. A definition a repository declares is
// used as it stands; otherwise one is derived from the whole corpus, with
// evidence for each piece, and a person's edits are replayed on top of it.
//
// Nothing here knows a technology. The rules are about names and values:
// which name segment varies between objects that are otherwise the same,
// which way data flows between them, how many distinct values a property
// has, and which core .hms type a physical type is paired with.
package derive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/model"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/perspective"
)

// BindingKeyName is the name derivation gives the entity key that tells
// several bindings of one noun apart. A person may rename it.
const BindingKeyName = "layer"

// valueOnlyBindingKey is the binding key of a definition derived from
// values an inspector already named, whose bindings carry names.
const valueOnlyBindingKey = "binding"

// Result is a derivation run.
type Result struct {
	// Observations are the input with every object and sidecar resolved into
	// binding and attribute observations, and every dialect endpoint named.
	Observations []model.Observation
	// Derivations are the definitions derived in this run, edits applied.
	Derivations []*perspective.Derivation
	// Stale are edits that no longer fit what was derived.
	Stale []perspective.Edit
}

// Run derives. reg holds the declared definitions; the derived ones are
// returned, not added to it.
func Run(obs []model.Observation, reg *perspective.Registry, edits []perspective.Edit) Result {
	var res Result
	byDialect := map[string][]model.Observation{}
	var sidecars, lineage, rest []model.Observation
	hmsTypes := map[model.ID]map[string]model.LogicalType{}
	for _, o := range obs {
		switch {
		case o.Kind == model.KindObject && o.Object != nil:
			byDialect[o.Object.Dialect] = append(byDialect[o.Object.Dialect], o)
		case o.Kind == model.KindSidecar && o.Sidecar != nil:
			sidecars = append(sidecars, o)
		case o.Kind == model.KindLineage && o.Lineage != nil:
			lineage = append(lineage, o)
		default:
			if o.Kind == model.KindAttribute && o.Attr != nil && o.Attr.Binding == "" && o.Attr.Type != model.Unknown {
				if hmsTypes[o.Subject] == nil {
					hmsTypes[o.Subject] = map[string]model.LogicalType{}
				}
				hmsTypes[o.Subject][strings.ToLower(o.Attr.Name)] = o.Attr.Type
			}
			rest = append(rest, o)
		}
	}

	known := map[string]*perspective.Definition{}
	for _, n := range reg.Names() {
		known[n] = reg.Get(n)
	}
	namers := map[string]*namer{}
	for _, dialect := range sortedKeys(byDialect) {
		d := &dialectRun{dialect: dialect, objects: byDialect[dialect], lineage: lineage, hmsTypes: hmsTypes}
		name := renamed(dialect, edits)
		if decl := reg.Get(name); decl != nil {
			d.def, d.declared, d.undeclared = decl, true, map[string]map[string]int{}
		} else {
			dv := d.derive()
			dv.Definition.Name, dv.Definition.Display = dialect, dialect
			d.renames, d.dropped, res.Stale = applyEdits(dv, edits, res.Stale)
			d.def = dv.Definition
			res.Derivations = append(res.Derivations, dv)
		}
		known[d.def.Name] = d.def
		namers[dialect] = d.namer()
		res.Observations = append(res.Observations, d.observations()...)
		res.Observations = append(res.Observations, d.undeclaredFindings()...)
	}

	// Lineage endpoints a dialect named by location become nouns.
	for _, o := range lineage {
		l := *o.Lineage
		for _, e := range []*model.Endpoint{&l.From, &l.To} {
			if e.ID.IsZero() && e.Dialect != "" {
				if nm := namers[e.Dialect]; nm != nil {
					e.ID, _ = nm.identity(e.Location)
				}
			}
		}
		o.Lineage = &l
		res.Observations = append(res.Observations, o)
	}

	// Bindings inspectors named themselves, of a perspective nothing
	// defines: derive a definition from their values.
	rest, valueOnly, stale := deriveFromValues(rest, known, edits)
	res.Stale = append(res.Stale, stale...)
	for _, dv := range valueOnly {
		known[dv.Definition.Name] = dv.Definition
		res.Derivations = append(res.Derivations, dv)
	}
	res.Observations = append(res.Observations, rest...)

	for _, o := range sidecars {
		res.Observations = append(res.Observations, resolveSidecar(o, known)...)
	}
	sort.Slice(res.Derivations, func(i, j int) bool {
		return res.Derivations[i].Definition.Name < res.Derivations[j].Definition.Name
	})
	return res
}

// renamed is a perspective's name after rename edits.
func renamed(name string, edits []perspective.Edit) string {
	for _, e := range edits {
		if e.Op == "rename" && e.Perspective == name {
			name = e.To
		}
	}
	return name
}

// applyEdits replays a person's edits on a fresh derivation. Apply follows
// a rename, so later edits naming the new name still apply.
func applyEdits(dv *perspective.Derivation, edits []perspective.Edit, stale []perspective.Edit) (map[string]string, map[string]bool, []perspective.Edit) {
	renames, dropped, bad := dv.Apply(edits)
	return renames, dropped, append(stale, bad...)
}

// ---------------------------------------------------------------------------
// One dialect's objects.

type dialectRun struct {
	dialect  string
	objects  []model.Observation
	lineage  []model.Observation
	hmsTypes map[model.ID]map[string]model.LogicalType

	def      *perspective.Definition
	declared bool
	renames  map[string]string
	dropped  map[string]bool
	// undeclared counts properties a declared definition has no key for.
	undeclared map[string]map[string]int

	nm *namer
}

// key is the key a property maps to. A derived definition knows the edits
// made to it; a declared one maps through its keys' aliases, and a property
// it does not declare is recorded as undeclared rather than given a value.
func (d *dialectRun) key(at perspective.Attach, k string) (string, bool) {
	if d.dropped[k] {
		return "", false
	}
	if to, ok := d.renames[k]; ok {
		return to, true
	}
	if d.declared {
		if key, ok := d.def.Resolve(at, k); ok {
			return key, true
		}
		if d.undeclared[k] == nil {
			d.undeclared[k] = map[string]int{}
		}
		d.undeclared[k][string(at)]++
		return "", false
	}
	return k, true
}

// namer is a dialect's name pattern: how a physical location names a noun
// and a binding.
type namer struct {
	// variants are the schema suffixes that name a binding, in pipeline
	// order.
	variants []string
	// schema, when a declared definition gives a name pattern, matches a
	// schema to namespace and binding name.
	schema *regexp.Regexp
	nsIdx  int
	keyIdx int
}

func (d *dialectRun) namer() *namer {
	if d.nm != nil {
		return d.nm
	}
	if d.def != nil && d.def.NamePattern != nil && len(d.def.NamePattern.Schema) > 0 {
		d.nm = patternNamer(d.def)
		return d.nm
	}
	variants, _ := d.variants()
	d.nm = &namer{variants: variants}
	return d.nm
}

// patternNamer compiles a declared schema name pattern.
func patternNamer(def *perspective.Definition) *namer {
	nm := &namer{nsIdx: -1, keyIdx: -1}
	var re strings.Builder
	re.WriteString("^")
	group := 0
	for _, p := range def.NamePattern.Schema {
		switch {
		case p.Lit != "":
			re.WriteString(regexp.QuoteMeta(p.Lit))
		case p.Ref == "namespace":
			group++
			nm.nsIdx = group
			re.WriteString("(.+?)")
		case p.Ref != "":
			group++
			if p.Ref == def.BindingKey {
				nm.keyIdx = group
			}
			ext, _ := def.Extension(perspective.Entity, p.Ref)
			var alts []string
			for _, v := range ext.EnumValues {
				alts = append(alts, regexp.QuoteMeta(v.ID))
				nm.variants = append(nm.variants, v.ID)
			}
			if len(alts) > 0 {
				re.WriteString("(" + strings.Join(alts, "|") + ")")
			} else {
				re.WriteString("([^_]+)")
			}
		case p.Runtime != "":
			re.WriteString(".*?")
		}
	}
	re.WriteString("$")
	nm.schema = regexp.MustCompile(re.String())
	return nm
}

// identity is the noun and binding name a location stands for.
func (nm *namer) identity(l model.Location) (model.ID, string) {
	schema, layer := l.Schema, ""
	ns := schema
	if nm.schema != nil {
		if m := nm.schema.FindStringSubmatch(schema); m != nil {
			if nm.nsIdx > 0 {
				ns = m[nm.nsIdx]
			}
			if nm.keyIdx > 0 {
				layer = m[nm.keyIdx]
			}
		}
	} else if i := strings.LastIndex(schema, "_"); i > 0 {
		for _, v := range nm.variants {
			if schema[i+1:] == v {
				ns, layer = schema[:i], v
			}
		}
	}
	return model.ID{Namespace: ns, Name: Stem(l.Table)}, layer
}

var placeholderSegment = regexp.MustCompile(`_?\{[^}]*\}`)

// Stem is a physical name without its run-time {placeholder} segments: the
// part of the name that is the same on every run.
func Stem(table string) string {
	return strings.Trim(placeholderSegment.ReplaceAllString(table, ""), "_")
}

func hasPlaceholder(l model.Location) bool {
	return strings.Contains(l.Table, "{") || strings.Contains(l.Schema, "{")
}

// variants finds the schema suffixes that tell objects of one noun apart:
// objects with the same table name, in schemas that differ only in their
// last _-separated segment. The evidence lists the groups that showed it.
func (d *dialectRun) variants() ([]string, perspective.Evidence) {
	type split struct{ prefix, suffix string }
	groups := map[string]map[split]bool{}
	for _, o := range d.objects {
		l := o.Object.Location
		i := strings.LastIndex(l.Schema, "_")
		if i <= 0 {
			continue
		}
		stem := strings.ToLower(Stem(l.Table))
		if groups[stem] == nil {
			groups[stem] = map[split]bool{}
		}
		groups[stem][split{l.Schema[:i], l.Schema[i+1:]}] = true
	}
	found := map[string]bool{}
	ev := perspective.Evidence{Item: "binding_key",
		Rule: "the same table name in schemas that differ only in their last _-separated segment"}
	for _, stem := range sortedKeys(groups) {
		byPrefix := map[string][]string{}
		for s := range groups[stem] {
			byPrefix[s.prefix] = append(byPrefix[s.prefix], s.suffix)
		}
		for _, prefix := range sortedKeys(byPrefix) {
			suffixes := byPrefix[prefix]
			if len(suffixes) < 2 {
				continue
			}
			sort.Strings(suffixes)
			ev.Support++
			ev.Examples = append(ev.Examples, fmt.Sprintf("%s_{%s}.%s", prefix, strings.Join(suffixes, ","), stem))
			for _, s := range suffixes {
				found[s] = true
			}
		}
	}
	return d.order(found), ev
}

// order puts binding names in the direction data flows between them, read
// from lineage between two bindings of one noun; names no lineage orders
// follow, alphabetically.
func (d *dialectRun) order(names map[string]bool) []string {
	if len(names) == 0 {
		return nil
	}
	nm := &namer{variants: sortedKeys(names)}
	after := map[string]map[string]bool{}
	indeg := map[string]int{}
	for n := range names {
		after[n] = map[string]bool{}
		indeg[n] = 0
	}
	for _, o := range d.lineage {
		f, t := o.Lineage.From, o.Lineage.To
		if f.Dialect != d.dialect || t.Dialect != d.dialect {
			continue
		}
		fid, fl := nm.identity(f.Location)
		tid, tl := nm.identity(t.Location)
		if fid != tid || fl == tl || fl == "" || tl == "" || after[fl][tl] {
			continue
		}
		after[fl][tl] = true
		indeg[tl]++
	}
	var out []string
	for len(out) < len(names) {
		var ready []string
		for n, deg := range indeg {
			if deg == 0 {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 { // a cycle: the rest alphabetically
			for _, n := range sortedKeys(indeg) {
				out = append(out, n)
			}
			break
		}
		sort.Strings(ready)
		n := ready[0]
		out = append(out, n)
		delete(indeg, n)
		for m := range after[n] {
			if _, ok := indeg[m]; ok {
				indeg[m]--
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
