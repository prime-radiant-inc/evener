package hubcore

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"io"
	"reflect"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// shownMetaFields are the SessionMeta fields a navigation row can show, as
// dotted paths into struct-valued fields. rootFingerprint hashes exactly these,
// so a write that changes one refreshes the row and a write that changes only
// something else does not.
//
// Where each is read: ID, Name, OriginalPrompt and ForkLabel build the title
// (nodeTitle); ParentSessionID, IsSubagent and ForkLabel place the row
// (nestedSessionIDs, nodeKind); WorktreePath, WorktreeManaged,
// WorktreeRestoreRoot and EnvInfo.WorkingDir resolve the project
// (EffectiveWorkingDir); EnvInfo.GitBranch is the branch; Origin classifies test
// runs; CreatedAt and UpdatedAt order and age the row; TurnCount and
// AcceptedInputTurns decide dormancy; Model, LastMessage and LastTurnEndedAt are
// row facts for an ended session; ObservedBy re-saves without moving UpdatedAt;
// JobTreeRootSessionID feeds the restart-required ownership walk.
var shownMetaFields = []string{
	"ID", "Name", "OriginalPrompt", "ForkLabel", "ParentSessionID", "IsSubagent",
	"Origin", "Model", "CreatedAt", "UpdatedAt", "LastTurnEndedAt", "LastMessage",
	"TurnCount", "AcceptedInputTurns", "ObservedBy",
	"WorktreePath", "WorktreeManaged", "WorktreeRestoreRoot",
	"EnvInfo.WorkingDir", "EnvInfo.GitBranch", "JobTreeRootSessionID",
}

// notShownMetaFields are the SessionMeta fields no navigation row reads. A
// SessionMeta field on neither list fails TestShownMetaFieldsCoverSessionMeta,
// which is what forces the next added field to be classified.
var notShownMetaFields = []string{
	"ProfileID", "CheapModel", "VisionModel", "Config", "Revision",
	"TurnBudgetWarningEmitted", "LastInputTokens", "NameSource", "NameUpdatedAt",
	"DivergenceTurn", "Goal", "PinnedNote", "Skills", "HumanNote", "AgentNote",
	"SessionURLs", "EnvContext", "ReasoningEffortEscalated", "CumulativeUsage",
	"WorkMillis", "JobTreeRevision",
	"EnvInfo.Platform", "EnvInfo.OSVersion", "EnvInfo.Today", "EnvInfo.KnowledgeCutoff",
	"EnvInfo.IsGitRepo", "EnvInfo.GitOriginURL", "EnvInfo.GitModifiedFiles",
	"EnvInfo.GitUntrackedFiles", "EnvInfo.GitRecentCommitTitles", "EnvInfo.Workspace",
	"EnvInfo.Resources",
}

// metaFieldIndex resolves a dotted SessionMeta field path to a reflect index.
func metaFieldIndex(path string) ([]int, bool) {
	var index []int
	typ := reflect.TypeFor[schema.SessionMeta]()
	for name := range strings.SplitSeq(path, ".") {
		if typ.Kind() != reflect.Struct {
			return nil, false
		}
		field, ok := typ.FieldByName(name)
		if !ok {
			return nil, false
		}
		index = append(index, field.Index...)
		typ = field.Type
	}
	return index, true
}

// shownMetaFieldIndexes are shownMetaFields resolved to reflect index paths once.
var shownMetaFieldIndexes = func() [][]int {
	indexes := make([][]int, len(shownMetaFields))
	for n, path := range shownMetaFields {
		index, ok := metaFieldIndex(path)
		if !ok {
			panic("shownMetaFields names a field SessionMeta lacks: " + path)
		}
		indexes[n] = index
	}
	return indexes
}()

// rootFingerprint hashes what navigation shows of the index: the shown fields
// of every non-subagent entry, in index order, and the set of subagents. A
// subagent contributes its identity and links only (ID, ParentSessionID,
// JobTreeRootSessionID, all fixed at spawn), combined order-independently
// because a subagent's autosave moves it within the index. So a subagent
// appearing or disappearing moves the fingerprint, and nothing else about a
// subagent does.
func rootFingerprint(all []PastEntry) uint64 {
	roots := fnv.New64a()
	var subagents uint64
	for n := range all {
		e := &all[n]
		if e.Meta.IsSubagent {
			h := fnv.New64a()
			writeHashString(h, e.ID)
			writeHashString(h, e.Meta.ParentSessionID)
			writeHashString(h, e.Meta.JobTreeRootSessionID)
			subagents += h.Sum64() // commutative: independent of index order
			continue
		}
		v := reflect.ValueOf(&e.Meta).Elem() // addressable: no copy of the whole meta
		writeHashString(roots, e.ID)
		for _, index := range shownMetaFieldIndexes {
			writeHashValue(roots, v.FieldByIndex(index))
		}
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], subagents)
	_, _ = roots.Write(b[:])
	return roots.Sum64()
}

func writeHashString(h hash.Hash64, s string) {
	_, _ = io.WriteString(h, s)
	_, _ = h.Write([]byte{0})
}

func writeHashValue(h hash.Hash64, v reflect.Value) {
	var b [8]byte
	switch v.Kind() {
	case reflect.String:
		writeHashString(h, v.String())
		return
	case reflect.Bool:
		if v.Bool() {
			b[0] = 1
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		binary.LittleEndian.PutUint64(b[:], uint64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		binary.LittleEndian.PutUint64(b[:], v.Uint())
	case reflect.Slice:
		binary.LittleEndian.PutUint64(b[:], uint64(v.Len())) // frames the elements
		_, _ = h.Write(b[:])
		for n := range v.Len() {
			writeHashValue(h, v.Index(n))
		}
		return
	case reflect.Struct:
		t, ok := reflect.TypeAssert[time.Time](v)
		if !ok {
			panic("shown SessionMeta field of unsupported struct type " + v.Type().String())
		}
		binary.LittleEndian.PutUint64(b[:], uint64(t.UnixNano()))
	default:
		panic("shown SessionMeta field of unsupported kind " + v.Kind().String())
	}
	_, _ = h.Write(b[:])
}
