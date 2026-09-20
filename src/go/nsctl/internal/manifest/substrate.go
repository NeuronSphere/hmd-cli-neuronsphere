package manifest

import (
	"fmt"
	"strings"
)

// Substrate is how much infrastructure an environment runs beneath what it
// declares (NERD014 SPEC001).
type Substrate string

const (
	// SubstrateNone is the Floci account and the control plane, nothing else:
	// no database, no graph, no cluster, and an empty first changeset. The
	// mode for a repo that deploys with its own toolset against infrastructure
	// it already has.
	SubstrateNone Substrate = "none"
	// SubstrateCore adds the environment database, the graph and the
	// database-account service, and deploys the core, network and database
	// instances. Enough for services; no cluster for charts.
	SubstrateCore Substrate = "core"
	// SubstrateFull is everything, including the k3s cluster and its
	// operators. The default, and what every environment ran before the key
	// existed.
	SubstrateFull Substrate = "full"
)

// Substrates lists the modes in the order they add things.
var Substrates = []Substrate{SubstrateNone, SubstrateCore, SubstrateFull}

// ParseSubstrate reads a mode, treating an empty value as full. The error
// names every accepted value, because it is the whole of what a user who
// mistyped one needs (NERD014 SPEC003).
func ParseSubstrate(s string) (Substrate, error) {
	switch v := Substrate(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return SubstrateFull, nil
	case SubstrateNone, SubstrateCore, SubstrateFull:
		return v, nil
	default:
		return "", fmt.Errorf("unknown substrate %q; choose one of none, core, full", s)
	}
}

// SubstrateMode is the recorded mode, full when there is none. Safe on a nil
// manifest, which is what Load returns for an environment that declares
// nothing.
func (m *Manifest) SubstrateMode() Substrate {
	if m == nil {
		return SubstrateFull
	}
	mode, err := ParseSubstrate(m.Substrate)
	if err != nil {
		// Validate has already refused this on load; a manifest built in
		// memory with a bad value is a programming error, and full is the
		// safe reading of it.
		return SubstrateFull
	}
	return mode
}

// LoadSubstrate is the recorded mode of a slug's environment: full when it
// has no manifest, and an error naming the file when the manifest is
// unreadable, which callers report and then treat as full -- never as none.
func LoadSubstrate(home, slug string, lookup Lookup) (Substrate, error) {
	m, err := Load(home, slug, lookup)
	if err != nil {
		return SubstrateFull, err
	}
	return m.SubstrateMode(), nil
}
