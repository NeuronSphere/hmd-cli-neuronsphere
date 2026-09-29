// Package credentials resolves a RepoClass's `access` declaration against a
// deployed environment: the URL a human opens, the user who logs in, and where
// the credential actually is (NERD023 SPEC004).
//
// It exists because the alternative is inference. nsctl can see an Ingress host
// and a Secret in a cluster; it cannot know which host a human uses, which
// secret backs it, or that self-registration is off. The RepoClass author knows,
// and a confident wrong answer about a credential is worse than no answer -- so
// this package reads a declaration and reports an entry it could not resolve as
// unresolved rather than guessing at it.
//
// Resolving is separate from revealing. Resolve fills in the placeholders and
// finds where the secret lives; it does not read a value unless asked, and
// nothing here prints one.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/bacon"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/floci"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/manifest"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/router"
)

// Entry is one resolved way in to one deployed instance.
type Entry struct {
	Instance  string
	RepoClass string
	Name      string
	URL       string
	Username  string
	Notes     string

	// Store and Key are where the credential is, empty when the entry declares
	// no secret. Reported even when the value is not: "it is in Parameter Store
	// under this name" is the answer most readers actually need, and it is safe
	// to print.
	Store    string
	Key      string
	Property string

	// Secret is the resolved value, filled only when the caller asked to reveal
	// it and the lookup succeeded.
	Secret string

	// Problem is why this entry did not fully resolve, empty when it did. An
	// entry that failed is still reported: dropping it would lose the only
	// place its name appears, and "declared, and here is why it is not
	// answering" is the report a reader needs.
	Problem string

	// superseded marks a class entry a stack replaced. Unexported: it is
	// bookkeeping between the two passes, never part of the report.
	superseded bool

	// Stack is the stack whose own declaration produced this entry, empty when
	// the instance's class declared it. Reported because a stack entry
	// supersedes the class's, and a reader who finds a username that is not the
	// one the class documents needs to know what replaced it (NERD023 SPEC007).
	Stack string
}

// HasSecret reports whether the entry names a credential at all.
func (e Entry) HasSecret() bool { return e.Store != "" && e.Key != "" }

// ClassReader loads a repo class's BACON manifest. Injected so this package
// stays free of the resolver's six tiers of where a tree might be.
type ClassReader func(repoClass string) (*bacon.Store, error)

// SecretGetter reads a secret's raw value from a named store.
type SecretGetter func(ctx context.Context, store, name string) (string, error)

// Options is what Resolve needs to know about the environment.
type Options struct {
	// Environment is the slug, which is also the {environment} placeholder: a
	// local Environment entity is typed by its slug, and every name the charts
	// build uses that same value.
	Environment  string
	DeploymentID string
	// Repos are the instances the environment manifest declares.
	Repos []manifest.Repo
	// OutputDir holds the resource outputs recorded at deploy time, keyed by
	// instance name. Empty means an entry whose secret names an output key
	// cannot resolve, which is reported rather than guessed around.
	OutputDir string
	// Class loads a repo class's manifest; required.
	Class ClassReader
	// Get reads a secret. nil means resolve but never reveal, which is the
	// default path and the one that needs no Floci at all.
	Get SecretGetter
	// Instance, when set, limits the result to that one instance.
	Instance string
	// Stacks are the stacks this environment declared. A stack is a RepoClass,
	// so its own `access` is read through Class like any other -- what it adds
	// is that its entries name the instance they describe, and supersede that
	// instance's class entry of the same name (NERD023 SPEC007).
	//
	// A record written before the class was persisted carries none, and its
	// declaration is then unreachable; that is reported as a problem rather than
	// passed over, because a stack that declares access and is silently ignored
	// is worse than one that says why.
	Stacks []manifest.StackRecord
}

// Resolve returns every declared way in to the environment's instances, sorted
// by instance then entry name so the same environment reports the same bytes
// twice.
//
// reveal asks for the values. It is a parameter rather than an Options field
// because it is the one decision that changes what the result contains, and a
// caller should have to say it at the call site.
func Resolve(ctx context.Context, o Options, reveal bool) []Entry {
	var out []Entry
	for _, repo := range o.Repos {
		if o.Instance != "" && repo.InstanceName != o.Instance {
			continue
		}
		if repo.RepoClassName == "" {
			continue
		}
		store, err := o.Class(repo.RepoClassName)
		if err != nil {
			// Not a problem worth reporting on its own: most classes declare no
			// access, and a class whose tree is not on this machine is the
			// ordinary state of an environment built from artifacts that were
			// pruned. An entry only exists to be reported once a declaration
			// has been read.
			continue
		}
		declared := bacon.ReadAccess(store.Doc)
		bacon.SortAccess(declared)
		for _, d := range declared {
			out = append(out, resolveOne(ctx, o, repo, d, reveal))
		}
	}
	// Stacks resolve against the class entries already gathered, because a stack
	// entry replaces the class entry for the same instance and name.
	stacked := stackEntries(ctx, o, out, reveal)
	kept := out[:0]
	for _, e := range out {
		if !e.superseded {
			kept = append(kept, e)
		}
	}
	out = append(kept, stacked...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// stackEntries resolves the access a stack declares for the instances it
// declared, and drops the class entries it supersedes from class (in place).
//
// A stack is a RepoClass, so its declaration is read through the same
// ClassReader; the only thing that differs is that its entries name the instance
// they describe, because a stack does not deploy as an instance of itself.
//
// It supersedes rather than sits beside. A stack that changed an instance's
// configuration is the thing that knows what changed: hmd-app-airflow documents
// the `admin` user its own default_configuration creates, and a stack that
// replaces that users fixture leaves the class's entry describing a login that
// does not exist. Reporting both would show two usernames for one URL and leave
// the reader to guess which, which is the failure this whole mechanism exists to
// avoid.
func stackEntries(ctx context.Context, o Options, class []Entry, reveal bool) []Entry {
	var out []Entry
	for _, st := range o.Stacks {
		if st.Class == "" {
			// Nothing to read, and saying so beats silence: a stack that
			// declares a front door and is ignored looks like a stack that
			// declares none.
			out = append(out, Entry{
				Instance: st.Name, Name: st.Name, Stack: st.Name,
				Problem: "this stack's record predates the class being recorded, so its own " +
					"access declaration cannot be found; re-run `nsctl stack add` to record it",
			})
			continue
		}
		store, err := o.Class(st.Class)
		if err != nil {
			continue
		}
		declared := bacon.ReadAccess(store.Doc)
		bacon.SortAccess(declared)
		for _, d := range declared {
			e, ok := resolveStackOne(ctx, o, st, d, reveal)
			if !ok {
				continue
			}
			// The class entry it replaces, if there is one. Blanked rather than
			// removed so the caller's slice indices stay put; Resolve filters.
			for i := range class {
				if class[i].Instance == e.Instance && class[i].Name == e.Name {
					class[i].superseded = true
				}
			}
			out = append(out, e)
		}
	}
	return out
}

// resolveStackOne resolves one stack entry against the instance it names.
//
// ok is false when the entry is filtered out by --instance. An entry naming an
// instance the environment does not declare is reported, not dropped: a stack
// that names the wrong instance has a defect in it, and the report is where its
// author finds out.
func resolveStackOne(ctx context.Context, o Options, st manifest.StackRecord,
	d bacon.AccessEntry, reveal bool) (Entry, bool) {

	target := d.Instance
	if target == "" {
		// A stack entry that names no instance has nothing to template against:
		// {ingress_host} and a secret name are both derived from one.
		if o.Instance != "" {
			return Entry{}, false
		}
		return Entry{
			Instance: st.Name, Name: d.Name, Stack: st.Name,
			Username: d.Username, Notes: d.Notes,
			Problem: "a stack's access entry must name the instance it describes with `instance`",
		}, true
	}
	if o.Instance != "" && target != o.Instance {
		return Entry{}, false
	}
	for _, repo := range o.Repos {
		if repo.InstanceName == target {
			e := resolveOne(ctx, o, repo, d, reveal)
			e.Stack = st.Name
			return e, true
		}
	}
	return Entry{
		Instance: target, Name: d.Name, Stack: st.Name,
		Username: d.Username, Notes: d.Notes,
		Problem: fmt.Sprintf("this stack declares access for %q, which this environment does not declare", target),
	}, true
}

func resolveOne(ctx context.Context, o Options, repo manifest.Repo, d bacon.AccessEntry, reveal bool) Entry {
	values := placeholders(o, repo)
	e := Entry{
		Instance:  repo.InstanceName,
		RepoClass: repo.RepoClassName,
		Name:      d.Name,
		URL:       bacon.ExpandAccess(d.URL, values),
		Username:  d.Username,
		Notes:     d.Notes,
	}
	if d.Secret == nil {
		return e
	}
	e.Store = d.Secret.Store
	e.Property = d.Secret.Property

	key, err := secretName(o, repo, d, values)
	if err != nil {
		e.Problem = err.Error()
		return e
	}
	e.Key = key

	if !reveal {
		return e
	}
	if o.Get == nil {
		e.Problem = "no secret reader: the control plane's Floci is not reachable"
		return e
	}
	raw, err := o.Get(ctx, e.Store, e.Key)
	if err != nil {
		if errors.Is(err, floci.ErrNoSuchSecret) {
			e.Problem = fmt.Sprintf("%s is not in %s yet; it is written when the instance deploys", e.Key, e.Store)
		} else {
			e.Problem = err.Error()
		}
		return e
	}
	value, err := pick(raw, e.Property)
	if err != nil {
		e.Problem = err.Error()
		return e
	}
	e.Secret = value
	return e
}

// secretName is the declaration's key, or the producer's own published name
// when the entry names an output key instead.
//
// The output takes precedence deliberately: a resources_output entry's
// `secret_name` is the producer stating where it put the secret, and nsctl
// re-deriving that from a template would be nsctl disagreeing with the thing
// that wrote it.
func secretName(o Options, repo manifest.Repo, d bacon.AccessEntry, values map[string]string) (string, error) {
	if d.Secret.Output != "" {
		name, err := outputValue(o.OutputDir, repo.InstanceName, d.Secret.Output)
		if err != nil {
			return "", err
		}
		return name, nil
	}
	key := bacon.ExpandAccess(d.Secret.Key, values)
	if strings.Contains(key, "{") {
		return "", fmt.Errorf("the secret name still has unresolved placeholders: %s", key)
	}
	return key, nil
}

func placeholders(o Options, repo manifest.Repo) map[string]string {
	return map[string]string{
		"instance_name":   repo.InstanceName,
		"repo_class_name": repo.RepoClassName,
		"deployment_id":   o.DeploymentID,
		"environment":     o.Environment,
		// The hostname this instance's user interface is reached at. It carries
		// the environment as its own label, so two environments deploying the
		// same chart resolve to different places (NERD025 SPEC005); the default
		// environment keeps the short form.
		"ingress_host": router.IngressHostFor(repo.InstanceName, o.Environment),
	}
}

// pick takes a property out of a JSON secret, or the whole value when the entry
// named no property.
func pick(raw, property string) (string, error) {
	if property == "" {
		return raw, nil
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("the secret is not a JSON object, so it has no %q property", property)
	}
	v, ok := doc[property]
	if !ok {
		keys := make([]string, 0, len(doc))
		for k := range doc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("the secret has no %q property; it has %s", property, strings.Join(keys, ", "))
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("the secret's %q property is not a string", property)
	}
	return s, nil
}

// outputValue reads one key out of an instance's recorded resource outputs.
//
// The file is the list submit_resources was given: one document per Resource,
// each with its own `output` object. The key is looked for in every Resource
// rather than in a named one, because a RepoClass that produces one Resource --
// which is the common case -- should not have to name it again here.
func outputValue(dir, instance, key string) (string, error) {
	if dir == "" || instance == "" {
		return "", fmt.Errorf("no recorded resource output for %s, so %q cannot be read", instance, key)
	}
	data, err := os.ReadFile(filepath.Join(dir, instance+".json"))
	if err != nil {
		return "", fmt.Errorf("%s has produced no resource output yet, so %q cannot be read", instance, key)
	}
	var resources []map[string]any
	if err := json.Unmarshal(data, &resources); err != nil {
		return "", fmt.Errorf("%s's recorded resource output is unreadable", instance)
	}
	for _, res := range resources {
		output, ok := res["output"].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := output[key]; ok {
			if s, ok := v.(string); ok {
				return s, nil
			}
			return "", fmt.Errorf("%s's resource output %q is not a string", instance, key)
		}
	}
	return "", fmt.Errorf("%s's resource output has no %q", instance, key)
}

// Any reports whether anything declares access at all. Used to decide whether a
// deploy summary should point at `env credentials`: a pointer to an empty table
// is noise, and a missing pointer to a full one is worse.
//
// Stacks count. A stack declares access for the instances it declared, so an
// environment whose only declaration is its stack's would otherwise deploy three
// user interfaces and say nothing about how to sign in to them.
func Any(repos []manifest.Repo, stacks []manifest.StackRecord, class ClassReader) bool {
	classes := make([]string, 0, len(repos)+len(stacks))
	for _, repo := range repos {
		classes = append(classes, repo.RepoClassName)
	}
	for _, st := range stacks {
		classes = append(classes, st.Class)
	}
	for _, c := range classes {
		if c == "" {
			continue
		}
		store, err := class(c)
		if err != nil {
			continue
		}
		if len(bacon.ReadAccess(store.Doc)) > 0 {
			return true
		}
	}
	return false
}
