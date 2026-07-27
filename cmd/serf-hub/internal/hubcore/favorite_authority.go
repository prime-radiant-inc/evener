package hubcore

import "slices"

// FavoriteAuthorityQuality records whether an authority fact is complete and
// unambiguous enough to support a presentation decision. The zero value is
// intentionally not complete.
type FavoriteAuthorityQuality string

const (
	FavoriteAuthorityComplete   FavoriteAuthorityQuality = "complete"
	FavoriteAuthorityIncomplete FavoriteAuthorityQuality = "incomplete"
	FavoriteAuthorityAmbiguous  FavoriteAuthorityQuality = "ambiguous"
)

// FavoriteSessionAuthority is a canonical session identity collected by the
// navigation layer. ID is the canonical session identity; Aliases are already
// known local/ref spellings for that same identity. TopLevel and Lineage are
// facts from the full metadata lineage set, not from a rendered or capped
// tree. The caller should derive TopLevel with TopLevelSessionIDs so tree and
// favorite policy share the same nestedSessionIDs rule.
type FavoriteSessionAuthority struct {
	ID       string
	Aliases  []string
	TopLevel bool
	Lineage  FavoriteAuthorityQuality
	Source   FavoriteAuthorityQuality
}

// FavoriteProjectAuthority is a canonical project identity collected by the
// navigation layer. ID must be identifier.Project.ID; display names and paths
// are deliberately not accepted as aliases here.
type FavoriteProjectAuthority struct {
	ID      string
	Quality FavoriteAuthorityQuality
}

// FavoriteNodeKind identifies the current, collision-checked kind of a
// navigation node. Only a current cluster node can positively invalidate a
// session decision; its spelling is not evidence.
type FavoriteNodeKind string

const (
	FavoriteNodeSession  FavoriteNodeKind = "session"
	FavoriteNodeSubagent FavoriteNodeKind = "subagent"
	FavoriteNodeFork     FavoriteNodeKind = "fork"
	FavoriteNodeCluster  FavoriteNodeKind = "cluster"
)

// FavoriteNodeAuthority is a current node classification collected after
// tree construction has identified synthetic nodes and checked collisions.
type FavoriteNodeAuthority struct {
	ID      string
	Kind    FavoriteNodeKind
	Quality FavoriteAuthorityQuality
}

// FavoriteAuthority is the complete in-memory authority input for one
// revalidation pass. It has no source, filesystem, network, persistence, or
// generation dependencies: those boundaries collect these facts before this
// pure classifier is called.
type FavoriteAuthority struct {
	Sessions []FavoriteSessionAuthority
	Projects []FavoriteProjectAuthority
	Nodes    []FavoriteNodeAuthority
}

// FavoriteDecisionState is the read-time presentation classification of one
// stored decision. It is never persisted.
type FavoriteDecisionState string

const (
	FavoriteDecisionValid            FavoriteDecisionState = "valid"
	FavoriteDecisionConfirmedInvalid FavoriteDecisionState = "confirmed-invalid"
	FavoriteDecisionDormant          FavoriteDecisionState = "dormant"
)

// FavoriteDecisionClassification records the state of one stored decision and
// the canonical target when the decision resolved to one. Dormant and
// confirmed-invalid rows remain untouched in persistence.
type FavoriteDecisionClassification struct {
	State        FavoriteDecisionState
	CanonicalKey ArchiveKey
}

// FavoriteRevalidation contains the in-memory result of classifying stored
// decisions. Presentation is keyed by canonical identity and contains only
// true, valid decisions. Classifications retains every input key, including
// false decisions, without mutating the input map.
type FavoriteRevalidation struct {
	Classifications map[ArchiveKey]FavoriteDecisionClassification
	Presentation    map[ArchiveKey]bool
}

type favoriteSessionIndex struct {
	byID           map[string][]FavoriteSessionAuthority
	byAlias        map[string][]string
	ambiguousIDs   map[string]bool
	ambiguousAlias map[string]bool
}

type favoriteProjectIndex struct {
	byID         map[string][]FavoriteProjectAuthority
	ambiguousIDs map[string]bool
}

type favoriteNodeIndex struct {
	byID         map[string][]FavoriteNodeAuthority
	ambiguousIDs map[string]bool
	clusterIDs   map[string]bool
}

// ClassifyFavoriteDecisions classifies stored decisions against already
// collected canonical authority facts. It is pure: it neither reads nor
// writes FavoriteStore, and it does not infer completeness from presentation
// slices or from string prefixes.
func ClassifyFavoriteDecisions(decisions map[ArchiveKey]bool, authority FavoriteAuthority) FavoriteRevalidation {
	result := FavoriteRevalidation{
		Classifications: make(map[ArchiveKey]FavoriteDecisionClassification, len(decisions)),
		Presentation:    make(map[ArchiveKey]bool),
	}
	sessions := indexFavoriteSessions(authority.Sessions)
	projects := indexFavoriteProjects(authority.Projects)
	nodes := indexFavoriteNodes(authority.Nodes, sessions)

	for key, favorited := range decisions {
		classification := classifyFavoriteDecision(key, sessions, projects, nodes)
		result.Classifications[key] = classification
		if favorited && classification.State == FavoriteDecisionValid {
			result.Presentation[classification.CanonicalKey] = true
		}
	}
	return result
}

func indexFavoriteSessions(authorities []FavoriteSessionAuthority) favoriteSessionIndex {
	index := favoriteSessionIndex{
		byID:           make(map[string][]FavoriteSessionAuthority, len(authorities)),
		byAlias:        make(map[string][]string, len(authorities)*2),
		ambiguousIDs:   make(map[string]bool),
		ambiguousAlias: make(map[string]bool),
	}
	for _, authority := range authorities {
		if authority.ID == "" {
			continue
		}
		index.byID[authority.ID] = append(index.byID[authority.ID], authority)
		aliases := append([]string{authority.ID}, authority.Aliases...)
		for _, alias := range aliases {
			if alias == "" {
				continue
			}
			index.byAlias[alias] = appendUniqueString(index.byAlias[alias], authority.ID)
		}
	}
	for id, authorities := range index.byID {
		if len(authorities) > 1 {
			index.ambiguousIDs[id] = true
		}
	}
	for alias, ids := range index.byAlias {
		if len(ids) > 1 {
			index.ambiguousAlias[alias] = true
		}
	}
	return index
}

func indexFavoriteProjects(authorities []FavoriteProjectAuthority) favoriteProjectIndex {
	index := favoriteProjectIndex{
		byID:         make(map[string][]FavoriteProjectAuthority, len(authorities)),
		ambiguousIDs: make(map[string]bool),
	}
	for _, authority := range authorities {
		if authority.ID == "" {
			continue
		}
		index.byID[authority.ID] = append(index.byID[authority.ID], authority)
	}
	for id, authorities := range index.byID {
		if len(authorities) > 1 {
			index.ambiguousIDs[id] = true
		}
	}
	return index
}

func indexFavoriteNodes(authorities []FavoriteNodeAuthority, sessions favoriteSessionIndex) favoriteNodeIndex {
	index := favoriteNodeIndex{
		byID:         make(map[string][]FavoriteNodeAuthority, len(authorities)),
		ambiguousIDs: make(map[string]bool),
		clusterIDs:   make(map[string]bool),
	}
	for _, authority := range authorities {
		if authority.ID == "" {
			continue
		}
		index.byID[authority.ID] = append(index.byID[authority.ID], authority)
	}
	for id, authorities := range index.byID {
		if len(authorities) != 1 || authorities[0].Quality != FavoriteAuthorityComplete {
			index.ambiguousIDs[id] = true
			continue
		}
		node := authorities[0]
		if node.Kind == FavoriteNodeCluster {
			if len(sessions.byID[id]) != 0 {
				// A real canonical session and a synthetic node share an
				// identity. Neither interpretation is safe to present.
				index.ambiguousIDs[id] = true
				continue
			}
			index.clusterIDs[id] = true
		}
	}
	return index
}

func classifyFavoriteDecision(
	key ArchiveKey,
	sessions favoriteSessionIndex,
	projects favoriteProjectIndex,
	nodes favoriteNodeIndex,
) FavoriteDecisionClassification {
	switch key.Kind {
	case "session":
		return classifyFavoriteSession(key, sessions, nodes)
	case "project":
		return classifyFavoriteProject(key, projects)
	default:
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
}

func classifyFavoriteSession(key ArchiveKey, sessions favoriteSessionIndex, nodes favoriteNodeIndex) FavoriteDecisionClassification {
	if nodes.ambiguousIDs[key.ID] {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
	if nodes.clusterIDs[key.ID] {
		return FavoriteDecisionClassification{
			State:        FavoriteDecisionConfirmedInvalid,
			CanonicalKey: key,
		}
	}

	if sessions.ambiguousAlias[key.ID] {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
	ids := sessions.byAlias[key.ID]
	if len(ids) != 1 || sessions.ambiguousIDs[ids[0]] {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
	authorities := sessions.byID[ids[0]]
	if len(authorities) != 1 {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
	authority := authorities[0]
	canonicalKey := ArchiveKey{Kind: "session", ID: authority.ID}
	if authority.Lineage != FavoriteAuthorityComplete || authority.Source != FavoriteAuthorityComplete {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant, CanonicalKey: canonicalKey}
	}
	if !authority.TopLevel {
		return FavoriteDecisionClassification{State: FavoriteDecisionConfirmedInvalid, CanonicalKey: canonicalKey}
	}
	return FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: canonicalKey}
}

func classifyFavoriteProject(key ArchiveKey, projects favoriteProjectIndex) FavoriteDecisionClassification {
	authorities := projects.byID[key.ID]
	if len(authorities) != 1 || projects.ambiguousIDs[key.ID] {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant}
	}
	if authorities[0].Quality != FavoriteAuthorityComplete {
		return FavoriteDecisionClassification{State: FavoriteDecisionDormant, CanonicalKey: key}
	}
	return FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: key}
}

func appendUniqueString(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
