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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Name < out[j].Name
	})
	return out
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
		// The Ingress hostname hmd-cli-helm gives this instance's chart. Derived
		// from the instance name alone, because the slug in alb.hostname is the
		// literal "local" in every environment.
		"ingress_host": router.IngressHostFor(repo.InstanceName),
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

// Any reports whether any instance declares access at all. Used to decide
// whether a deploy summary should point at `env credentials`: a pointer to an
// empty table is noise.
func Any(repos []manifest.Repo, class ClassReader) bool {
	for _, repo := range repos {
		if repo.RepoClassName == "" {
			continue
		}
		store, err := class(repo.RepoClassName)
		if err != nil {
			continue
		}
		if len(bacon.ReadAccess(store.Doc)) > 0 {
			return true
		}
	}
	return false
}
