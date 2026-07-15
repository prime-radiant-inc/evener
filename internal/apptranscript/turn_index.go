package apptranscript

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

const (
	turnIndexVersion        = 3
	turnIndexJournalVersion = 1
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

	// The remaining fields are intentionally unexported test/benchmark
	// instrumentation. Production callers do not depend on them.
	indexBytesCopied     int64
	indexBytesSerialized int64
	indexBytesPersisted  int64
}

var observeTurnIndexRead func(ReadStats)

type turnIndexDisk struct {
	Version        int                 `json:"version"`
	TranscriptSize int64               `json:"transcript_size"`
	CompleteSize   int64               `json:"complete_size"`
	Header         transcript.Header   `json:"header"`
	FirstCall      *transcript.APICall `json:"first_call,omitempty"`
	Records        []indexedTurn       `json:"records"`
	VisibleRecords int                 `json:"visible_records"`
	ToolNames      map[string]string   `json:"tool_names,omitempty"`
	MaxLineBytes   int                 `json:"max_line_bytes"`
	ProjectionID   string              `json:"projection_identity"`
	PrefixStamp    string              `json:"prefix_stamp"`
	FileIdentity   string              `json:"file_identity"`
	ChangeIdentity string              `json:"change_identity"`
	ModTimeUnixNS  int64               `json:"mod_time_unix_ns"`
	IntegrityStamp string              `json:"integrity_stamp"`

	// deltaRoot is an immutable persistent rope of records loaded from or
	// destined for the append-only journal. Keeping suffixes out of Records
	// lets an advancing reader publish a new index without copying or mutating
	// history still visible to concurrent readers.
	deltaRoot          *turnIndexRecordNode `json:"-"`
	journalValidBytes  int64                `json:"-"`
	journalNeedsRepair bool                 `json:"-"`
	journalApplied     bool                 `json:"-"`
}

type turnIndexRecordNode struct {
	left, right *turnIndexRecordNode
	records     []indexedTurn
	count       int
	height      int
}

type turnIndexJournalFrame struct {
	Version        int                 `json:"version"`
	PreviousStamp  string              `json:"previous_stamp"`
	TranscriptSize int64               `json:"transcript_size"`
	CompleteSize   int64               `json:"complete_size"`
	Header         transcript.Header   `json:"header"`
	FirstCall      *transcript.APICall `json:"first_call,omitempty"`
	Records        []indexedTurn       `json:"records"`
	VisibleRecords int                 `json:"visible_records"`
	ToolNames      map[string]string   `json:"tool_names,omitempty"`
	MaxLineBytes   int                 `json:"max_line_bytes"`
	ProjectionID   string              `json:"projection_identity"`
	PrefixStamp    string              `json:"prefix_stamp"`
	FileIdentity   string              `json:"file_identity"`
	ChangeIdentity string              `json:"change_identity"`
	ModTimeUnixNS  int64               `json:"mod_time_unix_ns"`
	IntegrityStamp string              `json:"integrity_stamp"`
}

type indexedTurn struct {
	Offset          int64             `json:"offset"`
	Length          int64             `json:"length"`
	Index           int               `json:"index"`
	Kind            string            `json:"kind"`
	Visible         bool              `json:"visible"`
	VisibleIndex    int               `json:"visible_index,omitempty"`
	ToolNamesBefore map[string]string `json:"tool_names_before,omitempty"`
	ToolChanges     []toolNameChange  `json:"tool_changes,omitempty"`
}

type toolNameChange struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

const toolNameCheckpointInterval = 128

// LatestFromFile returns the newest bounded turn window without projecting the
// historical prefix. A non-positive limit preserves the full-read behavior.
func (c *TurnCache) LatestFromFile(path string, maxLineBytes int, limit int, project BoundedEntryProjector) (turns []appwire.Turn, olderCursor string) {
	if limit <= 0 {
		all := c.TurnsFromFile(path, maxLineBytes, fullProjector(project))
		return appwire.WindowTurns(all, limit)
	}
	index, stats := c.loadTurnIndex(path, maxLineBytes, project)
	count := index.logicalTurnCount()
	lo := 0
	if count > limit {
		lo = count - limit
		olderCursor = strconv.Itoa(lo)
	}
	turns, projected := projectIndexedRange(path, index, lo, count, project)
	stats.ProjectedTurns = projected
	observeIndexRead(stats)
	return turns, olderCursor
}

// PageFromFile returns turns older than cursor without projecting records
// outside that page. A non-positive limit delegates to the legacy full reader.
func (c *TurnCache) PageFromFile(path string, maxLineBytes int, cursor string, limit int, project BoundedEntryProjector) FilePage {
	if limit <= 0 {
		all := c.TurnsFromFile(path, maxLineBytes, fullProjector(project))
		page := appwire.PageTurns(all, cursor, limit)
		return FilePage{Turns: page.Data, NextCursor: page.NextCursor}
	}
	index, stats := c.loadTurnIndex(path, maxLineBytes, project)
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
	lo := hi - limit
	if lo < 0 {
		lo = 0
	}
	next := ""
	if lo > 0 {
		next = strconv.Itoa(lo)
	}
	turns, projected := projectIndexedRange(path, index, lo, hi, project)
	stats.ProjectedTurns = projected
	observeIndexRead(stats)
	return FilePage{Turns: turns, NextCursor: next}
}

func fullProjector(project BoundedEntryProjector) EntryProjector {
	toolNames := map[string]string{}
	return func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		if project == nil {
			return nil
		}
		return project(raw, turnID, turnIndex, toolNames)
	}
}

func observeIndexRead(stats ReadStats) {
	if observeTurnIndexRead != nil {
		observeTurnIndexRead(stats)
	}
}

func (d turnIndexDisk) logicalTurnCount() int {
	count := d.VisibleRecords
	if PreludeTurn(d.Header, d.FirstCall) != nil {
		count++
	}
	return count
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
	return &turnIndexRecordNode{records: records, count: len(records), height: 1}
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
	return &turnIndexRecordNode{left: left, right: right, count: recordNodeCount(left) + recordNodeCount(right), height: height + 1}
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

func (c *TurnCache) loadTurnIndex(path string, maxLineBytes int, project BoundedEntryProjector) (turnIndexDisk, ReadStats) {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()

	var stats ReadStats
	file, err := os.Open(path)
	if err != nil {
		return turnIndexDisk{}, stats
	}
	defer file.Close() //nolint:errcheck // read-only file; close errors are not actionable
	info, err := file.Stat()
	if err != nil {
		return turnIndexDisk{}, stats
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
		if disk, err := readTurnIndexWithJournal(path + ".appwire-index.json"); err == nil {
			candidate = &disk
		}
	}

	if candidate != nil && !fromCache {
		identityMatches = candidate.TranscriptSize == info.Size() && candidate.ModTimeUnixNS == info.ModTime().UnixNano() &&
			candidate.FileIdentity == currentFileIdentity && candidate.ChangeIdentity == currentChangeIdentity
	}
	sameFile := candidate != nil && currentFileIdentity != "" && candidate.FileIdentity == currentFileIdentity
	appendOnly := sameFile && candidate.TranscriptSize < info.Size()
	index, start, validatedBytes := usableTurnIndex(file, info.Size(), maxLineBytes, projectionID, candidate, identityMatches, appendOnly, fromCache)
	rebuilt := start < 0
	if start < 0 {
		index = turnIndexDisk{
			Version:      turnIndexVersion,
			ToolNames:    map[string]string{},
			MaxLineBytes: maxLineBytes,
			ProjectionID: projectionID,
			PrefixStamp:  initialPrefixStamp(),
		}
		start = 0
	} else if fromCache && start == info.Size() {
		c.mu.Lock()
		c.touch(path)
		c.mu.Unlock()
		return index, stats
	}
	stats.IndexedBytes = validatedBytes
	previousIndex := index
	index = cloneTurnIndexForAppend(index)
	indexedBytes := scanTurnIndex(file, info.Size(), start, maxLineBytes, &index, project)
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
	return index, stats
}

func usableTurnIndex(file *os.File, size int64, maxLineBytes int, projectionID string, candidate *turnIndexDisk, identityMatches bool, appendOnly bool, trustedMemory bool) (turnIndexDisk, int64, int64) {
	if candidate == nil || candidate.Version != turnIndexVersion || candidate.MaxLineBytes != maxLineBytes || candidate.ProjectionID != projectionID {
		return turnIndexDisk{}, -1, 0
	}
	if !trustedMemory && !candidate.journalApplied && (candidate.IntegrityStamp == "" || candidate.IntegrityStamp != turnIndexIntegrityStamp(*candidate)) {
		return turnIndexDisk{}, -1, 0
	}
	if candidate.TranscriptSize > size || candidate.CompleteSize < 0 || candidate.CompleteSize > candidate.TranscriptSize {
		return turnIndexDisk{}, -1, 0
	}
	validatedBytes := int64(0)
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
		if record.Kind != "entry" && record.Kind != "api_call" {
			return turnIndexDisk{}, -1, validatedBytes
		}
		if record.Kind == "api_call" && (!record.Visible || len(record.ToolChanges) > 0 || len(record.ToolNamesBefore) > 0) {
			return turnIndexDisk{}, -1, validatedBytes
		}
		if record.Kind == "entry" && i%toolNameCheckpointInterval == 0 {
			if !equalToolNames(record.ToolNamesBefore, toolNames) {
				return turnIndexDisk{}, -1, validatedBytes
			}
		} else if len(record.ToolNamesBefore) > 0 {
			return turnIndexDisk{}, -1, validatedBytes
		}
		for _, change := range record.ToolChanges {
			if change.ID == "" {
				return turnIndexDisk{}, -1, validatedBytes
			}
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
	if visibleRecords != candidate.VisibleRecords || !equalToolNames(candidate.ToolNames, toolNames) {
		return turnIndexDisk{}, -1, validatedBytes
	}
	return *candidate, candidate.CompleteSize, validatedBytes
}

func scanTurnIndex(file *os.File, transcriptSize int64, start int64, maxLineBytes int, index *turnIndexDisk, project BoundedEntryProjector) int64 {
	if start >= transcriptSize {
		return 0
	}
	section := io.NewSectionReader(file, start, transcriptSize-start)
	reader := bufio.NewReader(section)
	offset := start
	entryIndex := 0
	if n := index.recordCount(); n > 0 {
		entryIndex = index.recordAt(n - 1).Index
	}
	var readBytes int64
	projectNames := cloneToolNames(index.ToolNames)
	visibleRecords := index.VisibleRecords
	var appended []indexedTurn
	for {
		line, err := reader.ReadBytes('\n')
		readBytes += int64(len(line))
		if err != nil {
			// A final line is not durable transcript data until its newline is
			// appended. CompleteSize intentionally remains before that tail.
			break
		}
		if maxLineBytes > 0 && len(bytes.TrimSuffix(line, []byte{'\n'})) > maxLineBytes {
			break
		}
		length := int64(len(line))
		var head struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(line, &head) == nil {
			switch head.Kind {
			case "header":
				if index.FirstCall == nil {
					_ = json.Unmarshal(line, &index.Header)
				}
			case "api_call":
				var call transcript.APICall
				if json.Unmarshal(line, &call) == nil {
					if index.FirstCall == nil {
						copy := call
						index.FirstCall = &copy
					}
					if strings.TrimSpace(call.Error) != "" {
						entryIndex++
						visibleRecords++
						appended = append(appended, indexedTurn{Offset: offset, Length: length, Index: entryIndex, Kind: head.Kind, Visible: true, VisibleIndex: visibleRecords})
					}
				}
			case "entry":
				entryIndex++
				record := indexedTurn{Offset: offset, Length: length, Index: entryIndex, Kind: head.Kind}
				if (index.recordCount()+len(appended))%toolNameCheckpointInterval == 0 {
					record.ToolNamesBefore = cloneToolNames(projectNames)
				}
				record.ToolChanges = toolNameChanges(line, projectNames)
				visible := false
				if project != nil {
					visible = len(project(line, fmt.Sprintf("turn_%d", entryIndex), entryIndex, projectNames)) > 0
				} else {
					applyToolNameChanges(projectNames, record.ToolChanges)
				}
				record.Visible = visible
				if visible {
					visibleRecords++
				}
				record.VisibleIndex = visibleRecords
				appended = append(appended, record)
				index.ToolNames = cloneToolNames(projectNames)
			}
		}
		offset += length
		index.CompleteSize = offset
		index.PrefixStamp = extendPrefixStamp(index.PrefixStamp, line)
	}
	if index.recordCount() == 0 {
		index.Records = appended
	} else {
		index.deltaRoot = joinRecordNodes(index.deltaRoot, newRecordLeaf(appended))
	}
	index.VisibleRecords = visibleRecords
	return readBytes
}

func toolNameChanges(raw []byte, names map[string]string) []toolNameChange {
	var entry transcript.Entry
	if json.Unmarshal(raw, &entry) != nil {
		return nil
	}
	var changes []toolNameChange
	for _, part := range entry.Turn.Message.Content {
		switch {
		case part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID != "":
			changes = append(changes, toolNameChange{ID: part.ToolCall.ID, Name: part.ToolCall.Name})
		case part.Kind == llm.ContentToolResult && part.ToolResult != nil:
			name := part.ToolResult.Name
			if name == "" {
				name = names[part.ToolResult.ToolCallID]
			}
			if name == "communicate" {
				changes = append(changes, toolNameChange{ID: part.ToolResult.ToolCallID, Delete: true})
			}
		}
	}
	return changes
}

func applyToolNameChanges(names map[string]string, changes []toolNameChange) {
	for _, change := range changes {
		if change.Delete {
			delete(names, change.ID)
		} else {
			names[change.ID] = change.Name
		}
	}
}

func toolNamesBeforeRecord(index turnIndexDisk, target int) map[string]string {
	if target <= 0 {
		return map[string]string{}
	}
	start := 0
	names := map[string]string{}
	for i := target; i >= 0; i-- {
		if i < index.recordCount() && index.recordAt(i).ToolNamesBefore != nil {
			start = i
			names = cloneToolNames(index.recordAt(i).ToolNamesBefore)
			break
		}
	}
	for i := start; i < target; i++ {
		applyToolNameChanges(names, index.recordAt(i).ToolChanges)
	}
	return names
}

func projectIndexedRange(path string, index turnIndexDisk, lo int, hi int, project BoundedEntryProjector) ([]appwire.Turn, int) {
	if lo >= hi {
		return nil, 0
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer file.Close() //nolint:errcheck // read-only file; close errors are not actionable
	var turns []appwire.Turn
	projected := 0
	prelude := PreludeTurn(index.Header, index.FirstCall)
	recordBase := 0
	if prelude != nil {
		if lo == 0 && hi > 0 {
			turns = append(turns, *prelude)
			projected++
		}
		recordBase = 1
	}
	recordLogicalLo := lo - recordBase
	if recordLogicalLo < 0 {
		recordLogicalLo = 0
	}
	recordCount := index.recordCount()
	startRecord := sort.Search(recordCount, func(i int) bool {
		return index.recordAt(i).VisibleIndex > recordLogicalLo
	})
	if startRecord == recordCount {
		return turns, projected
	}
	toolNames := toolNamesBeforeRecord(index, startRecord)
	logicalPosition := recordBase + recordLogicalLo
	for i := startRecord; i < recordCount; i++ {
		record := index.recordAt(i)
		if !record.Visible {
			applyToolNameChanges(toolNames, record.ToolChanges)
			continue
		}
		position := logicalPosition
		logicalPosition++
		if position < lo {
			continue
		}
		if position >= hi {
			break
		}
		raw := make([]byte, record.Length)
		if _, err := file.ReadAt(raw, record.Offset); err != nil {
			continue
		}
		projected++
		if record.Kind == "api_call" {
			if turn, ok := failedAPICallTurn(raw, record.Index); ok {
				turns = append(turns, turn)
			}
			continue
		}
		var items []appwire.ThreadItem
		if project != nil {
			items = project(raw, fmt.Sprintf("turn_%d", record.Index), record.Index, toolNames)
		}
		if len(items) == 0 {
			continue
		}
		turn := appwire.Turn{ID: fmt.Sprintf("turn_%d", record.Index), Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
		var entry transcript.Entry
		if json.Unmarshal(raw, &entry) == nil {
			if !entry.Turn.Timestamp.IsZero() {
				startedAt := entry.Turn.Timestamp.Unix()
				turn.StartedAt = &startedAt
			}
			turn.Usage = appwire.SerfUsageFromLLM(entry.Turn.Usage)
		}
		turns = append(turns, turn)
	}
	return turns, projected
}

func failedAPICallTurn(raw []byte, index int) (appwire.Turn, bool) {
	var call transcript.APICall
	if json.Unmarshal(raw, &call) != nil || strings.TrimSpace(call.Error) == "" {
		return appwire.Turn{}, false
	}
	info := diagnostic.FromFields(call.Source, call.Title, call.Hint, call.Error)
	return appwire.Turn{
		ID:        fmt.Sprintf("turn_%d", index),
		ItemsView: "full",
		Status:    appwire.TurnStatusFailed,
		Error: &appwire.TurnError{
			Message: call.Error,
			Source:  string(info.Source),
			Title:   info.Title,
			Hint:    info.Hint,
		},
	}, true
}

func readTurnIndex(path string) (turnIndexDisk, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return turnIndexDisk{}, err
	}
	var index turnIndexDisk
	if err := json.Unmarshal(data, &index); err != nil {
		return turnIndexDisk{}, err
	}
	return index, nil
}

func readTurnIndexWithJournal(path string) (turnIndexDisk, error) {
	index, err := readTurnIndex(path)
	if err != nil {
		return turnIndexDisk{}, err
	}
	if index.IntegrityStamp == "" || index.IntegrityStamp != turnIndexIntegrityStamp(index) {
		return turnIndexDisk{}, fmt.Errorf("invalid base index integrity")
	}
	journal, err := os.Open(path + ".journal")
	if err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return turnIndexDisk{}, err
	}
	defer journal.Close() //nolint:errcheck // read-only file

	reader := bufio.NewReader(journal)
	validBytes := int64(0)
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
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
			frame.IntegrityStamp == "" || frame.IntegrityStamp != turnIndexJournalStamp(frame) {
			return turnIndexDisk{}, fmt.Errorf("invalid index journal integrity chain")
		}
		if frame.TranscriptSize < index.TranscriptSize || frame.CompleteSize < index.CompleteSize ||
			frame.MaxLineBytes != index.MaxLineBytes || frame.ProjectionID != index.ProjectionID {
			return turnIndexDisk{}, fmt.Errorf("invalid index journal state transition")
		}
		index.deltaRoot = joinRecordNodes(index.deltaRoot, newRecordLeaf(frame.Records))
		index.TranscriptSize = frame.TranscriptSize
		index.CompleteSize = frame.CompleteSize
		index.Header = frame.Header
		index.FirstCall = frame.FirstCall
		index.VisibleRecords = frame.VisibleRecords
		index.ToolNames = frame.ToolNames
		index.PrefixStamp = frame.PrefixStamp
		index.FileIdentity = frame.FileIdentity
		index.ChangeIdentity = frame.ChangeIdentity
		index.ModTimeUnixNS = frame.ModTimeUnixNS
		index.IntegrityStamp = frame.IntegrityStamp
		index.journalApplied = true
		validBytes += int64(len(line))
	}
	index.journalValidBytes = validBytes
	return index, nil
}

func appendTurnIndexJournal(path string, previous turnIndexDisk, index *turnIndexDisk, stats *ReadStats) error {
	previousCount := previous.recordCount()
	if previousCount > index.recordCount() {
		return fmt.Errorf("index journal record count regressed")
	}
	records := make([]indexedTurn, index.recordCount()-previousCount)
	for i := range records {
		records[i] = index.recordAt(previousCount + i)
	}
	frame := turnIndexJournalFrame{
		Version:        turnIndexJournalVersion,
		PreviousStamp:  previous.IntegrityStamp,
		TranscriptSize: index.TranscriptSize,
		CompleteSize:   index.CompleteSize,
		Header:         index.Header,
		FirstCall:      index.FirstCall,
		Records:        records,
		VisibleRecords: index.VisibleRecords,
		ToolNames:      index.ToolNames,
		MaxLineBytes:   index.MaxLineBytes,
		ProjectionID:   index.ProjectionID,
		PrefixStamp:    index.PrefixStamp,
		FileIdentity:   index.FileIdentity,
		ChangeIdentity: index.ChangeIdentity,
		ModTimeUnixNS:  index.ModTimeUnixNS,
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
	sum := sha256.Sum256([]byte("serf-apptranscript-prefix-v1"))
	return hex.EncodeToString(sum[:])
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
	return fmt.Sprintf("turn-index-v%d:%s", turnIndexVersion, name)
}

func cloneTurnIndex(index turnIndexDisk) turnIndexDisk {
	return cloneTurnIndexObserved(index, nil)
}

func cloneTurnIndexForAppend(index turnIndexDisk) turnIndexDisk {
	index.ToolNames = cloneToolNames(index.ToolNames)
	if index.FirstCall != nil {
		copy := *index.FirstCall
		index.FirstCall = &copy
	}
	return index
}

func cloneTurnIndexObserved(index turnIndexDisk, stats *ReadStats) turnIndexDisk {
	if stats != nil && observeTurnIndexRead != nil {
		if data, err := json.Marshal(index); err == nil {
			stats.indexBytesCopied += int64(len(data))
		}
	}
	index.Records = append([]indexedTurn(nil), index.Records...)
	for i := range index.Records {
		index.Records[i].ToolNamesBefore = cloneToolNames(index.Records[i].ToolNamesBefore)
		index.Records[i].ToolChanges = append([]toolNameChange(nil), index.Records[i].ToolChanges...)
	}
	index.ToolNames = cloneToolNames(index.ToolNames)
	if index.FirstCall != nil {
		copy := *index.FirstCall
		index.FirstCall = &copy
	}
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
	clone := make(map[string]string, len(names))
	for id, name := range names {
		clone[id] = name
	}
	return clone
}
