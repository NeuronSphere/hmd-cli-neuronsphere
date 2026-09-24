package bacon

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Edits and shared vocabulary for the top-level `access` declaration: how a
// deployed RepoClass is reached, who logs in, and *where* the credential lives
// (NERD023 SPEC004).
//
// It is top-level rather than inside `local` because a front door is not a
// local-development fixture -- the same declaration describes the cloud
// deployment with different values substituted -- and it is a list because one
// RepoClass can expose more than one way in: a UI and the API behind it want
// different URLs and, often, different credentials.

// Secret stores an access entry may name. The choice is explicit because it
// cannot be inferred: hmd_lib_secrets_backend.create_secret() writes SSM
// Parameter Store whatever its name suggests, while Superset's chart reads its
// admin password through the Secrets Manager ClusterSecretStore. The emulator
// keeps the two namespaces apart, so a reader that guesses reports "does not
// exist" against a secret that is present in the other one.
const (
	StoreSecretsManager = "secrets-manager"
	StoreParameterStore = "parameter-store"
)

var accessStores = map[string]bool{
	StoreSecretsManager: true,
	StoreParameterStore: true,
}

// AccessPlaceholders are the substitutions an entry's url, secret.key and
// secret.property may use. Every one is a fact nsctl already holds about a
// deployed instance, which is what keeps the declaration portable: the same
// manifest resolves in any environment, under any instance name.
//
// An unknown placeholder is a validation error rather than an empty
// substitution, because the failure it produces otherwise is a URL with a hole
// in it or a secret lookup against a truncated name.
var AccessPlaceholders = []string{
	"instance_name",
	"repo_class_name",
	"deployment_id",
	"environment",
	"ingress_host",
}

// placeholderPattern matches a {name} substitution. Deliberately not
// text/template: the declaration is data an author writes by hand and a
// template language in it would invite logic, which is the thing a declaration
// exists not to have.
var placeholderPattern = regexp.MustCompile(`\{([^{}]*)\}`)

// AccessSecret names where one credential lives. It never holds one.
type AccessSecret struct {
	Store    string
	Key      string
	Property string
	// Output names a key of the producing RepoClass's emitted resource output
	// to read the secret's name from, instead of restating a template here.
	// Preferred where the producer publishes one -- resources_output entries
	// already carry `secret_name` -- because nsctl should not re-derive a name
	// its producer published.
	Output string
}

// AccessEntry is one way in to the deployed RepoClass.
type AccessEntry struct {
	Name     string
	URL      string
	Username string
	Notes    string
	Secret   *AccessSecret
}

// AddAccess adds or replaces the access entry with the name.
//
// Keyed and idempotent, like every other add-* verb: re-running it is a no-op
// in the diff rather than a second entry. Replacement is wholesale -- an entry
// is one statement about one door, and merging half of an old one into a new
// one would produce a declaration nobody wrote.
func AddAccess(doc *Object, e AccessEntry) (string, error) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return "", fmt.Errorf("an access entry needs a name: what this way in is called")
	}
	if strings.TrimSpace(e.URL) == "" {
		return "", fmt.Errorf("access entry %q needs a url: where it is reached", name)
	}
	for _, field := range []struct{ label, value string }{
		{"url", e.URL},
		{"secret.key", secretField(e.Secret, func(s *AccessSecret) string { return s.Key })},
		{"secret.property", secretField(e.Secret, func(s *AccessSecret) string { return s.Property })},
	} {
		if bad := UnknownPlaceholders(field.value); len(bad) > 0 {
			return "", fmt.Errorf("access entry %q: %s uses unknown placeholder %s; known are %s",
				name, field.label, strings.Join(quoteAll(bad), ", "), strings.Join(AccessPlaceholders, ", "))
		}
	}

	entry := NewObject()
	entry.Set("name", name)
	entry.Set("url", e.URL)
	if e.Username != "" {
		entry.Set("username", e.Username)
	}
	if e.Secret != nil {
		sec, err := accessSecretObject(name, e.Secret)
		if err != nil {
			return "", err
		}
		entry.Set("secret", sec)
	}
	if e.Notes != "" {
		entry.Set("notes", e.Notes)
	}

	existing, _ := doc.Array("access")
	kept := make([]any, 0, len(existing)+1)
	for _, item := range existing {
		if obj, ok := item.(*Object); ok {
			if v, _ := obj.String("name"); v == name {
				continue
			}
		}
		kept = append(kept, item)
	}
	doc.Set("access", append(kept, entry))
	return "access", nil
}

// RemoveAccess drops the entry with the name; ok reports whether there was one.
// The `access` key itself is removed when the last entry goes, so a repository
// that no longer declares a front door does not carry an empty list saying so.
func RemoveAccess(doc *Object, name string) (key string, ok bool) {
	existing, _ := doc.Array("access")
	kept := make([]any, 0, len(existing))
	for _, item := range existing {
		if obj, isObj := item.(*Object); isObj {
			if v, _ := obj.String("name"); v == name {
				ok = true
				continue
			}
		}
		kept = append(kept, item)
	}
	if !ok {
		return "access", false
	}
	if len(kept) == 0 {
		doc.Delete("access")
		return "access", true
	}
	doc.Set("access", kept)
	return "access", true
}

func accessSecretObject(entryName string, s *AccessSecret) (*Object, error) {
	if !accessStores[s.Store] {
		return nil, fmt.Errorf("access entry %q: secret.store must be %s or %s; got %q",
			entryName, StoreSecretsManager, StoreParameterStore, s.Store)
	}
	if strings.TrimSpace(s.Key) == "" && strings.TrimSpace(s.Output) == "" {
		return nil, fmt.Errorf("access entry %q: a secret names either a key or the resource output to read its name from",
			entryName)
	}
	obj := NewObject()
	obj.Set("store", s.Store)
	if s.Output != "" {
		obj.Set("output", s.Output)
	}
	if s.Key != "" {
		obj.Set("key", s.Key)
	}
	if s.Property != "" {
		obj.Set("property", s.Property)
	}
	return obj, nil
}

// UnknownPlaceholders lists the {name} substitutions in a template that are not
// in AccessPlaceholders, in first-seen order and without repeats.
func UnknownPlaceholders(template string) []string {
	var bad []string
	seen := map[string]bool{}
	known := map[string]bool{}
	for _, p := range AccessPlaceholders {
		known[p] = true
	}
	for _, m := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		name := m[1]
		if known[name] || seen[name] {
			continue
		}
		seen[name] = true
		bad = append(bad, name)
	}
	return bad
}

// ExpandAccess substitutes the known placeholders in a template. A placeholder
// with no value is left as it was written rather than emptied, so an unresolved
// URL reads as a template that did not resolve instead of a plausible wrong one.
func ExpandAccess(template string, values map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(template, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := values[name]; ok && v != "" {
			return v
		}
		return m
	})
}

// ReadAccess decodes the declaration. Entries keep file order; a malformed
// entry is skipped here and reported by Validate, which is the half of the
// contract that exists to say so.
func ReadAccess(doc *Object) []AccessEntry {
	raw, _ := doc.Array("access")
	out := make([]AccessEntry, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(*Object)
		if !ok {
			continue
		}
		name, _ := obj.String("name")
		if name == "" {
			continue
		}
		e := AccessEntry{Name: name}
		e.URL, _ = obj.String("url")
		e.Username, _ = obj.String("username")
		e.Notes, _ = obj.String("notes")
		if sec, ok := obj.Object("secret"); ok {
			s := &AccessSecret{}
			s.Store, _ = sec.String("store")
			s.Key, _ = sec.String("key")
			s.Property, _ = sec.String("property")
			s.Output, _ = sec.String("output")
			e.Secret = s
		}
		out = append(out, e)
	}
	return out
}

// SortAccess orders entries by name, for output that is the same bytes twice.
func SortAccess(entries []AccessEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

func secretField(s *AccessSecret, pick func(*AccessSecret) string) string {
	if s == nil {
		return ""
	}
	return pick(s)
}

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%q", v))
	}
	return out
}
