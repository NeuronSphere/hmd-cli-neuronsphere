package msdeploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// repoClassVersionBlobs are repo_class_version's mapping and collection
// attributes: served decoded by find_repo_class_versions, but transmitted to
// the CRUD PUT base64-encoded (see EncodeCollection).
var repoClassVersionBlobs = []string{"default_configuration", "discovery", "toolset", "deploy_commands"}

// SyncRepoClassVersion brings an already-registered version's catalog
// metadata in line with fields, and reports whether it had to write.
//
// add_repo_class_version refuses a version the catalog already has, and there
// is no operation to update one, so without this an edited
// default_configuration in a working tree never reached the catalog --
// get_deployment_config kept merging the first one registered into every
// deploy (NERD034 SPEC001). fields holds only blob attributes the caller read
// from the tree; any it leaves out keep their stored value.
//
// The write is the ms-base CRUD PUT with the row's identifier, which updates
// rather than creates. It replaces the whole row, so every attribute the row
// already had is carried over.
func (c *Client) SyncRepoClassVersion(ctx context.Context, repoClass, version string, fields map[string]any) (bool, error) {
	row, err := c.findRepoClassVersion(ctx, repoClass, version)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, fmt.Errorf("%s has no registered version %s to update", repoClass, version)
	}

	changed := false
	for key, want := range fields {
		if !sameJSON(decodeBlob(row[key]), want) {
			changed = true
			break
		}
	}
	if !changed {
		return false, nil
	}

	put := map[string]any{"identifier": row["identifier"], "version": row["version"]}
	for _, key := range repoClassVersionBlobs {
		value, ok := fields[key]
		if !ok {
			value = decodeBlob(row[key])
		}
		if value == nil {
			continue
		}
		encoded, err := EncodeCollection(value)
		if err != nil {
			return false, err
		}
		put[key] = encoded
	}
	if _, err := c.PutEntity(ctx, EntityRepoClassVersion, put); err != nil {
		return false, err
	}
	return true, nil
}

// findRepoClassVersion is the catalog row for exactly one version, or nil.
//
// One page, filtered, without the per-version dependency walk the unpaged
// call does. q is a substring match, so the exact version is picked out here.
// A service that predates pagination ignores the query and answers with the
// bare list, which is read the same way.
func (c *Client) findRepoClassVersion(ctx context.Context, repoClass, version string) (map[string]any, error) {
	query := url.Values{"q": {version}, "include_deps": {"false"}, "limit": {"500"}}
	body, err := c.APIOpRaw(ctx, "find_repo_class_versions/"+url.PathEscape(repoClass)+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("find_repo_class_versions: decoding the response: %w", err)
		}
		rows = page.Items
	}
	for _, row := range rows {
		if stringField(row, "version") == version {
			return row, nil
		}
	}
	return nil, nil
}

// decodeBlob reads a mapping or collection attribute however it was served:
// native JSON, or the base64-encoded JSON string ms-base stores.
func decodeBlob(value any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return value
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return value
	}
	return out
}

// sameJSON compares two values by their canonical JSON, so key order and
// int-versus-float are not differences.
func sameJSON(a, b any) bool {
	ca, errA := canonicalJSON(a)
	cb, errB := canonicalJSON(b)
	return errA == nil && errB == nil && bytes.Equal(ca, cb)
}

func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}
