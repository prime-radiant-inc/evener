package hubcore

import (
	"cmp"
	"hash"
	"hash/fnv"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
)

// rosterShownFields are the LiveEntry state a navigation row, status chip or
// activity panel shows, as dotted paths (a path crosses pointers, slices and
// map values transparently). rosterFingerprint hashes exactly these, so a
// probe that changes one invalidates navigation and a probe that changes only
// something else does not. A path names its whole value unless longer paths
// beneath it narrow the choice.
//
// Why each moves the fingerprint while Status holds still: ActiveFlags hides
// the fork action; PendingAsk, PendingEscalation and the escalation card IDs
// (a card's ID names it for its whole life) move the Clear bit and the card a
// row names; PendingQuestion, Failure and LastMessage are the row's why line (a
// question answered and another asked, or a turn retried and failed again,
// between two probes); Capabilities folds daemon state the status string does
// not, and walking the whole set means a new bit moves the fingerprint with no
// one remembering this site; the subagent, job, watch and task fields render on
// the row, a child's watches on the child's row; Lifecycle and LifecycleFresh
// change what resident UI surfaces; Subagents tallies the last line;
// LastTurnEndedAt turns a row Finished when a turn starts and ends between two
// probes; CurrentModel is what a model switch moves.
var rosterShownFields = []string{
	"Status", "ActiveFlags", "Crashed", "PendingAsk", "PendingEscalation",
	"PendingEscalations.EscalationID",
	"PendingQuestion", "Failure",
	"Capabilities", "CapabilitiesKnown",
	"RunningSubagentIDs", "RunningSubagentStates", "ChildWatches",
	"RunningJobs.JobID", "RunningJobs.JobType", "RunningJobs.Status",
	"RunningJobs.Command", "RunningJobs.Task", "RunningJobs.Reason",
	"CompletedJobs.JobID", "CompletedJobs.JobType", "CompletedJobs.Status",
	"CompletedJobs.Command", "CompletedJobs.Task", "CompletedJobs.Reason",
	"Watches",
	"Lifecycle.Phase", "Lifecycle.TimeoutMillis", "Lifecycle.EligibleSince",
	"Lifecycle.Deadline", "Lifecycle.Failure", "Lifecycle.Blockers", "LifecycleFresh",
	"Tasks.Total", "Tasks.Done", "Tasks.Cancelled", "Tasks.Current",
	"Subagents", "LastTurnEndedAt", "LastMessage", "CurrentModel",
}

// rosterNotShownFields are the LiveEntry fields rosterFingerprint leaves out
// on purpose. A field on neither list fails TestRosterFingerprintCoversLiveEntry,
// which forces the next added field to be classified.
//
// Entry is the rendezvous record and SessionID is the map key the fingerprint
// already hashes. Activity's bars move every minute a session works, so
// hashing them would broadcast an invalidation on every probe. Profile and
// Project reach no row. Manager is read from the session, outside navigation.
// The rest are the parts of a shown struct that no row
// reads: an escalation card's body (the ID names the card), a job's exit and
// provenance details, a task list's Remaining (its three counts determine it)
// and a lifecycle's last retirement attempt.
var rosterNotShownFields = []string{
	"Entry", "SessionID", "Activity", "Profile", "Project", "Manager",
	"PendingEscalations.ThreadID", "PendingEscalations.Ref", "PendingEscalations.Mode",
	"PendingEscalations.Tool", "PendingEscalations.Kind", "PendingEscalations.DeniedPath",
	"PendingEscalations.Command", "PendingEscalations.OutputSoFar", "PendingEscalations.PartiallyRan",
	"RunningJobs.ExhaustionBudget", "RunningJobs.ExhaustionLimit", "RunningJobs.Resumable", "RunningJobs.ExitCode", "RunningJobs.OutputBytes", "RunningJobs.TranscriptRef",
	"RunningJobs.FromWatch", "RunningJobs.Background", "RunningJobs.Intent", "RunningJobs.ParentDelegateID", "RunningJobs.DelegateID", "RunningJobs.OriginTurnID", "RunningJobs.OriginToolCallID", "RunningJobs.OriginItemID",
	"CompletedJobs.ExhaustionBudget", "CompletedJobs.ExhaustionLimit", "CompletedJobs.Resumable", "CompletedJobs.ExitCode", "CompletedJobs.OutputBytes", "CompletedJobs.TranscriptRef",
	"CompletedJobs.FromWatch", "CompletedJobs.Background", "CompletedJobs.Intent", "CompletedJobs.ParentDelegateID", "CompletedJobs.DelegateID", "CompletedJobs.OriginTurnID", "CompletedJobs.OriginToolCallID", "CompletedJobs.OriginItemID",
	"Lifecycle.LastAttempt", "Tasks.Remaining",
}

// fieldTrie is a set of dotted field paths. A node with no children stands for
// the whole value at that point.
type fieldTrie map[string]fieldTrie

func newFieldTrie(paths []string) fieldTrie {
	root := fieldTrie{}
	for _, path := range paths {
		node := root
		for name := range strings.SplitSeq(path, ".") {
			if node[name] == nil {
				node[name] = fieldTrie{}
			}
			node = node[name]
		}
	}
	return root
}

var rosterShownTrie = newFieldTrie(rosterShownFields)

// hashFields writes the parts of v the trie selects, walking pointers, slices
// and map values.
func hashFields(h hash.Hash64, v reflect.Value, node fieldTrie) {
	if len(node) == 0 {
		writeHashValue(h, v)
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			_, _ = h.Write([]byte{0})
			return
		}
		_, _ = h.Write([]byte{1})
		hashFields(h, v.Elem(), node)
	case reflect.Slice:
		writeHashValue(h, reflect.ValueOf(v.Len()))
		for n := range v.Len() {
			hashFields(h, v.Index(n), node)
		}
	case reflect.Map:
		writeHashValue(h, reflect.ValueOf(v.Len()))
		for _, key := range sortedMapKeys(v) {
			writeHashString(h, key.String())
			hashFields(h, v.MapIndex(key), node)
		}
	case reflect.Struct:
		names := make([]string, 0, len(node))
		for name := range node {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			hashFields(h, v.FieldByName(name), node[name])
		}
	default:
		panic("narrowed roster field of unsupported kind " + v.Kind().String())
	}
}

// orderInsensitive returns e with every list a daemon may report in any order
// sorted on a copy (the caller's slices are not this function's to reorder),
// and with the spellings that mean the same thing folded together: a nil task
// aggregate is an empty one, since neither puts a task line on the row, and
// the turn end is a millisecond instant like the rest of the wire.
func orderInsensitive(e LiveEntry) LiveEntry {
	e.ActiveFlags = slices.Sorted(slices.Values(e.ActiveFlags))
	e.RunningSubagentIDs = slices.Sorted(slices.Values(e.RunningSubagentIDs))
	e.RunningJobs = sortedJobs(e.RunningJobs)
	e.CompletedJobs = sortedJobs(e.CompletedJobs)
	e.Watches = sortedWatches(e.Watches)
	if e.ChildWatches != nil {
		children := make(map[string][]appwire.EvenerWatchInfo, len(e.ChildWatches))
		for id, watches := range e.ChildWatches {
			children[id] = sortedWatches(watches)
		}
		e.ChildWatches = children
	}
	if e.Tasks == nil {
		e.Tasks = &appwire.TaskAggregate{}
	}
	e.LastTurnEndedAt = time.UnixMilli(UnixMilliseconds(e.LastTurnEndedAt))
	return e
}

func sortedJobs(jobs []appwire.EvenerJobInfo) []appwire.EvenerJobInfo {
	return slices.SortedStableFunc(slices.Values(jobs), func(a, b appwire.EvenerJobInfo) int {
		return cmp.Or(strings.Compare(a.JobID, b.JobID), strings.Compare(a.JobType, b.JobType), strings.Compare(a.Status, b.Status))
	})
}

func sortedWatches(watches []appwire.EvenerWatchInfo) []appwire.EvenerWatchInfo {
	return slices.SortedStableFunc(slices.Values(watches), func(a, b appwire.EvenerWatchInfo) int {
		return strings.Compare(a.ID, b.ID)
	})
}

// rosterFingerprint hashes what navigation shows of the live set: the
// rosterShownFields of every entry, in session ID order.
func rosterFingerprint(bySess map[string]LiveEntry) uint64 {
	ids := make([]string, 0, len(bySess))
	for id := range bySess {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := fnv.New64a()
	for _, id := range ids {
		writeHashString(h, id)
		hashFields(h, reflect.ValueOf(orderInsensitive(bySess[id])), rosterShownTrie)
	}
	return h.Sum64()
}
