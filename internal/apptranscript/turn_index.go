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
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

const (
	turnIndexVersion    = 1
	turnIndexSampleSize = 4 << 10
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
}

var observeTurnIndexRead func(ReadStats)

type turnIndexDisk struct {
	Version        int                 `json:"version"`
	TranscriptSize int64               `json:"transcript_size"`
	CompleteSize   int64               `json:"complete_size"`
	Header         transcript.Header   `json:"header"`
	FirstCall      *transcript.APICall `json:"first_call,omitempty"`
	Records        []indexedTurn       `json:"records"`
	ToolNames      map[string]string   `json:"tool_names,omitempty"`
	MaxLineBytes   int                 `json:"max_line_bytes"`
	PrefixStamp    string              `json:"prefix_stamp"`
}

type indexedTurn struct {
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
}

// LatestFromFile returns the newest bounded turn window without projecting the
// historical prefix. A non-positive limit preserves the full-read behavior.
func (c *TurnCache) LatestFromFile(path string, maxLineBytes int, limit int, project BoundedEntryProjector) (turns []appwire.Turn, olderCursor string) {
	if limit <= 0 {
		all := c.TurnsFromFile(path, maxLineBytes, fullProjector(project))
		return appwire.WindowTurns(all, limit)
	}
	index, indexedBytes := c.loadTurnIndex(path, maxLineBytes)
	count := index.logicalTurnCount()
	lo := 0
	if count > limit {
		lo = count - limit
		olderCursor = strconv.Itoa(lo)
	}
	turns, projected := projectIndexedRange(path, index, lo, count, project)
	observeIndexRead(ReadStats{IndexedBytes: indexedBytes, ProjectedTurns: projected})
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
	index, indexedBytes := c.loadTurnIndex(path, maxLineBytes)
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
	observeIndexRead(ReadStats{IndexedBytes: indexedBytes, ProjectedTurns: projected})
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
	count := len(d.Records)
	if PreludeTurn(d.Header, d.FirstCall) != nil {
		count++
	}
	return count
}

func (c *TurnCache) loadTurnIndex(path string, maxLineBytes int) (turnIndexDisk, int64) {
	file, err := os.Open(path)
	if err != nil {
		return turnIndexDisk{}, 0
	}
	defer file.Close() //nolint:errcheck // read-only file; close errors are not actionable
	info, err := file.Stat()
	if err != nil {
		return turnIndexDisk{}, 0
	}

	var candidate *turnIndexDisk
	fromCache := false
	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.turnIndex != nil {
		candidate = entry.turnIndex
		fromCache = true
	}
	c.mu.Unlock()
	if candidate == nil {
		if disk, err := readTurnIndex(path + ".appwire-index.json"); err == nil {
			candidate = &disk
		}
	}

	index, start := usableTurnIndex(file, info.Size(), maxLineBytes, candidate)
	if start < 0 {
		index = turnIndexDisk{
			Version:      turnIndexVersion,
			ToolNames:    map[string]string{},
			MaxLineBytes: maxLineBytes,
		}
		start = 0
	} else if fromCache && start == info.Size() {
		c.mu.Lock()
		c.touch(path)
		c.mu.Unlock()
		return index, 0
	}
	index = cloneTurnIndex(index)
	indexedBytes := scanTurnIndex(file, info.Size(), start, maxLineBytes, &index)
	index.TranscriptSize = info.Size()
	index.MaxLineBytes = maxLineBytes
	index.PrefixStamp = prefixStamp(file, index.CompleteSize)

	stored := cloneTurnIndex(index)
	c.mu.Lock()
	entry := c.entries[path]
	if entry.size != info.Size() || !entry.mod.Equal(info.ModTime()) {
		entry.turns = nil
		entry.full = false
	}
	entry.size = info.Size()
	entry.mod = info.ModTime()
	entry.turnIndex = &stored
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()

	if indexedBytes > 0 || candidate == nil {
		_ = writeTurnIndex(path+".appwire-index.json", index)
	}
	return index, indexedBytes
}

func usableTurnIndex(file *os.File, size int64, maxLineBytes int, candidate *turnIndexDisk) (turnIndexDisk, int64) {
	if candidate == nil || candidate.Version != turnIndexVersion || candidate.MaxLineBytes != maxLineBytes {
		return turnIndexDisk{}, -1
	}
	if candidate.TranscriptSize > size || candidate.CompleteSize < 0 || candidate.CompleteSize > candidate.TranscriptSize {
		return turnIndexDisk{}, -1
	}
	if candidate.PrefixStamp == "" || candidate.PrefixStamp != prefixStamp(file, candidate.CompleteSize) {
		return turnIndexDisk{}, -1
	}
	previousEnd := int64(0)
	previousIndex := 0
	for _, record := range candidate.Records {
		if record.Offset < previousEnd || record.Length <= 0 || record.Offset+record.Length > candidate.CompleteSize || record.Index <= previousIndex {
			return turnIndexDisk{}, -1
		}
		previousEnd = record.Offset + record.Length
		previousIndex = record.Index
	}
	return *candidate, candidate.CompleteSize
}

func scanTurnIndex(file *os.File, transcriptSize int64, start int64, maxLineBytes int, index *turnIndexDisk) int64 {
	if start >= transcriptSize {
		return 0
	}
	section := io.NewSectionReader(file, start, transcriptSize-start)
	reader := bufio.NewReader(section)
	offset := start
	entryIndex := 0
	if n := len(index.Records); n > 0 {
		entryIndex = index.Records[n-1].Index
	}
	var readBytes int64
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
				_ = json.Unmarshal(line, &index.Header)
			case "api_call":
				var call transcript.APICall
				if json.Unmarshal(line, &call) == nil {
					if index.FirstCall == nil {
						copy := call
						index.FirstCall = &copy
					}
					if strings.TrimSpace(call.Error) != "" {
						entryIndex++
						index.Records = append(index.Records, indexedTurn{Offset: offset, Length: length, Index: entryIndex, Kind: head.Kind})
					}
				}
			case "entry":
				entryIndex++
				index.Records = append(index.Records, indexedTurn{Offset: offset, Length: length, Index: entryIndex, Kind: head.Kind})
				captureToolNames(line, index.ToolNames)
			}
		}
		offset += length
		index.CompleteSize = offset
	}
	return readBytes
}

func captureToolNames(raw []byte, names map[string]string) {
	var entry transcript.Entry
	if json.Unmarshal(raw, &entry) != nil {
		return
	}
	for _, part := range entry.Turn.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID != "" {
			names[part.ToolCall.ID] = part.ToolCall.Name
		}
	}
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
	toolNames := cloneToolNames(index.ToolNames)
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
	start := lo - recordBase
	if start < 0 {
		start = 0
	}
	end := hi - recordBase
	if end > len(index.Records) {
		end = len(index.Records)
	}
	for _, record := range index.Records[start:end] {
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

func writeTurnIndex(path string, index turnIndexDisk) error {
	data, err := json.Marshal(index)
	if err != nil {
		return err
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
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func prefixStamp(file *os.File, completeSize int64) string {
	if completeSize < 0 {
		return ""
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:", completeSize)
	headSize := completeSize
	if headSize > turnIndexSampleSize {
		headSize = turnIndexSampleSize
	}
	copyRange(hash, file, 0, headSize)
	if completeSize > headSize {
		tailStart := completeSize - turnIndexSampleSize
		if tailStart < headSize {
			tailStart = headSize
		}
		copyRange(hash, file, tailStart, completeSize-tailStart)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func copyRange(dst io.Writer, file *os.File, offset int64, length int64) {
	if length <= 0 {
		return
	}
	_, _ = io.Copy(dst, io.NewSectionReader(file, offset, length))
}

func cloneTurnIndex(index turnIndexDisk) turnIndexDisk {
	index.Records = append([]indexedTurn(nil), index.Records...)
	index.ToolNames = cloneToolNames(index.ToolNames)
	if index.FirstCall != nil {
		copy := *index.FirstCall
		index.FirstCall = &copy
	}
	return index
}

func cloneToolNames(names map[string]string) map[string]string {
	clone := make(map[string]string, len(names))
	for id, name := range names {
		clone[id] = name
	}
	return clone
}
