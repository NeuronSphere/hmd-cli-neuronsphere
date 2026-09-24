package bacon

import (
	"strings"
	"testing"
)

func supersetEntry() AccessEntry {
	return AccessEntry{
		Name:     "superset",
		URL:      "http://{ingress_host}/",
		Username: "admin",
		Notes:    "Self-registration is off.",
		Secret: &AccessSecret{
			Store:    StoreSecretsManager,
			Key:      "{instance_name}-{deployment_id}-{environment}-admin-credentials",
			Property: "password",
		},
	}
}

// AddAccess is keyed on name and idempotent, and every other key keeps its
// place -- the property that makes re-running a verb a no-op in the diff.
// NERD023 SPEC004.
func TestAddAndRemoveAccess(t *testing.T) {
	t.Parallel()

	doc := base()
	if _, err := AddAccess(doc, supersetEntry()); err != nil {
		t.Fatal(err)
	}
	if _, err := AddAccess(doc, AccessEntry{
		Name:   "api",
		URL:    "http://{ingress_host}/api/",
		Secret: &AccessSecret{Store: StoreParameterStore, Output: "secret_name"},
	}); err != nil {
		t.Fatal(err)
	}

	got := encoded(t, doc)
	for _, want := range []string{
		`"access"`,
		`"name": "superset"`,
		`"username": "admin"`,
		`"store": "secrets-manager"`,
		`"property": "password"`,
		`"output": "secret_name"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if keys := doc.Keys(); keys[0] != "name" || keys[1] != "description" || keys[2] != "build" {
		t.Errorf("existing keys moved: %v", keys)
	}

	// Replaced, not duplicated, and wholesale: the old property does not survive
	// into an entry nobody wrote that way.
	replaced := supersetEntry()
	replaced.Username = "root"
	replaced.Secret = &AccessSecret{Store: StoreParameterStore, Key: "k"}
	if _, err := AddAccess(doc, replaced); err != nil {
		t.Fatal(err)
	}
	entries := ReadAccess(doc)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries after replacing one, got %d", len(entries))
	}
	var superset AccessEntry
	for _, e := range entries {
		if e.Name == "superset" {
			superset = e
		}
	}
	if superset.Username != "root" {
		t.Errorf("username = %q, want the replacement's", superset.Username)
	}
	if superset.Secret.Property != "" {
		t.Errorf("property %q survived a wholesale replacement", superset.Secret.Property)
	}

	if _, ok := RemoveAccess(doc, "nope"); ok {
		t.Error("removing an entry that is not there reported success")
	}
	if _, ok := RemoveAccess(doc, "api"); !ok {
		t.Error("removing a declared entry reported failure")
	}
	// The last removal takes the key with it: a class that declares no front
	// door should not carry an empty list saying so.
	if _, ok := RemoveAccess(doc, "superset"); !ok {
		t.Error("removing the last entry reported failure")
	}
	if _, present := doc.Get("access"); present {
		t.Errorf("the access key survived its last entry:\n%s", encoded(t, doc))
	}
}

// An entry with nothing to look up, or a store nsctl cannot read, is refused at
// authoring time rather than written and discovered at resolution time.
func TestAddAccessRefusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		entry AccessEntry
		want  string
	}{
		{"no name", AccessEntry{URL: "http://x/"}, "needs a name"},
		{"no url", AccessEntry{Name: "a"}, "needs a url"},
		{"unknown placeholder in url",
			AccessEntry{Name: "a", URL: "http://{nope}/"}, `unknown placeholder "nope"`},
		{"unknown placeholder in key",
			AccessEntry{Name: "a", URL: "http://x/", Secret: &AccessSecret{Store: StoreSecretsManager, Key: "{nope}"}},
			`unknown placeholder "nope"`},
		{"unknown store",
			AccessEntry{Name: "a", URL: "http://x/", Secret: &AccessSecret{Store: "vault", Key: "k"}},
			"secret.store must be"},
		{"secret with nothing to look up",
			AccessEntry{Name: "a", URL: "http://x/", Secret: &AccessSecret{Store: StoreSecretsManager}},
			"names either a key or the resource output"},
	}
	for _, c := range cases {
		doc := base()
		_, err := AddAccess(doc, c.entry)
		if err == nil {
			t.Errorf("%s: want a refusal, got none", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
		if _, present := doc.Get("access"); present {
			t.Errorf("%s: a refused entry was written anyway", c.name)
		}
	}
}

// A placeholder with no value is left as written rather than emptied, so an
// unresolved URL reads as a template that did not resolve instead of a
// plausible wrong one.
func TestExpandAccess(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"instance_name": "superset",
		"deployment_id": "aaaa",
		"environment":   "dev",
		"ingress_host":  "superset.local.neuronsphere.io",
	}
	if got, want := ExpandAccess("http://{ingress_host}/", values), "http://superset.local.neuronsphere.io/"; got != want {
		t.Errorf("ExpandAccess = %q, want %q", got, want)
	}
	if got, want := ExpandAccess("{instance_name}-{deployment_id}-{environment}-admin-credentials", values),
		"superset-aaaa-dev-admin-credentials"; got != want {
		t.Errorf("ExpandAccess = %q, want %q", got, want)
	}
	// repo_class_name is known but unset here.
	if got := ExpandAccess("{repo_class_name}/x", values); got != "{repo_class_name}/x" {
		t.Errorf("an unset placeholder should stand, got %q", got)
	}
	if got := ExpandAccess("no placeholders", values); got != "no placeholders" {
		t.Errorf("ExpandAccess = %q", got)
	}
}

// NERD023 SPEC005: a manifest names where a credential lives and never holds
// one, and validate says so rather than ignoring the key.
func TestValidateRefusesALiteralCredential(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"password", "token", "api_key", "Password"} {
		doc := base()
		if _, err := AddAccess(doc, supersetEntry()); err != nil {
			t.Fatal(err)
		}
		list, _ := doc.Array("access")
		list[0].(*Object).Set(key, "hunter2")

		v := &validator{doc: doc}
		v.access()
		var found bool
		for _, f := range v.findings {
			if f.Severity == Error && strings.Contains(f.Message, "literal credential") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want an error naming a literal credential, got %v", key, v.findings)
		}
	}

	// The clean entry is clean: a check that flagged everything would be no
	// check at all.
	doc := base()
	if _, err := AddAccess(doc, supersetEntry()); err != nil {
		t.Fatal(err)
	}
	v := &validator{doc: doc}
	v.access()
	if len(v.findings) != 0 {
		t.Errorf("a well-formed declaration should be silent, got %v", v.findings)
	}
}

// Shape errors a hand-edited manifest can carry, which the authoring verbs
// cannot produce and validate therefore has to catch.
func TestValidateAccessShape(t *testing.T) {
	t.Parallel()

	doc := base()
	doc.Set("access", "not a list")
	v := &validator{doc: doc}
	v.access()
	if len(v.findings) != 1 || !strings.Contains(v.findings[0].Message, "must be a list") {
		t.Errorf("want one list error, got %v", v.findings)
	}

	dup := base()
	a, b := NewObject(), NewObject()
	a.Set("name", "x")
	a.Set("url", "http://x/")
	b.Set("name", "x")
	b.Set("url", "http://y/")
	b.Set("colour", "blue")
	dup.Set("access", []any{a, b})
	v = &validator{doc: dup}
	v.access()
	var dupErr, indexed bool
	for _, f := range v.findings {
		if strings.Contains(f.Message, "duplicates an earlier entry") {
			dupErr = true
		}
		// The duplicate keeps its index in the path: labelling both entries
		// "access.x" would leave the reader unable to tell them apart.
		if f.Path == "access[1].colour" {
			indexed = true
		}
	}
	if !dupErr {
		t.Errorf("want a duplicate-name error, got %v", v.findings)
	}
	if !indexed {
		t.Errorf("a duplicate's later findings should keep its index, got %v", v.findings)
	}
}
