package bacon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/artifact"
)

// Severity is how much a finding predicts (SPEC011): an error will fail a
// deploy, a warning will surprise, a note is inert.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Note    Severity = "note"
)

// Finding is one thing validate has to say.
type Finding struct {
	Severity Severity
	// Path is the key path the finding is about, dotted, or a file name.
	Path    string
	Message string
}

// String is the line validate prints: severity, the key path, what is wrong
// with it. `error: name is required`, `warning: meta-data/VERSION is absent`.
func (f Finding) String() string {
	if f.Path == "" {
		return fmt.Sprintf("%s: %s", f.Severity, f.Message)
	}
	return fmt.Sprintf("%s: %s %s", f.Severity, f.Path, f.Message)
}

// Known is what the validator cannot learn from the repo: the names that
// collide. Passed in so this package stays free of the bundled registry and
// the environment manifest's reserved list.
type Known struct {
	BundledClasses []string
	ReservedNames  []string
}

var (
	buildMechanisms  = map[string]bool{"tool_set": true, "external": true}
	capabilityKinds  = map[string]bool{"endpoint": true, "cli_command": true, "function": true, "class": true, "operation": true}
	requiredStrings  = map[string]bool{"true": true, "false": true}
	majorMinor       = regexp.MustCompile(`^\d+\.\d+$`)
	nativeSourceDirs = map[string]string{
		"helm":   "src/helm/Chart.yaml",
		"docker": "src/docker/Dockerfile",
		"cdktf":  "src/cdktf",
	}
)

// Validate runs the structural pass -- the BACON schema's shape, hand-coded
// from hmd-docs-bacon docs/reference/schema.rst -- and the semantic pass of
// SPEC011. It never writes.
func Validate(s *Store, known Known) []Finding {
	v := &validator{doc: s.Doc, dir: s.Dir, known: known}
	v.structure()
	v.semantics()
	sort.SliceStable(v.findings, func(i, j int) bool {
		return severityRank(v.findings[i].Severity) < severityRank(v.findings[j].Severity)
	})
	return v.findings
}

// Summary counts findings by severity.
func Summary(findings []Finding) (errors, warnings, notes int) {
	for _, f := range findings {
		switch f.Severity {
		case Error:
			errors++
		case Warning:
			warnings++
		case Note:
			notes++
		}
	}
	return
}

func severityRank(s Severity) int {
	switch s {
	case Error:
		return 0
	case Warning:
		return 1
	default:
		return 2
	}
}

type validator struct {
	doc      *Object
	dir      string
	known    Known
	findings []Finding
}

func (v *validator) add(sev Severity, path, format string, a ...any) {
	v.findings = append(v.findings, Finding{Severity: sev, Path: path, Message: fmt.Sprintf(format, a...)})
}

// structure is the schema: required members, types, enums.
func (v *validator) structure() {
	for _, key := range []string{"name", "description"} {
		if s, ok := v.doc.String(key); !ok || s == "" {
			if _, present := v.doc.Get(key); present {
				v.add(Error, key, "must be a non-empty string")
			} else {
				v.add(Error, key, "is required")
			}
		}
	}
	if _, ok := v.doc.Get("build"); !ok {
		v.add(Error, "build", "is required (an empty object is enough)")
	}

	for _, section := range []string{"build", "deploy", "test"} {
		raw, ok := v.doc.Get(section)
		if !ok {
			continue
		}
		obj, ok := raw.(*Object)
		if !ok {
			v.add(Error, section, "must be an object")
			continue
		}
		v.commands(section, obj)
		if mech, ok := obj.Get("mechanism"); ok {
			if s, isStr := mech.(string); !isStr || !buildMechanisms[s] {
				v.add(Error, section+".mechanism", "must be tool_set or external")
			}
		}
	}

	if deploy, ok := v.doc.Object("deploy"); ok {
		v.dependencyMap("deploy.dependencies", deploy, "dependencies")
		if raw, ok := deploy.Get("resources"); ok {
			if resources, isObj := raw.(*Object); isObj {
				for _, name := range resources.Keys() {
					path := "deploy.resources." + name
					res, ok := resources.Object(name)
					if !ok {
						v.add(Error, path, "must be an object")
						continue
					}
					for _, key := range []string{"resource_namespace", "resource_definition_name", "version"} {
						if s, ok := res.String(key); !ok || s == "" {
							v.add(Error, path+"."+key, "is required")
						}
					}
					if p, ok := res.Get("produces"); ok {
						if _, isBool := p.(bool); !isBool {
							v.add(Error, path+".produces", "must be a boolean")
						}
					}
				}
			} else {
				v.add(Error, "deploy.resources", "must be an object keyed by resource name")
			}
		}
		if raw, ok := deploy.Get("default_configuration"); ok {
			if _, isObj := raw.(*Object); !isObj {
				v.add(Error, "deploy.default_configuration", "must be an object")
			}
		}
	}
	if test, ok := v.doc.Object("test"); ok {
		v.dependencyMap("test.targets", test, "targets")
	}
	v.discovery()
	v.licence()
}

// licence checks the shape of the `license` declaration (NERD017 SPEC011)
// and nothing more: which paths an author keeps out of what they publish
// is theirs to decide, and an exclude that covers a tool's source directory
// is deliberate in the repos that do it (the deploy re-tags an image it
// never builds).
func (v *validator) licence() {
	raw, ok := v.doc.Get("license")
	if !ok {
		return
	}
	switch l := raw.(type) {
	case string:
		if strings.TrimSpace(l) == "" {
			v.add(Error, "license", "must be a non-empty SPDX expression")
		}
	case *Object:
		if s, ok := l.String("spdx"); !ok || strings.TrimSpace(s) == "" {
			v.add(Error, "license.spdx", "is required: the SPDX expression of what is published")
		}
		if rawEx, ok := l.Get("exclude"); ok {
			list, ok := rawEx.([]any)
			if !ok {
				v.add(Error, "license.exclude", "must be a list of paths relative to the repository root")
			} else {
				for i, e := range list {
					path := fmt.Sprintf("license.exclude[%d]", i)
					s, ok := e.(string)
					if !ok {
						v.add(Error, path, "must be a string")
						continue
					}
					if _, err := artifact.CleanExclude(s); err != nil {
						v.add(Error, path, "%v", err)
					}
				}
			}
		}
		for _, key := range l.Keys() {
			if key != "spdx" && key != "exclude" {
				v.add(Warning, "license."+key, "is not part of the declaration and is ignored")
			}
		}
	default:
		v.add(Error, "license", "must be an SPDX string or an object with spdx and exclude")
	}
}

func (v *validator) commands(section string, obj *Object) {
	raw, ok := obj.Get("commands")
	if !ok {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.add(Error, section+".commands", "must be a list of command lists")
		return
	}
	for i, entry := range list {
		argv, ok := entry.([]any)
		path := fmt.Sprintf("%s.commands[%d]", section, i)
		if !ok {
			v.add(Error, path, "must be a list: a tool name followed by its arguments")
			continue
		}
		if len(argv) == 0 {
			v.add(Error, path, "is empty; a command names its tool first")
			continue
		}
		if _, isStr := argv[0].(string); !isStr {
			v.add(Error, path, "must start with the tool's name")
		}
	}
}

func (v *validator) dependencyMap(path string, parent *Object, key string) {
	raw, ok := parent.Get(key)
	if !ok {
		return
	}
	deps, ok := raw.(*Object)
	if !ok {
		v.add(Error, path, "must be an object keyed by role")
		return
	}
	for _, role := range deps.Keys() {
		p := path + "." + role
		dep, ok := deps.Object(role)
		if !ok {
			v.add(Error, p, "must be an object")
			continue
		}
		if req, ok := dep.Get("required"); ok {
			switch t := req.(type) {
			case string:
				if !requiredStrings[t] {
					v.add(Error, p+".required", "must be the string \"true\" or \"false\", got %q", t)
				}
			case bool:
				// SPEC011: the schema's enum is the strings; a boolean is what
				// every hand-written manifest gets wrong first.
				v.add(Warning, p+".required", "is a JSON boolean; the schema wants the string \"%t\"", t)
			default:
				v.add(Error, p+".required", "must be the string \"true\" or \"false\"")
			}
		}
		if raw, ok := dep.Get("resource"); ok {
			res, isObj := raw.(*Object)
			if !isObj {
				v.add(Error, p+".resource", "must be an object")
				continue
			}
			for _, key := range []string{"resource_namespace", "resource_definition_name", "version"} {
				if s, ok := res.String(key); !ok || s == "" {
					v.add(Error, p+".resource."+key, "is required when resource is present")
				}
			}
			if raw, ok := res.Get("tag_selector"); ok {
				if _, isObj := raw.(*Object); !isObj {
					v.add(Error, p+".resource.tag_selector", "must be an object of string values")
				}
			}
		}
	}
}

func (v *validator) discovery() {
	raw, ok := v.doc.Get("discovery")
	if !ok {
		return
	}
	disc, ok := raw.(*Object)
	if !ok {
		v.add(Error, "discovery", "must be an object")
		return
	}
	if s, ok := disc.Get("summary"); ok {
		if _, isStr := s.(string); !isStr {
			v.add(Error, "discovery.summary", "must be a string")
		}
	}
	v.discoveryList(disc, "entry_points", []string{"path", "description"}, nil)
	v.discoveryList(disc, "capabilities", []string{"name", "kind", "description"}, func(path string, item *Object) {
		if kind, _ := item.String("kind"); !capabilityKinds[kind] {
			v.add(Error, path+".kind", "must be one of endpoint, cli_command, function, class, operation")
		}
	})
	v.discoveryList(disc, "related_docs", []string{"title", "path"}, nil)
}

func (v *validator) discoveryList(disc *Object, key string, required []string, extra func(string, *Object)) {
	raw, ok := disc.Get(key)
	if !ok {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.add(Error, "discovery."+key, "must be a list")
		return
	}
	for i, entry := range list {
		path := fmt.Sprintf("discovery.%s[%d]", key, i)
		item, ok := entry.(*Object)
		if !ok {
			v.add(Error, path, "must be an object")
			continue
		}
		for _, field := range required {
			if s, ok := item.String(field); !ok || s == "" {
				v.add(Error, path+"."+field, "is required")
			}
		}
		if extra != nil {
			extra(path, item)
		}
	}
}

// semantics is SPEC011's three lists.
func (v *validator) semantics() {
	deploy, hasDeploy := v.doc.Object("deploy")

	if hasDeploy {
		// Error: a deploy section with neither commands nor a resolvable
		// command is a bare KeyError in hmd-cli-deploy -- except a stack
		// (NERD017 SPEC001), which names its companions rather than
		// deploying an instance of itself.
		commands, _ := deploy.Array("commands")
		if len(commands) == 0 && !v.isStack() && !v.hasLocalDeployScript() {
			v.add(Error, "deploy.commands", "deploy declares no commands and there is no src/local/deploy_local.sh; nothing can deploy this")
		}
		execs := 0
		for i, entry := range commands {
			argv, ok := entry.([]any)
			if !ok || len(argv) == 0 {
				continue
			}
			tool, _ := argv[0].(string)
			path := fmt.Sprintf("deploy.commands[%d]", i)
			if tool == "exec" {
				execs++
				if len(argv) == 1 {
					v.add(Error, path, "exec has no command to run")
				}
				continue
			}
			if rel, native := nativeSourceDirs[tool]; native {
				if _, err := os.Stat(filepath.Join(v.dir, filepath.FromSlash(rel))); err != nil {
					v.add(Error, path, "%s deploys from %s, which does not exist", tool, rel)
				}
			}
		}
		if execs > 1 {
			v.add(Error, "deploy.commands", "more than one exec command; exactly one per phase (NERD009 SPEC003)")
		}
		if execs > 0 && execs < len(commands) {
			v.add(Error, "deploy.commands", "exec beside another tool; a phase is either exec or tool set commands")
		}
		if execs > 0 {
			v.add(Note, "deploy.commands", "exec is nsctl's; hmd deploy does not implement it yet (NERD009 SPEC012)")
		}
		if _, ok := deploy.Get("mechanism"); ok {
			v.add(Note, "deploy.mechanism", "is read by no installed tool set; it records intent and changes nothing")
		}

		// Error: a required dependency with neither a class nor a resource
		// is unresolvable by construction.
		if deps, ok := deploy.Object("dependencies"); ok {
			for _, role := range deps.Keys() {
				dep, ok := deps.Object(role)
				if !ok {
					continue
				}
				req, _ := dep.Get("required")
				if !requiredIsTrue(req) && req != nil {
					continue
				}
				_, hasClass := dep.Get("repo_class_name")
				_, hasResource := dep.Get("resource")
				if !hasClass && !hasResource {
					v.add(Error, "deploy.dependencies."+role, "is required but names neither a repo_class_name nor a resource; nothing can satisfy it")
				}
			}
		}
	}

	// Warning: VERSION absent or not MAJOR.MINOR.
	version := filepath.Join(v.dir, filepath.FromSlash(VersionFile))
	if data, err := os.ReadFile(version); err != nil {
		v.add(Warning, VersionFile, "is absent; the class registers under the sentinel 0.1.0, a version it never declared (NERD009 SPEC007)")
	} else if s := strings.TrimSpace(string(data)); !majorMinor.MatchString(s) {
		v.add(Warning, VersionFile, "is %q, not MAJOR.MINOR", s)
	}

	// Warning: a resources yaml missing its identity keys is skipped silently.
	v.resourceFiles()

	// Warning: a name that collides.
	name, _ := v.doc.String("name")
	if name != "" {
		for _, bundled := range v.known.BundledClasses {
			if name == bundled {
				v.add(Warning, "name", "%q is also a repo class bundled into nsctl; deploying both in one environment resolves to one of them", name)
			}
		}
		for _, reserved := range v.known.ReservedNames {
			if name == reserved {
				v.add(Warning, "name", "%q is a reserved instance name; `nsctl repo add` refuses an instance called this", name)
			}
		}
	}
}

func (v *validator) hasLocalDeployScript() bool {
	_, err := os.Stat(filepath.Join(v.dir, "src", "local", "deploy_local.sh"))
	return err == nil
}

// isStack reports local.stack (NERD017 SPEC001): true means this RepoClass
// exists to name other RepoClasses and is never itself instanced or deployed.
func (v *validator) isStack() bool {
	local, ok := v.doc.Object("local")
	if !ok {
		return false
	}
	stack, _ := local.Get("stack")
	b, _ := stack.(bool)
	return b
}

func (v *validator) resourceFiles() {
	matches, _ := filepath.Glob(filepath.Join(v.dir, "meta-data", "resources", "*.yaml"))
	for _, path := range matches {
		rel := filepath.ToSlash(filepath.Join("meta-data", "resources", filepath.Base(path)))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			v.add(Warning, rel, "is not valid YAML: %v", err)
			continue
		}
		for _, key := range []string{"resource_namespace", "resource_definition_name"} {
			if s, _ := doc[key].(string); s == "" {
				v.add(Warning, rel, "has no %s; the resource it declares is skipped silently", key)
			}
		}
	}
}

// JSONFindings is the --json form.
func JSONFindings(findings []Finding) ([]byte, error) {
	type item struct {
		Severity Severity `json:"severity"`
		Path     string   `json:"path,omitempty"`
		Message  string   `json:"message"`
	}
	items := make([]item, 0, len(findings))
	for _, f := range findings {
		items = append(items, item{f.Severity, f.Path, f.Message})
	}
	e, w, n := Summary(findings)
	return json.MarshalIndent(struct {
		Errors   int    `json:"errors"`
		Warnings int    `json:"warnings"`
		Notes    int    `json:"notes"`
		Findings []item `json:"findings"`
	}{e, w, n, items}, "", "  ")
}
