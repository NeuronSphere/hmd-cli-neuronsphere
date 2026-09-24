package bacon

import "strings"

// Describe is the normalized summary `hmd describe` prints
// (hmd_cli_manifest.describe), as an ordered document: name, description,
// project_type, discovery, resources, dependencies. Two additions the Python
// omits and an agent reading a foreign repo class needs: each dependency
// carries its `resource` block, and a `test` section is reported when
// present. Both are additive; the Python's output is a strict subset.
func Describe(doc *Object) *Object {
	out := NewObject()
	name, _ := doc.String("name")
	description, _ := doc.String("description")
	out.Set("name", name)
	out.Set("description", description)

	if pt, ok := doc.Object("project_type"); ok && pt.Len() > 0 {
		out.Set("project_type", pt)
	}

	if discovery, ok := doc.Object("discovery"); ok && discovery.Len() > 0 {
		d := NewObject()
		summary, _ := discovery.String("summary")
		if summary == "" {
			summary = description
		}
		if summary != "" {
			d.Set("summary", summary)
		}
		for _, key := range []string{"entry_points", "capabilities", "related_docs"} {
			if list, ok := discovery.Array(key); ok && len(list) > 0 {
				d.Set(key, list)
			}
		}
		if d.Len() > 0 {
			out.Set("discovery", d)
		}
	}

	deploy, _ := doc.Object("deploy")

	if resources, ok := deploy.Object("resources"); ok && resources.Len() > 0 {
		var list []any
		for _, resName := range resources.Keys() {
			res, ok := resources.Object(resName)
			if !ok {
				continue
			}
			entry := NewObject()
			entry.Set("name", resName)
			for _, key := range []string{"resource_namespace", "resource_definition_name", "version", "description"} {
				if v, ok := res.Get(key); ok && v != nil {
					entry.Set(key, v)
				}
			}
			list = append(list, entry)
		}
		if len(list) > 0 {
			out.Set("resources", list)
		}
	}

	if deps, ok := deploy.Object("dependencies"); ok && deps.Len() > 0 {
		var list []any
		for _, depName := range deps.Keys() {
			dep, ok := deps.Object(depName)
			if !ok {
				continue
			}
			entry := NewObject()
			entry.Set("name", depName)
			if v, ok := dep.Get("repo_class_name"); ok && v != nil {
				entry.Set("repo_class_name", v)
			}
			if v, ok := dep.Get("required"); ok && v != nil {
				entry.Set("required", requiredIsTrue(v))
			}
			if v, ok := dep.Get("version_spec"); ok && v != nil {
				entry.Set("version_spec", v)
			}
			if res, ok := dep.Object("resource"); ok && res.Len() > 0 {
				entry.Set("resource", res)
			}
			list = append(list, entry)
		}
		if len(list) > 0 {
			out.Set("dependencies", list)
		}
	}

	if test, ok := doc.Object("test"); ok && test.Len() > 0 {
		out.Set("test", test)
	}

	// The access declaration verbatim (NERD023 SPEC004). Reported as written
	// rather than resolved: describe reads a repository, where an instance name
	// and an environment do not yet exist, so the placeholders are the honest
	// answer. `nsctl env credentials` is where they are filled in.
	if list, ok := doc.Array("access"); ok && len(list) > 0 {
		out.Set("access", list)
	}
	return out
}

// requiredIsTrue is the Python's `str(v).lower() == "true"`: the string
// "true", the boolean true, and nothing else.
func requiredIsTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.ToLower(t) == "true"
	}
	return false
}
