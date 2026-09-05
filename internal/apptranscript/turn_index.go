package apptranscript

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

const (
	turnIndexVersion        = 11
	turnIndexJournalVersion = 3
	turnIndexAnchorBytes    = 256

	// Index records contain fixed metadata plus a bounded expansion of fields
	// already present in the authoritative transcript. This allowance and ratio
	// cover that representation (including tool seeds) without imposing a fixed
	// cap on large legitimate transcripts.
	turnIndexSidecarAllowance = int64(1 << 20)
	turnIndexSidecarRatio     = int64(8)
)

// FilePage is one bounded page read directly from a transcript file.
type FilePage struct {
	Turns      []appwire.Turn
	NextCursor string
}

// ReadStats describes the work performed by one bounded transcript read. It is
// reported only through observeTurnIndexRead for package tests and benchmarks.
type ReadStats struct {
	IndexedBytes   int64
	ProjectedTurns int
	ProjectedItems int

	// The remaining fields are intentionally unexported test/benchmark
	// instrumentation. Production callers do not depend on them.
	indexBytesCopied      int64
	indexBytesSerialized  int64
	indexBytesPersisted   int64
	resolverEntriesCopied int64
	resolverHistoryVisits int64
	recordVisits          int64
	anchorBytesRead       int64
	journalRecords        int64
	rebuilt               bool
	usageScans            int64
	failureScans          int64
	derivedScans          int64
}

var (
	observeTurnIndexReadMu sync.RWMutex
	observeTurnIndexRead   func(ReadStats)
)

// InstallReadObserverForTesting installs instrumentation for bounded transcript
// reads and returns a function that restores the previous observer. The callback
// is invoked without holding the observer lock.
func InstallReadObserverForTesting(observer func(ReadStats)) func() {
	observeTurnIndexReadMu.Lock()
	previous := observeTurnIndexRead
	observeTurnIndexRead = observer
	observeTurnIndexReadMu.Unlock()
	return func() {
		observeTurnIndexReadMu.Lock()
		observeTurnIndexRead = previous
		observeTurnIndexReadMu.Unlock()
	}
}

type turnIndexDisk struct {
	Version                 int               `json:"version"`
	TranscriptFormatVersion int               `json:"transcript_format_version"`
	TranscriptSize          int64             `json:"transcript_size"`
	CompleteSize            int64             `json:"complete_size"`
	Header                  transcript.Header `json:"header"`
	Records                 []indexedTurn     `json:"records"`
	VisibleRecords          int               `json:"visible_records"`
	MaxLineBytes            int               `json:"max_line_bytes"`
	ProjectionID            string            `json:"projection_identity"`
	PrefixStamp             string            `json:"prefix_stamp"`
	FileIdentity            string            `json:"file_identity"`
	ChangeIdentity          string            `json:"change_identity"`
	ModTimeUnixNS           int64             `json:"mod_time_unix_ns"`
	IntegrityStamp          string            `json:"integrity_stamp"`
	FirstAnchor             turnIndexAnchor   `json:"first_anchor"`
	TailAnchor              turnIndexAnchor   `json:"tail_anchor"`
	Incarnation             string            `json:"incarnation"`

	// deltaRoot is an immutable persistent rope of records loaded from or
	// destined for the append-only journal. Keeping suffixes out of Records
	// lets an advancing reader publish a new index without copying or mutating
	// history still visible to concurrent readers.
	deltaRoot          *turnIndexRecordNode `json:"-"`
	journalValidBytes  int64                `json:"-"`
	journalNeedsRepair bool                 `json:"-"`
	journalApplied     bool                 `json:"-"`
	baseVisible        []int                `json:"-"`
}

type turnIndexRecordNode struct {
	left, right *turnIndexRecordNode
	records     []indexedTurn
	count       int
	visible     int
	height      int
}

type turnIndexJournalFrame struct {
	Version                 int               `json:"version"`
	PreviousStamp           string            `json:"previous_stamp"`
	TranscriptFormatVersion int               `json:"transcript_format_version"`
	TranscriptSize          int64             `json:"transcript_size"`
	CompleteSize            int64             `json:"complete_size"`
	Header                  transcript.Header `json:"header"`
	Records                 []indexedTurn     `json:"records"`
	VisibleRecords          int               `json:"visible_records"`
	MaxLineBytes            int               `json:"max_line_bytes"`
	ProjectionID            string            `json:"projection_identity"`
	PrefixStamp             string            `json:"prefix_stamp"`
	FileIdentity            string            `json:"file_identity"`
	ChangeIdentity          string            `json:"change_identity"`
	ModTimeUnixNS           int64             `json:"mod_time_unix_ns"`
	IntegrityStamp          string            `json:"integrity_stamp"`
	FirstAnchor             turnIndexAnchor   `json:"first_anchor"`
	TailAnchor              turnIndexAnchor   `json:"tail_anchor"`
	Incarnation             string            `json:"incarnation"`
}

type indexedTurn struct {
	Offset       int64             `json:"offset"`
	Length       int64             `json:"length"`
	Index        int               `json:"index"`
	Kind         string            `json:"kind"`
	Visible      bool              `json:"visible"`
	VisibleIndex int               `json:"visible_index,omitempty"`
	ItemCount    uint32            `json:"item_count"`
	ToolSeed     map[string]string `json:"tool_seed,omitempty"`
	ToolChanges  []toolNameChange  `json:"tool_changes,omitempty"`
	// TurnKind is the entry's semantic turn kind ("USER_INPUT", "ASSISTANT",
	// ...), the logical-grouping input (Kind above is the record kind
	// "entry"). TurnID is the entry's resolved persistedTurnID at scan time
	// — a group opener's record names the logical turn. GroupItems is the
	// number of items this record contributes to its group's MERGED count
	// and GroupCalls the call ids its merged items introduce (the counting
	// form of mergeGroupedItems).
	TurnKind   schema.TurnKind `json:"turn_kind,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	GroupItems uint32          `json:"group_items,omitempty"`
	GroupCalls []string        `json:"group_calls,omitempty"`
}

// groupRole classifies a record within its logical turn group.
type groupRole string

const (
	// groupOpener starts a logical turn (USER_INPUT).
	groupOpener groupRole = "opener"
	// groupContinuation extends the open logical turn (ASSISTANT, TOOL,
	// TOOL_RESULTS, TURN_FAILURE, STEERING). With no open turn it starts one.
	groupContinuation groupRole = "continuation"
	// groupStandalone is its own logical turn, grouped with nothing
	// (every other kind).
	groupStandalone groupRole = "standalone"
)

// groupRoleFor classifies a turn kind's role in a logical turn.
func groupRoleFor(kind schema.TurnKind) groupRole {
	switch {
	case opensLogicalTurn(kind):
		return groupOpener
	case continuesLogicalTurn(kind):
		return groupContinuation
	default:
		return groupStandalone
	}
}

type turnIndexAnchor struct {
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	Stamp  string `json:"stamp"`
}

type toolNameChange struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Lookup bool   `json:"lookup,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// LatestFromFile returns the newest bounded turn window without projecting the
// historical prefix. A non-positive limit preserves the full-read behavior.
func (c *TurnCache) LatestFromFile(path string, maxLineBytes int, limit int, project BoundedEntryProjector) (turns []appwire.Turn, olderCursor string, err error) {
	if limit <= 0 {
		all, err := c.TurnsFromFile(path, maxLineBytes, fullProjector(project))
		if err != nil {
			return nil, "", err
		}
		turns, cursor := appwire.WindowTurns(all, limit)
		return turns, cursor, nil
	}
	index, stats, err := c.loadTurnIndex(path, maxLineBytes, project)
	if err != nil {
		return nil, "", err
	}
	count := index.logicalTurnCount()
	lo := 0
	if count > limit {
		lo = count - limit
		olderCursor = strconv.Itoa(lo)
	}
	turns, projected, err := projectIndexedRangeObserved(path, index, lo, count, project, &stats)
	if err != nil {
		c.invalidate(path)
		return nil, "", err
	}
	stats.ProjectedTurns = projected
	observeIndexRead(stats)
	return turns, olderCursor, nil
}

// PageFromFile returns turns older than cursor without projecting records
// outside that page. A non-positive limit delegates to the legacy full reader.
func (c *TurnCache) PageFromFile(path string, maxLineBytes int, cursor string, limit int, project BoundedEntryProjector) (FilePage, error) {
	if limit <= 0 {
		all, err := c.TurnsFromFile(path, maxLineBytes, fullProjector(project))
		if err != nil {
			return FilePage{}, err
		}
		page := appwire.PageTurns(all, cursor, limit)
		return FilePage{Turns: page.Data, NextCursor: page.NextCursor}, nil
	}
	index, stats, err := c.loadTurnIndex(path, maxLineBytes, project)
	if err != nil {
		return FilePage{}, err
	}
	hi := index.logicalTurnCount()
	if cursor != "" {
		if parsed, err := strconv.Atoi(cursor); err == nil {
			hi = parsed
		}
	}
	if hi > index.logicalTurnCount() {
		hi = index.logicalTurnCount()
	}
	if hi < 0 {
		hi = 0
	}
	lo := max(hi-limit, 0)
	next := ""
	if lo > 0 {
		next = strconv.Itoa(lo)
	}
	turns, projected, err := projectIndexedRangeObserved(path, index, lo, hi, project, &stats)
	if err != nil {
		c.invalidate(path)
		return FilePage{}, err
	}
	stats.ProjectedTurns = projected
	observeIndexRead(stats)
	return FilePage{Turns: turns, NextCursor: next}, nil
}

// TurnCountFromFile returns the indexed logical turn count without projecting
// any turns. The projector participates in the same visibility and sidecar
// identity rules as LatestFromFile and PageFromFile.
func (c *TurnCache) TurnCountFromFile(path string, maxLineBytes int, project BoundedEntryProjector) (int, error) {
	index, stats, err := c.loadTurnIndex(path, maxLineBytes, project)
	if err != nil {
		return 0, err
	}
	stats.ProjectedTurns = 0
	observeIndexRead(stats)
	return index.logicalTurnCount(), nil
}

func fullProjector(project BoundedEntryProjector) EntryProjector {
	toolNames := map[string]string{}
	return func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		if project == nil {
			return nil
		}
		return project(turn, turnID, turnIndex, toolNames)
	}
}

func observeIndexRead(stats ReadStats) {
	observeTurnIndexReadMu.RLock()
	observer := observeTurnIndexRead
	observeTurnIndexReadMu.RUnlock()
	if observer != nil {
		observer(stats)
	}
}

// logicalTurnCount returns the number of turns a full grouped projection
// emits: the prelude (when the header carries one) plus every logical group
// whose merged item count is nonzero. It is a single allocation-free forward
// pass — bounded append reads call it on every request, so it must not
// materialize groups or walk spans backward.
func (d turnIndexDisk) logicalTurnCount() int {
	count := 0
	if PreludeTurn(d.Header) != nil {
		count++
	}
	n := d.recordCount()
	groupItems := uint64(0)
	for i := range n {
		record := d.recordAt(i)
		if i > 0 && recordStartsGroup(record.TurnKind, d.recordAt(i-1).TurnKind) {
			// The previous group just closed: count it when it projected
			// items.
			if groupItems > 0 {
				count++
			}
			groupItems = 0
		}
		groupItems += uint64(record.GroupItems)
	}
	if groupItems > 0 {
		count++
	}
	return count
}

// indexedGroup is one logical turn materialized over persisted records: the
// group's contiguous record span and its accumulated state.
type indexedGroup struct {
	// id is the 0-based logical group ordinal over the whole record list.
	id int
	// start/end are the record ordinals [start, end) of the group's span.
	start, end int
	// turnID is the group opener's persistedTurnID; openerIndex is the
	// opener's 1-based entry index.
	turnID      string
	openerIndex int
	// items is the group's merged item count.
	items uint64
	// calls is the set of command call ids the group's merged items use.
	calls map[string]bool
	// open reports whether the group accepts continuations.
	open bool
}

// indexedGroups materializes the whole record list into logical groups in
// order. It mirrors the accumulator's state machine: openers start groups,
// continuations join the open group, standalone kinds close it. Records whose
// projected items are all invisible (ItemCount == 0) still join their group —
// they carry stamps — but add no items.
func (d turnIndexDisk) indexedGroups() []indexedGroup {
	var groups []indexedGroup
	n := d.recordCount()
	for i := range n {
		record := d.recordAt(i)
		role := groupRoleFor(record.TurnKind)
		join := role == groupContinuation && len(groups) > 0 && groups[len(groups)-1].open
		if join {
			group := &groups[len(groups)-1]
			group.end = i + 1
			group.items += uint64(record.GroupItems)
			for _, id := range record.GroupCalls {
				group.calls[id] = true
			}
			continue
		}
		turnID := record.TurnID
		if turnID == "" {
			// Defensive: a record the version bump let through without a
			// resolved id still answers by its fallback rule.
			turnID = persistedTurnID(record.KindTurn(), record.Index)
		}
		group := indexedGroup{
			id:          len(groups),
			start:       i,
			end:         i + 1,
			turnID:      turnID,
			openerIndex: record.Index,
			items:       uint64(record.GroupItems),
			calls:       cloneGroupCalls(record.GroupCalls),
			open:        role != groupStandalone,
		}
		groups = append(groups, group)
	}
	return groups
}

// KindTurn reconstructs the record's turn kind from persisted fields (the
// turn kind itself is persisted as TurnKind).
func (r indexedTurn) KindTurn() schema.Turn {
	return schema.Turn{Kind: r.TurnKind}
}

func (d turnIndexDisk) recordCount() int {
	return len(d.Records) + recordNodeCount(d.deltaRoot)
}

func (d turnIndexDisk) recordAt(i int) indexedTurn {
	if i < len(d.Records) {
		return d.Records[i]
	}
	return recordNodeAt(d.deltaRoot, i-len(d.Records))
}

func recordNodeCount(node *turnIndexRecordNode) int {
	if node == nil {
		return 0
	}
	return node.count
}

func recordNodeHeight(node *turnIndexRecordNode) int {
	if node == nil {
		return 0
	}
	return node.height
}

func newRecordLeaf(records []indexedTurn) *turnIndexRecordNode {
	if len(records) == 0 {
		return nil
	}
	visible := 0
	for i := range records {
		if records[i].Visible {
			visible++
		}
	}
	return &turnIndexRecordNode{records: records, count: len(records), visible: visible, height: 1}
}

func joinRecordNodes(left, right *turnIndexRecordNode) *turnIndexRecordNode {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if recordNodeHeight(left) > recordNodeHeight(right)+1 {
		joined := joinRecordNodes(left.right, right)
		return balanceRecordNode(left.left, joined)
	}
	if recordNodeHeight(right) > recordNodeHeight(left)+1 {
		joined := joinRecordNodes(left, right.left)
		return balanceRecordNode(joined, right.right)
	}
	return makeRecordBranch(left, right)
}

func balanceRecordNode(left, right *turnIndexRecordNode) *turnIndexRecordNode {
	if recordNodeHeight(left) > recordNodeHeight(right)+1 {
		return makeRecordBranch(left.left, makeRecordBranch(left.right, right))
	}
	if recordNodeHeight(right) > recordNodeHeight(left)+1 {
		return makeRecordBranch(makeRecordBranch(left, right.left), right.right)
	}
	return makeRecordBranch(left, right)
}

func makeRecordBranch(left, right *turnIndexRecordNode) *turnIndexRecordNode {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	height := recordNodeHeight(left)
	if got := recordNodeHeight(right); got > height {
		height = got
	}
	return &turnIndexRecordNode{left: left, right: right, count: recordNodeCount(left) + recordNodeCount(right), visible: recordNodeVisible(left) + recordNodeVisible(right), height: height + 1}
}

func recordNodeVisible(node *turnIndexRecordNode) int {
	if node == nil {
		return 0
	}
	return node.visible
}

// visibleRecordAt returns the record with zero-based visible rank. It uses the
// immutable rope's visible subtree counts, so invisible history is never walked.
func (d turnIndexDisk) visibleRecordAt(rank int, stats *ReadStats) (indexedTurn, bool) {
	if rank < 0 || rank >= d.VisibleRecords {
		return indexedTurn{}, false
	}
	baseVisible := d.baseVisible
	if baseVisible == nil {
		baseVisible = visibleRecordOrdinals(d.Records)
	}
	if rank < len(baseVisible) {
		if stats != nil {
			stats.recordVisits++
		}
		return d.Records[baseVisible[rank]], true
	}
	return visibleNodeAt(d.deltaRoot, rank-len(baseVisible), stats)
}

func visibleNodeAt(node *turnIndexRecordNode, rank int, stats *ReadStats) (indexedTurn, bool) {
	if node == nil || rank < 0 || rank >= node.visible {
		return indexedTurn{}, false
	}
	if stats != nil {
		stats.recordVisits++
	}
	if node.records != nil {
		for i := range node.records {
			if node.records[i].Visible {
				if rank == 0 {
					return node.records[i], true
				}
				rank--
			}
		}
		return indexedTurn{}, false
	}
	leftVisible := recordNodeVisible(node.left)
	if rank < leftVisible {
		return visibleNodeAt(node.left, rank, stats)
	}
	return visibleNodeAt(node.right, rank-leftVisible, stats)
}

func visibleRecordOrdinals(records []indexedTurn) []int {
	ordinals := make([]int, 0)
	for i := range records {
		if records[i].Visible {
			ordinals = append(ordinals, i)
		}
	}
	return ordinals
}

func recordNodeAt(node *turnIndexRecordNode, i int) indexedTurn {
	if node == nil || i < 0 || i >= node.count {
		return indexedTurn{}
	}
	if node.records != nil {
		return node.records[i]
	}
	leftCount := recordNodeCount(node.left)
	if i < leftCount {
		return recordNodeAt(node.left, i)
	}
	return recordNodeAt(node.right, i-leftCount)
}

func (c *TurnCache) loadTurnIndex(path string, maxLineBytes int, project BoundedEntryProjector) (turnIndexDisk, ReadStats, error) {
	return c.loadTurnIndexInternal(path, maxLineBytes, project, false)
}

func (c *TurnCache) loadTurnIndexForItemPaging(path string, maxLineBytes int, project BoundedEntryProjector) (turnIndexDisk, ReadStats, error) {
	return c.loadTurnIndexInternal(path, maxLineBytes, project, true)
}

func (c *TurnCache) loadTurnIndexInternal(path string, maxLineBytes int, project BoundedEntryProjector, strictItemAppendValidation bool) (turnIndexDisk, ReadStats, error) {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()

	var stats ReadStats
	file, err := os.Open(path)
	if err != nil {
		c.invalidate(path)
		return turnIndexDisk{}, stats, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close() //nolint:errcheck // read-only file; close errors are not actionable
	info, err := file.Stat()
	if err != nil {
		c.invalidate(path)
		return turnIndexDisk{}, stats, fmt.Errorf("stat transcript: %w", err)
	}
	projectionID := projectionIdentity(project)
	currentFileIdentity := fileIdentity(info)
	currentChangeIdentity := fileChangeIdentity(info)

	var candidate *turnIndexDisk
	fromCache := false
	identityMatches := false
	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.turnIndex != nil {
		candidate = entry.turnIndex
		fromCache = true
		identityMatches = entry.size == info.Size() && entry.mod.Equal(info.ModTime()) &&
			entry.fileIdentity == currentFileIdentity && entry.changeIdentity == currentChangeIdentity
	}
	c.mu.Unlock()
	if candidate == nil {
		if disk, err := readTurnIndexWithJournal(path+".appwire-index.json", info.Size()); err == nil {
			candidate = &disk
		}
	}

	if candidate != nil && !fromCache {
		identityMatches = candidate.TranscriptSize == info.Size() && candidate.ModTimeUnixNS == info.ModTime().UnixNano() &&
			candidate.FileIdentity == currentFileIdentity && candidate.ChangeIdentity == currentChangeIdentity
	}
	sameFile := candidate != nil && currentFileIdentity != "" && candidate.FileIdentity == currentFileIdentity
	appendOnly := sameFile && candidate.TranscriptSize < info.Size()
	index, start, validatedBytes := usableTurnIndex(file, info.Size(), maxLineBytes, projectionID, candidate, identityMatches, appendOnly, fromCache, &stats)
	if start >= 0 && appendOnly && strictItemAppendValidation {
		// Sparse anchors can prove that their two small regions are unchanged,
		// but cannot prove that an arbitrary interior rewrite did not occur before
		// an append. Item cursors require the complete indexed prefix to validate
		// before preserving its incarnation, including for a warm candidate.
		stamp, readBytes := prefixStamp(file, index.CompleteSize)
		validatedBytes += readBytes
		if index.PrefixStamp == "" || index.PrefixStamp != stamp {
			start = -1
		}
	}
	if start >= 0 && !fromCache && !validIndexedItemCounts(index) {
		start = -1
	}
	if start >= 0 && strings.TrimSpace(index.Incarnation) == "" {
		start = -1
	}
	rebuilt := start < 0
	if start < 0 {
		incarnation, err := newTurnIndexIncarnation()
		if err != nil {
			c.invalidate(path)
			return turnIndexDisk{}, stats, err
		}
		index = turnIndexDisk{
			Version:                 turnIndexVersion,
			TranscriptFormatVersion: transcript.FormatVersion,
			MaxLineBytes:            maxLineBytes,
			ProjectionID:            projectionID,
			PrefixStamp:             initialPrefixStamp(),
			Incarnation:             incarnation,
		}
		start = 0
	} else if fromCache && identityMatches && start == info.Size() {
		c.mu.Lock()
		c.touch(path)
		c.mu.Unlock()
		return index, stats, nil
	} else if !appendOnly && !identityMatches {
		incarnation, err := newTurnIndexIncarnation()
		if err != nil {
			c.invalidate(path)
			return turnIndexDisk{}, stats, err
		}
		index.Incarnation = incarnation
		rebuilt = true
	}
	stats.rebuilt = rebuilt
	stats.IndexedBytes = validatedBytes
	previousIndex := index
	resolver := map[string]string{}
	if !rebuilt {
		c.mu.Lock()
		entry := c.entries[path]
		if fromCache && entry.toolResolver != nil {
			// indexMu excludes every other scanner for this cache, so the private
			// resolver can advance in place. It is never published in an index.
			resolver = entry.toolResolver
		} else {
			resolver = replayToolResolver(index, &stats)
		}
		c.mu.Unlock()
	}
	index = cloneTurnIndexForAppend(index)
	indexedBytes, err := scanTurnIndex(file, info.Size(), start, maxLineBytes, &index, resolver, project)
	if err != nil {
		c.invalidate(path)
		return turnIndexDisk{}, stats, err
	}
	stats.IndexedBytes += indexedBytes
	index.TranscriptSize = info.Size()
	index.MaxLineBytes = maxLineBytes
	index.ProjectionID = projectionID
	index.FileIdentity = currentFileIdentity
	index.ChangeIdentity = currentChangeIdentity
	index.ModTimeUnixNS = info.ModTime().UnixNano()
	stored := index
	c.mu.Lock()
	entry := c.entries[path]
	if entry.size != info.Size() || !entry.mod.Equal(info.ModTime()) || entry.fileIdentity != currentFileIdentity || entry.changeIdentity != currentChangeIdentity {
		entry.turns = nil
		entry.full = false
	}
	entry.size = info.Size()
	entry.mod = info.ModTime()
	entry.fileIdentity = currentFileIdentity
	entry.changeIdentity = currentChangeIdentity
	entry.turnIndex = &stored
	entry.toolResolver = resolver
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()

	basePath := path + ".appwire-index.json"
	if rebuilt || (!fromCache && candidate == nil) {
		index.IntegrityStamp = turnIndexIntegrityStampObserved(index, &stats)
		stored.IntegrityStamp = index.IntegrityStamp
		_ = writeTurnIndex(basePath, index, &stats)
	} else if indexedBytes > 0 || index.journalNeedsRepair {
		if err := appendTurnIndexJournal(basePath+".journal", previousIndex, &index, &stats); err == nil {
			stored.IntegrityStamp = index.IntegrityStamp
		}
	} else if !identityMatches {
		// Metadata-only changes do not alter indexed content, but must be durable
		// so a restart can validate the authoritative transcript identity.
		_ = appendTurnIndexJournal(basePath+".journal", previousIndex, &index, &stats)
		stored.IntegrityStamp = index.IntegrityStamp
	}
	c.mu.Lock()
	entry = c.entries[path]
	entry.turnIndex = &stored
	c.entries[path] = entry
	c.mu.Unlock()
	return index, stats, nil
}

func validIndexedItemCounts(index turnIndexDisk) bool {
	for i := 0; i < index.recordCount(); i++ {
		record := index.recordAt(i)
		if record.Visible != (record.ItemCount > 0) {
			return false
		}
	}
	return true
}

func usableTurnIndex(file *os.File, size int64, maxLineBytes int, projectionID string, candidate *turnIndexDisk, identityMatches bool, appendOnly bool, trustedMemory bool, stats *ReadStats) (turnIndexDisk, int64, int64) {
	if candidate == nil || candidate.Version != turnIndexVersion || candidate.TranscriptFormatVersion != transcript.FormatVersion || candidate.MaxLineBytes != maxLineBytes || candidate.ProjectionID != projectionID {
		return turnIndexDisk{}, -1, 0
	}
	if !trustedMemory && !candidate.journalApplied && (candidate.IntegrityStamp == "" || candidate.IntegrityStamp != turnIndexIntegrityStamp(*candidate)) {
		return turnIndexDisk{}, -1, 0
	}
	if candidate.TranscriptSize > size || candidate.CompleteSize < 0 || candidate.CompleteSize > candidate.TranscriptSize {
		return turnIndexDisk{}, -1, 0
	}
	validatedBytes := int64(0)
	if appendOnly {
		if !anchorsMatchObserved(file, stats, candidate.FirstAnchor, candidate.TailAnchor) {
			return turnIndexDisk{}, -1, validatedBytes
		}
	}
	if !identityMatches && !appendOnly {
		stamp, readBytes := prefixStamp(file, candidate.CompleteSize)
		validatedBytes = readBytes
		if candidate.PrefixStamp == "" || candidate.PrefixStamp != stamp {
			return turnIndexDisk{}, -1, validatedBytes
		}
	}
	if trustedMemory {
		return *candidate, candidate.CompleteSize, validatedBytes
	}
	previousEnd := int64(0)
	previousIndex := 0
	visibleRecords := 0
	toolNames := map[string]string{}
	for i := 0; i < candidate.recordCount(); i++ {
		record := candidate.recordAt(i)
		if record.Offset < previousEnd || record.Length <= 0 || record.Offset > candidate.CompleteSize-record.Length || record.Index != previousIndex+1 {
			return turnIndexDisk{}, -1, validatedBytes
		}
		if record.Kind != "entry" {
			return turnIndexDisk{}, -1, validatedBytes
		}
		if !validToolSeed(record.ToolSeed, record.ToolChanges, toolNames) {
			return turnIndexDisk{}, -1, validatedBytes
		}
		if record.Visible {
			visibleRecords++
		}
		if record.VisibleIndex != visibleRecords {
			return turnIndexDisk{}, -1, validatedBytes
		}
		applyToolNameChanges(toolNames, record.ToolChanges)
		previousEnd = record.Offset + record.Length
		previousIndex = record.Index
	}
	if visibleRecords != candidate.VisibleRecords {
		return turnIndexDisk{}, -1, validatedBytes
	}
	candidate.baseVisible = visibleRecordOrdinals(candidate.Records)
	return *candidate, candidate.CompleteSize, validatedBytes
}

func scanTurnIndex(file *os.File, transcriptSize int64, start int64, maxLineBytes int, index *turnIndexDisk, projectNames map[string]string, project BoundedEntryProjector) (int64, error) {
	if start >= transcriptSize {
		if err := transcript.ValidateHeader(index.Header); err != nil {
			return 0, err
		}
		return 0, nil
	}
	section := io.NewSectionReader(file, start, transcriptSize-start)
	reader := bufio.NewReader(section)
	offset := start
	entryIndex := 0
	if n := index.recordCount(); n > 0 {
		entryIndex = index.recordAt(n - 1).Index
	}
	// openCalls is the open logical group's accumulated call-id set and
	// openTurnID its turn id. openCalls is nil until a continuation first
	// needs it (then reconstructed from the indexed prefix) and both reset
	// at every group start.
	var openCalls map[string]bool
	var openTurnID string
	var readBytes int64
	visibleRecords := index.VisibleRecords
	var appended []indexedTurn
	headerRead := index.Header.Kind != ""
	if headerRead {
		if err := transcript.ValidateHeader(index.Header); err != nil {
			return 0, err
		}
	}
	for {
		line, complete, lineBytes, err := readTurnIndexLine(reader, maxLineBytes)
		readBytes += lineBytes
		if err != nil {
			return readBytes, err
		}
		if !complete {
			// A final line is not durable transcript data until its newline is
			// appended. CompleteSize intentionally remains before that tail.
			break
		}
		length := lineBytes
		trimmed := bytes.TrimSpace(line)
		framedLine := make([]byte, len(line)+1)
		copy(framedLine, line)
		framedLine[len(line)] = '\n'
		if len(trimmed) == 0 {
			offset += length
			index.CompleteSize = offset
			index.PrefixStamp = extendPrefixStamp(index.PrefixStamp, framedLine)
			continue
		}
		if !headerRead {
			var err error
			index.Header, err = transcript.DecodeHeader(trimmed)
			if err != nil {
				return readBytes, fmt.Errorf("parse transcript header: %w", err)
			}
			index.TranscriptFormatVersion = transcript.FormatVersion
			headerRead = true
		} else {
			entry, err := transcript.DecodeEntry(trimmed)
			if err != nil {
				return readBytes, fmt.Errorf("parse transcript entry: %w", err)
			}
			entryIndex++
			record := indexedTurn{Offset: offset, Length: length, Index: entryIndex, Kind: entry.Kind, TurnKind: entry.Turn.Kind}
			record.ToolSeed, record.ToolChanges = toolProjectionState(entry, projectNames)
			// Logical-group bookkeeping runs BEFORE projection: the entry is
			// projected under its group's turn id (the opener's), exactly
			// the way the range reader names it, so the index scan and the
			// projection cannot disagree (kata: one name per entry).
			record.TurnID = persistedTurnID(entry.Turn, entryIndex)
			prevKind := schema.TurnKind("")
			if len(appended) > 0 {
				prevKind = appended[len(appended)-1].TurnKind
			} else if n := index.recordCount(); n > 0 {
				prevKind = index.recordAt(n - 1).TurnKind
			}
			if recordStartsGroup(entry.Turn.Kind, prevKind) {
				openTurnID = record.TurnID
				openCalls = map[string]bool{}
			} else if openCalls == nil || openTurnID == "" {
				// Continues a group whose opener lives in the previously
				// indexed prefix: reconstruct its id and accumulated calls.
				openTurnID, openCalls = openGroupState(*index)
			}
			var projectedItems []appwire.ThreadItem
			if project != nil {
				recordNames := cloneToolNames(record.ToolSeed)
				projectedItems = project(entry.Turn, openTurnID, entryIndex, recordNames)
				if uint64(len(projectedItems)) > uint64(^uint32(0)) {
					return readBytes, fmt.Errorf("projected item count for entry %d exceeds uint32", entryIndex)
				}
			}
			applyToolNameChanges(projectNames, record.ToolChanges)
			record.ItemCount = uint32(len(projectedItems))
			contribution, introduced := mergedContribution(projectedItems, openCalls)
			record.GroupItems = uint32(contribution)
			record.GroupCalls = introduced
			record.Visible = record.ItemCount > 0
			if record.Visible {
				visibleRecords++
			}
			record.VisibleIndex = visibleRecords
			appended = append(appended, record)
		}
		offset += length
		index.CompleteSize = offset
		index.PrefixStamp = extendPrefixStamp(index.PrefixStamp, framedLine)
	}
	if index.recordCount() == 0 {
		index.Records = appended
	} else {
		index.deltaRoot = joinRecordNodes(index.deltaRoot, newRecordLeaf(appended))
	}
	if index.baseVisible == nil {
		index.baseVisible = visibleRecordOrdinals(index.Records)
	}
	index.VisibleRecords = visibleRecords
	index.FirstAnchor, index.TailAnchor = transcriptAnchors(file, index.CompleteSize)
	if !headerRead {
		return readBytes, fmt.Errorf("%w: missing transcript header", transcript.ErrUnsupportedFormat)
	}
	return readBytes, nil
}

// readTurnIndexLine reads one newline-terminated transcript record while
// retaining at most maxLineBytes plus the delimiter. If the final crash tail is
// unterminated, it is consumed and discarded even when it is arbitrarily large;
// complete oversized records still fail the line-size contract.
func readTurnIndexLine(reader *bufio.Reader, maxLineBytes int) (line []byte, complete bool, bytesRead int64, err error) {
	return transcript.ReadLine(reader, maxLineBytes)
}

func toolProjectionState(entry transcript.Entry, names map[string]string) (map[string]string, []toolNameChange) {
	switch entry.Turn.Kind {
	case schema.TurnAssistant:
		var changes []toolNameChange
		for _, part := range entry.Turn.Message.Content {
			if part.Kind != llm.ContentToolCall || part.ToolCall == nil {
				continue
			}
			changes = append(changes, toolNameChange{ID: part.ToolCall.ID, Name: part.ToolCall.Name})
		}
		return nil, changes
	case schema.TurnTool, schema.TurnToolResults:
	default:
		return nil, nil
	}
	var seed map[string]string
	var changes []toolNameChange
	localNames := map[string]string{}
	localDeleted := map[string]bool{}
	touched := map[string]bool{}
	for _, part := range entry.Turn.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		name := part.ToolResult.Name
		if name == "" {
			id := part.ToolResult.ToolCallID
			if touched[id] {
				if !localDeleted[id] {
					name = localNames[id]
				}
			} else {
				name = names[id]
				if seed == nil {
					seed = map[string]string{}
				}
				// Assignment deliberately preserves an explicitly empty lookup.
				seed[id] = name
			}
			changes = append(changes, toolNameChange{ID: id, Name: name, Lookup: true})
		}
		if name == "communicate" {
			changes = append(changes, toolNameChange{ID: part.ToolResult.ToolCallID, Delete: true})
			delete(localNames, part.ToolResult.ToolCallID)
			localDeleted[part.ToolResult.ToolCallID] = true
			touched[part.ToolResult.ToolCallID] = true
		}
	}
	return seed, changes
}

func applyToolNameChanges(names map[string]string, changes []toolNameChange) {
	for _, change := range changes {
		if change.Lookup {
			continue
		} else if change.Delete {
			delete(names, change.ID)
		} else {
			names[change.ID] = change.Name
		}
	}
}

func replayToolResolver(index turnIndexDisk, stats *ReadStats) map[string]string {
	names := map[string]string{}
	for i := 0; i < index.recordCount(); i++ {
		if stats != nil {
			stats.resolverHistoryVisits++
		}
		applyToolNameChanges(names, index.recordAt(i).ToolChanges)
	}
	return names
}

func validToolSeed(seed map[string]string, changes []toolNameChange, names map[string]string) bool {
	localNames := cloneToolNames(seed)
	required := map[string]string{}
	for _, change := range changes {
		switch {
		case change.Lookup:
			if _, ok := required[change.ID]; !ok {
				required[change.ID] = names[change.ID]
			}
			if localNames[change.ID] != change.Name {
				return false
			}
		case change.Delete:
			delete(localNames, change.ID)
		default:
			localNames[change.ID] = change.Name
		}
	}
	return equalToolNames(seed, required)
}

func transcriptAnchors(file *os.File, completeSize int64) (turnIndexAnchor, turnIndexAnchor) {
	if completeSize <= 0 {
		return turnIndexAnchor{}, turnIndexAnchor{}
	}
	firstLength := min(int64(turnIndexAnchorBytes), completeSize)
	first := anchorAt(file, 0, int(firstLength))
	tailOffset := max(completeSize-int64(turnIndexAnchorBytes), 0)
	tail := anchorAt(file, tailOffset, int(completeSize-tailOffset))
	return first, tail
}

func anchorAt(file *os.File, offset int64, length int) turnIndexAnchor {
	data := make([]byte, length)
	n, _ := file.ReadAt(data, offset)
	sum := sha256.Sum256(data[:n])
	return turnIndexAnchor{Offset: offset, Length: n, Stamp: hex.EncodeToString(sum[:])}
}

func anchorsMatchObserved(file *os.File, stats *ReadStats, anchors ...turnIndexAnchor) bool {
	for _, anchor := range anchors {
		if anchor.Length <= 0 || anchor.Stamp == "" {
			return false
		}
		data := make([]byte, anchor.Length)
		n, err := file.ReadAt(data, anchor.Offset)
		if stats != nil {
			stats.anchorBytesRead += int64(n)
		}
		if err != nil && err != io.EOF {
			return false
		}
		sum := sha256.Sum256(data[:n])
		if n != anchor.Length || hex.EncodeToString(sum[:]) != anchor.Stamp {
			return false
		}
	}
	return true
}

func projectIndexedRange(path string, index turnIndexDisk, lo int, hi int, project BoundedEntryProjector) ([]appwire.Turn, int) {
	turns, projected, _ := projectIndexedRangeObserved(path, index, lo, hi, project, nil)
	return turns, projected
}

func projectIndexedRangeObserved(path string, index turnIndexDisk, lo int, hi int, project BoundedEntryProjector, stats *ReadStats) ([]appwire.Turn, int, error) {
	if lo >= hi {
		return nil, 0, nil
	}
	// Project the visible groups [lo, hi): each group is one turn. The
	// prelude, when present, is visible slot 0. Groups whose merged items
	// are empty emit no turn, so a group's visible slot — and its entry
	// ordinal — are only consumed by groups that project.
	turns := []appwire.Turn{}
	projected := 0
	prelude := PreludeTurn(index.Header)
	slot := 0
	if prelude != nil {
		if lo == 0 {
			positioned, err := positionPreludeItems(prelude.Items)
			if err != nil {
				return nil, projected, err
			}
			prelude.Items = positioned
			turns = append(turns, *prelude)
			projected++
		}
		slot = 1
	}
	entryOrdinal := uint64(0)
	if prelude != nil {
		entryOrdinal = 1
	}
	n := index.recordCount()
	for i := 0; i < n; {
		// Walk the record list group by group without materializing all
		// groups: find where this group's span ends, then decide whether to
		// project it.
		spanEnd := i + 1
		for spanEnd < n && !recordStartsGroup(index.recordAt(spanEnd).TurnKind, index.recordAt(spanEnd-1).TurnKind) {
			spanEnd++
		}
		groupItems := uint64(0)
		for r := i; r < spanEnd; r++ {
			groupItems += uint64(index.recordAt(r).GroupItems)
		}
		if groupItems == 0 {
			i = spanEnd
			continue
		}
		thisSlot := slot
		slot++
		entryOrdinalAt := entryOrdinal
		entryOrdinal++
		groupStart := i
		i = spanEnd
		if thisSlot >= hi {
			// Past the projected window: no later group can be inside it.
			break
		}
		if thisSlot < lo {
			continue
		}
		group := indexedGroup{id: thisSlot, start: groupStart, end: spanEnd, turnID: index.recordAt(groupStart).TurnID, openerIndex: index.recordAt(groupStart).Index, items: groupItems, calls: nil, open: false}
		turn, projectedGroup, err := projectIndexedGroup(path, index, &group, entryOrdinalAt, project)
		if err != nil {
			return nil, projected, err
		}
		projected += projectedGroup
		if turn == nil {
			continue
		}
		turns = append(turns, *turn)
	}
	return turns, projected, nil
}

// projectIndexedGroup materializes one logical group's turn: it reads every
// record of the group's span, projects each through the BoundedEntryProjector
// under the group's turn id, merges the items by call id, positions them at
// the group's entry ordinal, and stamps failure/usage/timestamp across the
// group's entries. It returns nil (not an error) when the group projects to
// no items — the indexed metadata said otherwise, so the caller invalidates.
func projectIndexedGroup(path string, index turnIndexDisk, group *indexedGroup, entryOrdinal uint64, project BoundedEntryProjector) (*appwire.Turn, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close() //nolint:errcheck // read-only file; close errors are not actionable
	var items []appwire.ThreadItem
	var entries []schema.Turn
	projected := 0
	for i := group.start; i < group.end; i++ {
		record := index.recordAt(i)
		raw := make([]byte, record.Length)
		if _, err := file.ReadAt(raw, record.Offset); err != nil {
			return nil, projected, fmt.Errorf("read transcript entry: %w", err)
		}
		entry, err := transcript.DecodeEntry(raw)
		if err != nil {
			return nil, projected, fmt.Errorf("parse transcript entry: %w", err)
		}
		projected++
		entries = append(entries, entry.Turn)
		if project == nil {
			continue
		}
		projectedItems := project(entry.Turn, group.turnID, record.Index, cloneToolNames(record.ToolSeed))
		items = append(items, projectedItems...)
	}
	merged := mergeGroupedItems(items)
	if len(merged) == 0 {
		return nil, projected, nil
	}
	for j := range merged {
		merged[j].TurnID = group.turnID
	}
	positioned, err := positionProjectedItemsAt(merged, group.turnID, entryOrdinal)
	if err != nil {
		return nil, projected, err
	}
	turn := appwire.Turn{ID: group.turnID, Items: positioned, ItemsView: "full", Status: appwire.TurnStatusCompleted}
	stampGroupedTurnFromEntries(&turn, entries)
	return &turn, projected, nil
}

func readTurnIndex(path string) (turnIndexDisk, error) {
	transcriptPath := strings.TrimSuffix(path, ".appwire-index.json")
	info, err := os.Stat(transcriptPath)
	if err != nil {
		return turnIndexDisk{}, err
	}
	return readTurnIndexBounded(path, info.Size())
}

func readTurnIndexBounded(path string, transcriptSize int64) (turnIndexDisk, error) {
	data, err := readFileBounded(path, turnIndexSidecarLimit(transcriptSize))
	if err != nil {
		return turnIndexDisk{}, err
	}
	var index turnIndexDisk
	if err := json.Unmarshal(data, &index); err != nil {
		return turnIndexDisk{}, err
	}
	return index, nil
}

func readTurnIndexWithJournal(path string, transcriptSize int64) (turnIndexDisk, error) {
	index, err := readTurnIndexBounded(path, transcriptSize)
	if err != nil {
		return turnIndexDisk{}, err
	}
	if index.IntegrityStamp == "" || index.IntegrityStamp != turnIndexIntegrityStamp(index) {
		return turnIndexDisk{}, errors.New("invalid base index integrity")
	}
	journal, err := os.Open(path + ".journal")
	if err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return turnIndexDisk{}, err
	}
	defer journal.Close() //nolint:errcheck // read-only file
	journalInfo, err := journal.Stat()
	if err != nil {
		return turnIndexDisk{}, err
	}
	journalLimit := turnIndexJournalLimit(transcriptSize)
	if journalInfo.Size() > journalLimit {
		return turnIndexDisk{}, errors.New("index journal exceeds transcript-derived limit")
	}

	reader := bufio.NewReader(journal)
	frameLimit := turnIndexSidecarLimit(transcriptSize)
	validBytes := int64(0)
	readBytes := int64(0)
	for {
		line, readErr := readBoundedJournalFrame(reader, frameLimit)
		if int64(len(line)) > journalLimit-readBytes {
			return turnIndexDisk{}, errors.New("index journal grew beyond transcript-derived limit")
		}
		readBytes += int64(len(line))
		if errors.Is(readErr, io.EOF) {
			if len(line) > 0 {
				index.journalNeedsRepair = true
			}
			break
		}
		if readErr != nil {
			return turnIndexDisk{}, readErr
		}
		var frame turnIndexJournalFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return turnIndexDisk{}, fmt.Errorf("decode index journal: %w", err)
		}
		if frame.Version != turnIndexJournalVersion || frame.PreviousStamp != index.IntegrityStamp ||
			frame.Incarnation != index.Incarnation ||
			frame.IntegrityStamp == "" || frame.IntegrityStamp != turnIndexJournalStamp(frame) {
			return turnIndexDisk{}, errors.New("invalid index journal integrity chain")
		}
		if frame.TranscriptSize < index.TranscriptSize || frame.CompleteSize < index.CompleteSize ||
			frame.TranscriptFormatVersion != transcript.FormatVersion || frame.MaxLineBytes != index.MaxLineBytes || frame.ProjectionID != index.ProjectionID {
			return turnIndexDisk{}, errors.New("invalid index journal state transition")
		}
		index.deltaRoot = joinRecordNodes(index.deltaRoot, newRecordLeaf(frame.Records))
		index.TranscriptSize = frame.TranscriptSize
		index.CompleteSize = frame.CompleteSize
		index.Header = frame.Header
		index.TranscriptFormatVersion = frame.TranscriptFormatVersion
		index.VisibleRecords = frame.VisibleRecords
		index.PrefixStamp = frame.PrefixStamp
		index.FileIdentity = frame.FileIdentity
		index.ChangeIdentity = frame.ChangeIdentity
		index.ModTimeUnixNS = frame.ModTimeUnixNS
		index.IntegrityStamp = frame.IntegrityStamp
		index.FirstAnchor = frame.FirstAnchor
		index.TailAnchor = frame.TailAnchor
		index.Incarnation = frame.Incarnation
		index.journalApplied = true
		validBytes += int64(len(line))
	}
	index.journalValidBytes = validBytes
	return index, nil
}

func turnIndexSidecarLimit(transcriptSize int64) int64 {
	if transcriptSize < 0 {
		transcriptSize = 0
	}
	maxInt64 := int64(^uint64(0) >> 1)
	maxLimit := maxInt64 - 1 // leave room for the LimitReader sentinel byte
	if transcriptSize > (maxLimit-turnIndexSidecarAllowance)/turnIndexSidecarRatio {
		return maxLimit
	}
	return turnIndexSidecarAllowance + turnIndexSidecarRatio*transcriptSize
}

func turnIndexJournalLimit(transcriptSize int64) int64 {
	limit := turnIndexSidecarLimit(transcriptSize)
	maxInt64 := int64(^uint64(0) >> 1)
	maxLimit := maxInt64 - 1
	if limit > maxLimit/2 {
		return maxLimit
	}
	// A journal may have per-append framing overhead absent from the compact base,
	// but its record payload is still derived from the same authoritative bytes.
	return 2 * limit
}

func readFileBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read-only file
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, errors.New("index sidecar exceeds transcript-derived limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("index sidecar grew beyond transcript-derived limit")
	}
	return data, nil
}

func readBoundedJournalFrame(reader *bufio.Reader, limit int64) ([]byte, error) {
	var frame []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if int64(len(fragment)) > limit-int64(len(frame)) {
			return nil, errors.New("index journal frame exceeds transcript-derived limit")
		}
		frame = append(frame, fragment...)
		switch {
		case err == nil:
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return frame, io.EOF
		default:
			return nil, err
		}
	}
}

func appendTurnIndexJournal(path string, previous turnIndexDisk, index *turnIndexDisk, stats *ReadStats) error {
	previousCount := previous.recordCount()
	if previousCount > index.recordCount() {
		return errors.New("index journal record count regressed")
	}
	records := make([]indexedTurn, index.recordCount()-previousCount)
	for i := range records {
		records[i] = index.recordAt(previousCount + i)
	}
	frame := turnIndexJournalFrame{
		Version:                 turnIndexJournalVersion,
		PreviousStamp:           previous.IntegrityStamp,
		TranscriptFormatVersion: index.TranscriptFormatVersion,
		TranscriptSize:          index.TranscriptSize,
		CompleteSize:            index.CompleteSize,
		Header:                  index.Header,
		Records:                 records,
		VisibleRecords:          index.VisibleRecords,
		MaxLineBytes:            index.MaxLineBytes,
		ProjectionID:            index.ProjectionID,
		PrefixStamp:             index.PrefixStamp,
		FileIdentity:            index.FileIdentity,
		ChangeIdentity:          index.ChangeIdentity,
		ModTimeUnixNS:           index.ModTimeUnixNS,
		FirstAnchor:             index.FirstAnchor,
		TailAnchor:              index.TailAnchor,
		Incarnation:             index.Incarnation,
	}
	if stats != nil {
		stats.journalRecords += int64(len(records))
	}
	frame.IntegrityStamp = turnIndexJournalStampObserved(frame, stats)
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if stats != nil {
		stats.indexBytesSerialized += int64(len(data))
	}
	if index.journalNeedsRepair {
		if err := os.Truncate(path, index.journalValidBytes); err != nil {
			return err
		}
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if index.journalNeedsRepair {
		flags = os.O_WRONLY | os.O_APPEND
	}
	journal, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := journal.Write(data); err != nil {
		_ = journal.Close()
		return err
	}
	if stats != nil {
		stats.indexBytesPersisted += int64(len(data))
	}
	if err := journal.Sync(); err != nil {
		_ = journal.Close()
		return err
	}
	if err := journal.Close(); err != nil {
		return err
	}
	index.IntegrityStamp = frame.IntegrityStamp
	index.journalApplied = true
	index.journalNeedsRepair = false
	return nil
}

func turnIndexJournalStamp(frame turnIndexJournalFrame) string {
	return turnIndexJournalStampObserved(frame, nil)
}

func turnIndexJournalStampObserved(frame turnIndexJournalFrame, stats *ReadStats) string {
	frame.IntegrityStamp = ""
	data, err := json.Marshal(frame)
	if err != nil {
		return ""
	}
	if stats != nil {
		stats.indexBytesSerialized += int64(len(data))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeTurnIndex(path string, index turnIndexDisk, stats *ReadStats) error {
	index.IntegrityStamp = turnIndexIntegrityStampObserved(index, stats)
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if stats != nil {
		stats.indexBytesSerialized += int64(len(data))
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".appwire-index-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath) //nolint:errcheck // best-effort cleanup after rename/failure
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if stats != nil {
		stats.indexBytesPersisted += int64(len(data))
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	// A rebuilt base supersedes every old journal frame. Failure to remove the
	// disposable journal is non-fatal; the next reader rejects it and rebuilds.
	_ = os.Remove(path + ".journal")
	return nil
}

func initialPrefixStamp() string {
	sum := sha256.Sum256([]byte("evener-apptranscript-prefix-v1"))
	return hex.EncodeToString(sum[:])
}

func newTurnIndexIncarnation() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate transcript index incarnation: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func extendPrefixStamp(stamp string, line []byte) string {
	previous, err := hex.DecodeString(stamp)
	if err != nil || len(previous) != sha256.Size {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write(previous)
	_, _ = hash.Write(line)
	return hex.EncodeToString(hash.Sum(nil))
}

func prefixStamp(file *os.File, completeSize int64) (string, int64) {
	if completeSize < 0 {
		return "", 0
	}
	reader := bufio.NewReader(io.NewSectionReader(file, 0, completeSize))
	stamp := initialPrefixStamp()
	var readBytes int64
	for readBytes < completeSize {
		line, err := reader.ReadBytes('\n')
		readBytes += int64(len(line))
		if err != nil {
			return "", readBytes
		}
		stamp = extendPrefixStamp(stamp, line)
	}
	return stamp, readBytes
}

func turnIndexIntegrityStamp(index turnIndexDisk) string {
	return turnIndexIntegrityStampObserved(index, nil)
}

func turnIndexIntegrityStampObserved(index turnIndexDisk, stats *ReadStats) string {
	index.IntegrityStamp = ""
	data, err := json.Marshal(index)
	if err != nil {
		return ""
	}
	if stats != nil {
		stats.indexBytesSerialized += int64(len(data))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileIdentity(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	field := func(name string) (uint64, bool) {
		got := value.FieldByName(name)
		if !got.IsValid() {
			return 0, false
		}
		switch got.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return uint64(got.Int()), true
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return got.Uint(), true
		default:
			return 0, false
		}
	}
	if device, ok := field("Dev"); ok {
		if inode, ok := field("Ino"); ok {
			return fmt.Sprintf("dev:%d:ino:%d", device, inode)
		}
	}
	volume, volumeOK := field("VolumeSerialNumber")
	high, highOK := field("FileIndexHigh")
	low, lowOK := field("FileIndexLow")
	if !volumeOK {
		volume, volumeOK = field("vol")
	}
	if !highOK {
		high, highOK = field("idxhi")
	}
	if !lowOK {
		low, lowOK = field("idxlo")
	}
	if volumeOK && highOK && lowOK {
		return fmt.Sprintf("volume:%d:index:%d", volume, high<<32|low)
	}
	return ""
}

func fileChangeIdentity(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range []string{"Ctim", "Ctimespec", "Ctime", "ChangeTime"} {
		if field := value.FieldByName(name); field.IsValid() {
			if identity := reflectedTimeIdentity(field); identity != "" {
				return name + ":" + identity
			}
		}
	}
	high := value.FieldByName("ChangeTimeHigh")
	low := value.FieldByName("ChangeTimeLow")
	if high.IsValid() && low.IsValid() {
		return fmt.Sprintf("ChangeTime:%d:%d", reflectedUint(high), reflectedUint(low))
	}
	return ""
}

func reflectedTimeIdentity(value reflect.Value) string {
	value = reflect.Indirect(value)
	if !value.IsValid() {
		return ""
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10)
	case reflect.Struct:
		for _, fields := range [][2]string{{"Sec", "Nsec"}, {"Tv_sec", "Tv_nsec"}, {"HighDateTime", "LowDateTime"}} {
			first := value.FieldByName(fields[0])
			second := value.FieldByName(fields[1])
			if first.IsValid() && second.IsValid() {
				return fmt.Sprintf("%d:%d", reflectedUint(first), reflectedUint(second))
			}
		}
	}
	return ""
}

func reflectedUint(value reflect.Value) uint64 {
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(value.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	default:
		return 0
	}
}

func projectionIdentity(project BoundedEntryProjector) string {
	name := "<nil>"
	if project != nil {
		if function := runtime.FuncForPC(reflect.ValueOf(project).Pointer()); function != nil {
			name = function.Name()
		} else {
			name = "<unknown>"
		}
	}
	return fmt.Sprintf("turn-index-v%d:%s:%s", turnIndexVersion, itemIndexProjectionID, name)
}

func cloneTurnIndexForAppend(index turnIndexDisk) turnIndexDisk {
	return index
}

func equalToolNames(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for id, name := range a {
		if b[id] != name {
			return false
		}
	}
	return true
}

func cloneToolNames(names map[string]string) map[string]string {
	return cloneToolNamesObserved(names, nil)
}

// openGroupState reconstructs the open logical group's turn id and
// accumulated call-id set at the tail of an indexed prefix. It walks back
// through the group's records (each contributes the ids its merged items
// introduced) until the record that started the group, whose resolved turn id
// names the group.
func openGroupState(index turnIndexDisk) (string, map[string]bool) {
	calls := map[string]bool{}
	n := index.recordCount()
	if n == 0 {
		return "", calls
	}
	turnID := ""
	for i := n - 1; i >= 0; i-- {
		record := index.recordAt(i)
		for _, id := range record.GroupCalls {
			calls[id] = true
		}
		turnID = record.TurnID
		if i == 0 || recordStartsGroup(record.TurnKind, index.recordAt(i-1).TurnKind) {
			break
		}
	}
	if turnID == "" && n > 0 {
		turnID = persistedTurnID(recordAtKindTurn(index, n-1), index.recordAt(n-1).Index)
	}
	return turnID, calls
}

// recordAtKindTurn reconstructs a record's turn kind for the fallback id
// rule when the persisted TurnID is absent.
func recordAtKindTurn(index turnIndexDisk, i int) schema.Turn {
	return schema.Turn{Kind: index.recordAt(i).TurnKind}
}

// cloneGroupCalls copies a record's persisted group-call slice into the set
// form the counting merge uses. The slice is part of the durable sidecar, so
// readers never mutate it in place.
func cloneGroupCalls(calls []string) map[string]bool {
	if len(calls) == 0 {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(calls))
	for _, id := range calls {
		set[id] = true
	}
	return set
}

func cloneToolNamesObserved(names map[string]string, stats *ReadStats) map[string]string {
	clone := make(map[string]string, len(names))
	maps.Copy(clone, names)
	if stats != nil {
		stats.resolverEntriesCopied += int64(len(names))
	}
	return clone
}
