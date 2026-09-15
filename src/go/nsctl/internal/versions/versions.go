// Package versions enumerates what a repo class has published to an Artifact
// Librarian, and caches the answer.
//
// This is the half of NERD011 that touches a network. The arithmetic half --
// which versions a specifier admits, and which of them is highest -- is
// internal/versionspec, and the split is what lets manifest validation, status
// reporting and error messages evaluate a specifier without anyone wondering
// whether they just made an HTTP request. NERD011 SPEC001 and SPEC004.
//
// # Three requests, and four routes that do not work
//
// A repo class's published versions come from an unfiltered repo search, the
// content_item_has_repo edges of the matching repo, and a chunked get_by_nid
// over those ids -- see internal/librarian's search surface, which records the
// dead ends in detail. The version and the item type are read out of the content
// path rather than fetched as entities, because the librarian's own path grammar
// carries both.
//
// # Resolution never runs during a deploy
//
// This package is reached from the artifact verbs -- the commands a developer
// types when choosing a version -- and from nothing that deploys. NERD005
// SPEC002's resolution tiers read the disk and never consult a librarian, and
// an environment apply is offline by default. A version resolution on the deploy
// path would make a deploy's result depend on the day it ran, which is the
// precise thing a lock exists to prevent.
//
// # The cache is cache
//
// The enumerations live under $HMD_HOME/.cache/neuronsphere/versions, beside the
// unpacked artifacts internal/artifact keeps and with the same standing: wholly
// reconstructible, so deleting one costs a re-query and nothing else. Purging an
// environment removes its state directory and never reaches .cache/neuronsphere,
// so there is no purge work here.
//
// A cache entry is never silently refreshed. The command the user typed queries;
// everything else reads what is there. An implicit refresh would reintroduce by
// the back door exactly the non-determinism the offline rule above removes --
// which is why each entry records when it was taken, so a reader can tell "this
// is the newest version" from "this was the newest version when somebody last
// asked".
package versions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/atomicfile"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/librarian"
	"github.com/neuronsphere/hmd-cli-neuronsphere/internal/versionspec"
)

// ErrNoCache means nothing has ever enumerated this repo class on this machine.
// Distinguished from an empty enumeration, which is a repo class that exists and
// has published nothing.
var ErrNoCache = errors.New("no cached enumeration")

// Item is one published artifact: a version, and which kind of artifact it is.
//
// The item type is kept even though nothing yet resolves against anything but
// `build`. It costs nothing -- SPEC001 reads it out of the path either way --
// and the alternative is discarding it here and re-enumerating the day something
// wants a schema version.
type Item struct {
	Version  string `json:"version"`
	ItemType string `json:"item_type"`
}

// Published is one repo class's enumeration, as cached.
type Published struct {
	RepoClass string `json:"repo_class"`
	// QueriedAt is when the librarian was asked. Reported rather than merely
	// recorded: the difference between "this is the newest version" and "this
	// was the newest version when somebody last asked" is the whole reason it
	// is here.
	QueriedAt time.Time `json:"queried_at"`
	Items     []Item    `json:"items"`
}

// Versions is every published version of one item type, newest first. An empty
// item type means every type.
func (p *Published) Versions(itemType string) []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range p.Items {
		if itemType != "" && i.ItemType != itemType {
			continue
		}
		if seen[i.Version] {
			continue
		}
		seen[i.Version] = true
		out = append(out, i.Version)
	}
	versionspec.Sort(out)
	return out
}

// ItemTypes is every item type the enumeration saw, so a message about a repo
// class that has published only schemas can say so.
func (p *Published) ItemTypes() []string {
	seen := map[string]bool{}
	var out []string
	for _, i := range p.Items {
		if !seen[i.ItemType] {
			seen[i.ItemType] = true
			out = append(out, i.ItemType)
		}
	}
	return out
}

// Age is how long ago the enumeration was taken.
func (p *Published) Age(now time.Time) time.Duration { return now.Sub(p.QueriedAt) }

// Enumerate asks a librarian what a repo class has published.
//
// progress may be nil. It is called with a human-readable step as the requests
// complete, because the last one is slow in proportion to how much the repo
// class has published -- 29 seconds for 392 versions, measured -- and silence
// that long reads as a hang.
//
// A content path that does not parse is skipped rather than failing the
// enumeration: a librarian holds content items that are not build artifacts.
func Enumerate(ctx context.Context, client *librarian.Client, repoClass string,
	progress func(string)) (*Published, error) {

	step := func(format string, args ...any) {
		if progress != nil {
			progress(fmt.Sprintf(format, args...))
		}
	}

	identifier, err := client.RepoIdentifier(ctx, repoClass)
	if err != nil {
		return nil, err
	}
	ids, err := client.ContentItemIDs(ctx, identifier)
	if err != nil {
		return nil, err
	}
	step("%s: %d content items", repoClass, len(ids))

	items, err := client.ContentItemsByNID(ctx, ids, func(done, total int) {
		if done < total {
			step("%s: read %d of %d", repoClass, done, total)
		}
	})
	if err != nil {
		return nil, err
	}

	p := &Published{RepoClass: repoClass, QueriedAt: time.Now().UTC()}
	for _, item := range items {
		spec, err := librarian.ParseContentPath(item.Path)
		if err != nil || spec.Name != repoClass {
			continue
		}
		p.Items = append(p.Items, Item{Version: spec.Version, ItemType: spec.ItemType})
	}
	return p, nil
}

// Root is where enumerations are cached.
func Root(home string) string {
	return filepath.Join(home, ".cache", "neuronsphere", "versions")
}

// Path is the cache file for one repo class.
func Path(home, repoClass string) string {
	if home == "" || repoClass == "" {
		return ""
	}
	return filepath.Join(Root(home), repoClass+".json")
}

// Load reads a cached enumeration, or ErrNoCache.
//
// It never queries. A caller that wants fresh data calls Enumerate, which is the
// command the user typed; everything else reads what is there.
func Load(home, repoClass string) (*Published, error) {
	path := Path(home, repoClass)
	if path == "" {
		return nil, fmt.Errorf("%s: %w", repoClass, ErrNoCache)
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("%s: %w", repoClass, ErrNoCache)
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var p Published
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &p, nil
}

// Save writes an enumeration to the cache.
//
// A missing HMD_HOME is not an error: there is nowhere to cache, and refusing to
// answer a question that has already been answered because the result cannot be
// filed would be the wrong trade.
func Save(home string, p *Published) error {
	path := Path(home, p.RepoClass)
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o644, 0o755)
}
