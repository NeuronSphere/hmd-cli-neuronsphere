package librarian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// ErrNoSuchRepo means the librarian has never heard of a repo class. It is kept
// apart from "no version satisfies your specifier" because the two have nothing
// in common: one is a name to correct or a repo that has never published, the
// other is a range to widen.
var ErrNoSuchRepo = errors.New("no such repo in this librarian")

// RepoEntity is one row of hmd_lang_artifact_librarian.repo: the repo class's
// name, and the identifier its content items are related to.
type RepoEntity struct {
	Identifier string `json:"identifier"`
	RepoName   string `json:"repo_name"`
}

// ContentItem is a content item as get_by_nid returns it. Only the path is read:
// the version and the item type are both *in* the path, so neither is ever
// fetched as an entity.
type ContentItem struct {
	Nid  string `json:"nid"`
	Path string `json:"content_item_path"`
}

// nidChunk is how many ids go in one get_by_nid request.
//
// Measured: 18 versions took 4.8 s, 96 took 8.2 s and 392 took 29.3 s in a
// single request -- close enough to a Lambda timeout that a repo class with a
// longer history would simply fail. The chunking lives here rather than in the
// caller so that a second caller does not have to rediscover the limit.
const nidChunk = 100

// Repos lists every repo the librarian knows, and is memoised for the life of
// the client.
//
// The body is an empty object, which is what an unfiltered ms-base search takes.
// A *filtered* body answers 500, so every caller filters client side -- which is
// affordable precisely because one request brings back all of them: 217 rows in
// 1.3 s, measured 2026-09-15.
func (c *Client) Repos(ctx context.Context) ([]RepoEntity, error) {
	c.reposOnce.Lock()
	defer c.reposOnce.Unlock()
	if c.repos != nil {
		return c.repos, nil
	}
	var out []RepoEntity
	if err := c.search(ctx, "hmd_lang_artifact_librarian.repo", &out); err != nil {
		return nil, err
	}
	c.repos = out
	return out, nil
}

// RepoIdentifier resolves a repo class name to the identifier its content items
// hang off, or ErrNoSuchRepo.
func (c *Client) RepoIdentifier(ctx context.Context, repoName string) (string, error) {
	repos, err := c.Repos(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range repos {
		if r.RepoName == repoName {
			return r.Identifier, nil
		}
	}
	return "", fmt.Errorf("%s: %w", repoName, ErrNoSuchRepo)
}

// ContentItemIDs lists the ids of the content items belonging to a repo.
//
// The relationship is content_item_has_repo, read towards the repo, so each edge
// names the content item in ref_from.
//
// It is emphatically *not* repo_has_repo_version, which is the relationship an
// implementer reaches for first. That one is declared in the language pack and
// never populated: every repo answers with zero edges, so an implementation
// built on it looks like a librarian with nothing published rather than like a
// bug. The content-path parser writes content_item_has_repo and
// content_item_has_repo_version instead.
func (c *Client) ContentItemIDs(ctx context.Context, repoIdentifier string) ([]string, error) {
	path := "/api/hmd_lang_artifact_librarian.content_item_has_repo/to/" + repoIdentifier
	payload, err := c.request(ctx, http.MethodGet, path, nil, "content items of "+repoIdentifier)
	if err != nil {
		return nil, err
	}
	var edges []struct {
		RefFrom string `json:"ref_from"`
	}
	if err := json.Unmarshal(payload, &edges); err != nil {
		return nil, fmt.Errorf("content items of %s: decoding the response: %w", repoIdentifier, err)
	}
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		if e.RefFrom != "" {
			ids = append(ids, e.RefFrom)
		}
	}
	// Sorted so that two enumerations of an unchanged repo produce the same
	// order, and so the chunk boundaries do not move under a retry.
	sort.Strings(ids)
	return ids, nil
}

// ContentItemsByNID reads content items by id, in chunks.
//
// progress may be nil. It is called after each chunk with how many ids have been
// read and how many there are, because this is the slow leg and 29 seconds of
// silence reads as a hang.
//
// Note what this costs: get_by_nid mints a presigned download_url for every item
// it returns, so enumerating versions this way pays for URL signing purely to
// read paths. That is acceptable in a command a developer runs occasionally and
// is not acceptable on any path a deploy takes.
func (c *Client) ContentItemsByNID(ctx context.Context, nids []string,
	progress func(done, total int)) ([]ContentItem, error) {

	items := make([]ContentItem, 0, len(nids))
	for start := 0; start < len(nids); start += nidChunk {
		end := start + nidChunk
		if end > len(nids) {
			end = len(nids)
		}
		var chunk []ContentItem
		if err := c.operation(ctx, "get_by_nid", map[string]any{"nids": nids[start:end]}, &chunk); err != nil {
			return nil, err
		}
		items = append(items, chunk...)
		if progress != nil {
			progress(end, len(nids))
		}
	}
	return items, nil
}

// search is an unfiltered CRUD search: POST /api/<entity> with an empty object.
//
// There is no filtered form here on purpose. Any filtered body answers 500,
// while the unfiltered one succeeds -- as do /apiop/search with like, contains,
// startswith or begins_with on content_item_path, and listing every
// hmd_lang_librarian.content_item answers 502 because the response is far too
// large. All four look correct and three of them fail silently, so they are
// recorded here rather than rediscovered.
func (c *Client) search(ctx context.Context, entityType string, out any) error {
	payload, err := c.request(ctx, http.MethodPost, "/api/"+entityType,
		map[string]any{}, "search "+entityType)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("search %s: decoding the response: %w", entityType, err)
	}
	return nil
}

// reposCache is the memoised repo list and its lock, embedded in Client.
type reposCache struct {
	reposOnce sync.Mutex
	repos     []RepoEntity
}

// ParseContentPath reads a content path back into the Spec that would produce
// it: librarian.Spec's grammar read backwards.
//
// The parse is verified by re-rendering, so there is exactly one statement of
// the grammar in this package rather than a generating half and a parsing half
// that can drift. BACON's pre_build_artifacts and the librarian's content paths
// are one vocabulary, which is the rule NERD010 SPEC002 follows in the other
// direction.
//
// A path that does not match is an error rather than a panic, and callers
// enumerating a whole repo skip those: a librarian holds content items that are
// not build artifacts, and they are not this mechanism's business.
func ParseContentPath(path string) (Spec, error) {
	rest, ok := strings.CutPrefix(path, "repository:/")
	if !ok {
		return Spec{}, fmt.Errorf("content path %q: expected a repository:/ path", path)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return Spec{}, fmt.Errorf(
			"content path %q: expected repository:/<repo>/<version>/<repo>_<version>_<item_type>.zip", path)
	}
	s := Spec{Name: parts[0], Version: parts[1]}
	itemType, ok := strings.CutPrefix(parts[2], s.Name+"_"+s.Version+"_")
	if !ok {
		return Spec{}, fmt.Errorf("content path %q: the file name does not name the repo and version", path)
	}
	itemType, ok = strings.CutSuffix(itemType, ".zip")
	if !ok {
		return Spec{}, fmt.Errorf("content path %q: not a .zip", path)
	}
	s.ItemType = itemType
	if s.Name == "" || s.Version == "" || s.ItemType == "" || s.ContentPath() != path {
		return Spec{}, fmt.Errorf("content path %q: does not round-trip through the grammar", path)
	}
	return s, nil
}
