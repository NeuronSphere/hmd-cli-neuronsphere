package bom

import (
	"context"
)

// SeedBaseResourceDefinitions upserts the abstract resource supertypes bundled
// with ms-deployment (NERD0004).
//
// It has to run before anything declares what it produces. A repo's own
// definition parents onto one of these -- hmd-postgres-rds's
// aws.neuronsphere.io/aurora-postgres onto database.neuronsphere.io/postgres --
// and both the upsert and the declaration fail if the parent is not in the
// graph.
//
// Idempotent, and cheap enough to run on every seed. That it was missing only
// ever mattered on a graph that had never seen these types, which is why it
// went unnoticed: every graph nsctl was developed against had been seeded by
// the Python CLI at some point. The first genuinely cold start after a purge
// failed with `declare_produces_resource_definition: HTTP 400: ...
// aurora-postgres ... not found`.
func (s *Seeder) SeedBaseResourceDefinitions(ctx context.Context) error {
	return s.Client.APIOpTolerateExists(ctx, "seed_base_resource_definitions", map[string]any{})
}

// UpsertResourceDefinitions registers the resource types a RepoClass declares
// in its meta-data/resources/*.yaml.
//
// The repo's document is forwarded as it stands. nsctl carries no table of
// resource types, for the same reason it carries none of who produces what:
// the repo already states it, in the file the cloud deploy path reads, and a
// second copy in another language is the one that goes stale.
//
// Best-effort, deliberately. A definition that fails to upsert is a warning
// rather than a failed start, because DeclareProduces is the operation that
// actually needs it and fails loudly and specifically when it is missing.
func (s *Seeder) UpsertResourceDefinitions(ctx context.Context, repoClass string) error {
	declarations, err := s.Versions.Produces(repoClass)
	if err != nil {
		return err
	}
	for _, d := range declarations {
		if d.Namespace == "" || d.Name == "" {
			continue
		}
		payload := map[string]any{
			"resource_namespace":       d.Namespace,
			"resource_definition_name": d.Name,
			"version":                  d.Version,
		}
		if d.Description != "" {
			payload["description"] = d.Description
		}
		if d.ResourceMetadata != nil {
			payload["resource_metadata"] = d.ResourceMetadata
		}
		if d.OutputSchema != nil {
			payload["output_schema"] = d.OutputSchema
		}
		if d.Parent != nil && d.Parent.Namespace != "" {
			payload["parent"] = map[string]any{
				"resource_namespace":       d.Parent.Namespace,
				"resource_definition_name": d.Parent.Name,
				"version":                  d.Parent.Version,
			}
		}
		if err := s.Client.APIOpTolerateExists(ctx, "upsert_resource_definition", payload); err != nil {
			s.warn("could not register the resource type %s/%s declared by %s: %v",
				d.Namespace, d.Name, repoClass, err)
		}
	}
	return nil
}
