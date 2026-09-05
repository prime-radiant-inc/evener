package apptranscript

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

const itemIndexProjectionID = "apptranscript-items-v1"

// ItemWindowOptions controls one indexed atomic-item read. Cursor is empty for
// an initial read and is the opaque cursor returned by a prior item window for
// a previous read.
type ItemWindowOptions struct {
	ThreadRef string
	Cursor    string
	Limit     int
}

// LatestItemWindowFromFile returns the newest indexed projected items without
// projecting records outside the selected batch.
func (c *TurnCache) LatestItemWindowFromFile(
	path string,
	maxLineBytes int,
	options ItemWindowOptions,
	project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	return c.LatestItemWindowFromFileContext(context.Background(), path, maxLineBytes, options, project)
}

// LatestItemWindowFromFileContext returns the newest indexed projected items
// while honoring ctx cancellation.
func (c *TurnCache) LatestItemWindowFromFileContext(
	ctx context.Context,
	path string,
	maxLineBytes int,
	options ItemWindowOptions,
	project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	if err := ctx.Err(); err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	if options.Cursor != "" {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, appwire.TranscriptItemCursorStale()
	}
	return c.itemWindowFromFile(ctx, path, maxLineBytes, options, project, false)
}

// PreviousItemWindowFromFile returns the indexed projected items immediately
// before the cursor boundary. The boundary is exclusive and is never clamped.
func (c *TurnCache) PreviousItemWindowFromFile(
	path string,
	maxLineBytes int,
	options ItemWindowOptions,
	project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	return c.PreviousItemWindowFromFileContext(context.Background(), path, maxLineBytes, options, project)
}

// PreviousItemWindowFromFileContext returns the indexed projected items before
// the cursor boundary while honoring ctx cancellation.
func (c *TurnCache) PreviousItemWindowFromFileContext(
	ctx context.Context,
	path string,
	maxLineBytes int,
	options ItemWindowOptions,
	project BoundedEntryProjector,
) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	if err := ctx.Err(); err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	return c.itemWindowFromFile(ctx, path, maxLineBytes, options, project, true)
}

type indexedItemRange struct {
	// group is the range's logical group; for the prelude range it is the
	// zero value with prelude set.
	group   *indexedGroup
	record  indexedTurn
	entry   uint64
	start   uint64
	count   uint64
	lo      uint64
	hi      uint64
	prelude bool
}

func (c *TurnCache) itemWindowFromFile(ctx context.Context, path string, maxLineBytes int, options ItemWindowOptions, project BoundedEntryProjector, previous bool) (appitempaging.TranscriptItemWindow, appitempaging.CursorIdentity, error) {
	if err := ctx.Err(); err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	limit, err := appwire.NormalizeTranscriptItemLimit(options.Limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	index, stats, err := c.loadTurnIndexForItemPaging(ctx, path, maxLineBytes, project)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, appitempaging.CursorIdentity{}, err
	}
	identity := appitempaging.CursorIdentity{
		ThreadRef:         options.ThreadRef,
		Incarnation:       index.Incarnation,
		ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	if options.ThreadRef == "" || identity.Incarnation == "" {
		return appitempaging.TranscriptItemWindow{}, identity, appwire.TranscriptItemCursorStale()
	}

	ranges, total, err := indexedItemRanges(index)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	if previous && options.Cursor == "" {
		return appitempaging.TranscriptItemWindow{}, identity, nil
	}

	end := total
	if previous {
		before, err := appitempaging.DecodeCursor(options.Cursor, identity)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
		end, err = cursorBoundaryRank(ranges, before)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
	}
	start := uint64(0)
	if end > uint64(limit) {
		start = end - uint64(limit)
	}

	selectedRanges := intersectItemRanges(ranges, start, end)
	candidates, projectedRecords, err := projectIndexedItemRangesContext(ctx, path, index, selectedRanges, project)
	if err != nil {
		if !isContextError(err) {
			c.invalidate(path)
		}
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	selected, _, err := appitempaging.SelectCandidates(candidates, nil, limit)
	if err != nil {
		return appitempaging.TranscriptItemWindow{}, identity, err
	}
	stats.ProjectedItems = len(selected)
	stats.ProjectedTurns = projectedRecords
	observeIndexRead(stats)

	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if start > 0 && len(selected) > 0 {
		cursor, err := appitempaging.EncodeCursor(identity, selected[0].Position)
		if err != nil {
			return appitempaging.TranscriptItemWindow{}, identity, err
		}
		window.OlderCursor = cursor
	}
	return window, identity, nil
}

func indexedItemRanges(index turnIndexDisk) ([]indexedItemRange, uint64, error) {
	ranges := make([]indexedItemRange, 0, index.VisibleRecords+1)
	var total uint64
	hasPrelude := false
	if prelude := PreludeTurn(index.Header); prelude != nil {
		count := uint64(len(prelude.Items))
		if count > uint64(math.MaxUint32) {
			return nil, 0, errors.New("prelude item count exceeds uint32")
		}
		ranges = append(ranges, indexedItemRange{start: 0, count: count, prelude: true})
		total = count
		hasPrelude = true
	}
	// One range per logical group (the turn a live snapshot rendered), with
	// the group's MERGED item count and entry ordinal. Entry 0 is the
	// prelude's range; the first group follows it when a prelude exists.
	groups := index.indexedGroups()
	entry := uint64(0)
	if hasPrelude {
		entry = 1
	}
	for gi := range groups {
		group := &groups[gi]
		if group.items == 0 {
			continue
		}
		if ^uint64(0)-total < group.items {
			return nil, 0, errors.New("projected item count overflows uint64")
		}
		ranges = append(ranges, indexedItemRange{group: group, record: index.recordAt(group.start), entry: entry, start: total, count: group.items})
		total += group.items
		entry++
	}
	return ranges, total, nil
}

func cursorBoundaryRank(ranges []indexedItemRange, before appwire.ThreadItemPosition) (uint64, error) {
	for _, itemRange := range ranges {
		if itemRange.prelude {
			if before.Entry != 0 {
				continue
			}
			if uint64(before.Item) >= itemRange.count {
				return 0, appwire.TranscriptItemCursorStale()
			}
			return itemRange.start + uint64(before.Item), nil
		}
		if itemRange.entry != before.Entry {
			continue
		}
		if uint64(before.Item) >= itemRange.count {
			return 0, appwire.TranscriptItemCursorStale()
		}
		return itemRange.start + uint64(before.Item), nil
	}
	return 0, appwire.TranscriptItemCursorStale()
}

func intersectItemRanges(ranges []indexedItemRange, start, end uint64) []indexedItemRange {
	selected := make([]indexedItemRange, 0, len(ranges))
	for _, itemRange := range ranges {
		rangeEnd := itemRange.start + itemRange.count
		if itemRange.start >= end || rangeEnd <= start {
			continue
		}
		itemRange.lo = max(start, itemRange.start) - itemRange.start
		itemRange.hi = min(end, rangeEnd) - itemRange.start
		selected = append(selected, itemRange)
	}
	return selected
}

func projectIndexedItemRanges(path string, index turnIndexDisk, ranges []indexedItemRange, project BoundedEntryProjector) ([]appitempaging.TranscriptItemCandidate, int, error) {
	return projectIndexedItemRangesContext(context.Background(), path, index, ranges, project)
}

func projectIndexedItemRangesContext(ctx context.Context, path string, index turnIndexDisk, ranges []indexedItemRange, project BoundedEntryProjector) ([]appitempaging.TranscriptItemCandidate, int, error) {
	candidates := make([]appitempaging.TranscriptItemCandidate, 0)
	projectedRecords := 0
	var file *os.File
	var err error
	for _, itemRange := range ranges {
		if err := ctx.Err(); err != nil {
			if file != nil {
				_ = file.Close()
			}
			return nil, projectedRecords, err
		}
		if itemRange.prelude {
			turn := PreludeTurn(index.Header)
			if turn == nil {
				continue
			}
			items, err := positionPreludeItems(turn.Items)
			if err != nil {
				return nil, projectedRecords, err
			}
			for itemIndex, item := range items {
				position := *item.Position
				if uint64(itemIndex) < itemRange.lo || uint64(itemIndex) >= itemRange.hi {
					continue
				}
				candidates = append(candidates, appitempaging.TranscriptItemCandidate{
					TurnID:          turn.ID,
					Turn:            *turn,
					Item:            item,
					Position:        position,
					HasEarlierItems: position.Item > 0,
					HasLaterItems:   uint64(itemIndex)+1 < itemRange.count,
				})
			}
			projectedRecords++
			continue
		}
		group := itemRange.group
		if group == nil || group.items == 0 {
			continue
		}
		if file == nil {
			file, err = os.Open(path)
			if err != nil {
				return nil, projectedRecords, fmt.Errorf("open transcript: %w", err)
			}
		}
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, projectedRecords, err
		}
		// Read and project every record of the group's span, then merge by
		// call id — the same shape the full grouped read produces.
		var items []appwire.ThreadItem
		var entries []schema.Turn
		for i := group.start; i < group.end; i++ {
			if err := ctx.Err(); err != nil {
				_ = file.Close()
				return nil, projectedRecords, err
			}
			record := index.recordAt(i)
			raw := make([]byte, record.Length)
			if _, err := file.ReadAt(raw, record.Offset); err != nil {
				_ = file.Close()
				return nil, projectedRecords, fmt.Errorf("read transcript entry: %w", err)
			}
			entry, err := transcript.DecodeEntry(raw)
			if err != nil {
				_ = file.Close()
				return nil, projectedRecords, fmt.Errorf("parse transcript entry: %w", err)
			}
			entries = append(entries, entry.Turn)
			projectedRecords++
			if project != nil {
				projectedItems := project(entry.Turn, group.turnID, record.Index, cloneToolNames(record.ToolSeed))
				items = append(items, projectedItems...)
			}
			if err := ctx.Err(); err != nil {
				_ = file.Close()
				return nil, projectedRecords, err
			}
		}
		merged := mergeGroupedItems(items)
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, projectedRecords, err
		}
		if uint64(len(merged)) != itemRange.count {
			_ = file.Close()
			return nil, projectedRecords, fmt.Errorf("indexed item count for logical group %d changed", group.id)
		}
		for j := range merged {
			merged[j].TurnID = group.turnID
		}
		positioned, err := positionProjectedItemsAt(merged, group.turnID, itemRange.entry)
		if err != nil {
			_ = file.Close()
			return nil, projectedRecords, err
		}
		turn := appwire.Turn{ID: group.turnID, Items: positioned, ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted}
		stampGroupedTurnFromEntries(&turn, entries)
		for itemIndex := range positioned {
			position := *positioned[itemIndex].Position
			if uint64(itemIndex) < itemRange.lo || uint64(itemIndex) >= itemRange.hi {
				continue
			}
			candidate := appitempaging.TranscriptItemCandidate{
				TurnID:          group.turnID,
				Turn:            turn,
				Item:            positioned[itemIndex],
				Position:        position,
				HasEarlierItems: uint64(itemIndex) > 0,
				HasLaterItems:   uint64(itemIndex)+1 < itemRange.count,
			}
			candidates = append(candidates, candidate)
		}
	}
	if file != nil {
		_ = file.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, projectedRecords, err
	}
	return candidates, projectedRecords, nil
}

func positionPreludeItems(items []appwire.ThreadItem) ([]appwire.ThreadItem, error) {
	positioned := make([]appwire.ThreadItem, len(items))
	for i, item := range items {
		if uint64(i) > uint64(math.MaxUint32) {
			return nil, errors.New("prelude item index exceeds uint32")
		}
		position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
		item.Position = &position
		item.TranscriptKey = appitempaging.TranscriptItemKey(appwire.SystemPreludeTurnID, position)
		positioned[i] = item
	}
	return positioned, nil
}
