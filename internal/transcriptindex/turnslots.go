package transcriptindex

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
)

// The turn-slot table is the sidecar's turn id to turn-table slot index. Turn
// slots are already the table's ordinal, so what a turn-addressed read lacks is
// the id to slot mapping; without one, resolving a boundary id walks every turn
// summary.
//
// The table is an open-addressed array of uint32 buckets. An id's probe starts
// at its hash bucket and walks forward to the first empty bucket. A bucket is
// never cleared, so a probe sequence never loses an entry it once passed, and a
// lookup therefore stops at the first empty bucket. A turn that reopens under
// the same id keeps one bucket naming its latest record, which is the record a
// backwards scan of the turn table would find.
//
// The table is a hint, not an authority: every candidate is confirmed against
// the turn record it names, which must hold the id looked up. A torn bucket
// left by a crash, or one naming a slot a recovered extension truncated away,
// reads as a miss rather than a wrong turn. A miss falls back to reading the
// turn table itself, which is always correct.
const (
	turnSlotsName = "turnslots"
	// turnSlotsEmpty marks a free bucket.
	turnSlotsEmpty = ^uint32(0)
	// turnSlotsMinCapacity is the smallest table a build mints.
	turnSlotsMinCapacity = 64
	// turnSlotsProbeRead is how many buckets one read fetches: a probe
	// sequence usually ends inside one window.
	turnSlotsProbeRead = 8
)

// turnSlotTable is one generation of the table: a fixed-size file of uint32
// buckets named by meta.TurnSlotsFile. Growth mints a new generation and names
// it in the next meta commit; the old file goes once that commit lands, as the
// update log's cut does.
type turnSlotTable struct {
	file     recordFile
	name     string
	capacity uint64
}

// turnSlotHash is FNV-1a over the turn id: stable across processes and builds,
// so a table written by one handle serves another.
func turnSlotHash(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}

// readBuckets returns up to count buckets starting at index, wrapping past the
// table's end, so one probe window covers a sequence either side of the wrap.
func (t *turnSlotTable) readBuckets(index, count uint64) ([]uint32, error) {
	count = min(count, t.capacity)
	buf := make([]byte, count*4)
	first := min(count, t.capacity-index)
	if _, err := t.file.ReadAt(buf[:first*4], int64(index)*4); err != nil {
		return nil, fmt.Errorf("%w: read turn slot buckets: %w", errCorrupt, err)
	}
	if rest := count - first; rest > 0 {
		if _, err := t.file.ReadAt(buf[first*4:], 0); err != nil {
			return nil, fmt.Errorf("%w: read turn slot buckets: %w", errCorrupt, err)
		}
	}
	buckets := make([]uint32, count)
	for i := range buckets {
		buckets[i] = binary.LittleEndian.Uint32(buf[i*4:])
	}
	return buckets, nil
}

func (t *turnSlotTable) writeBucket(index uint64, value uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], value)
	if _, err := t.file.WriteAt(buf[:], int64(index)*4); err != nil {
		return fmt.Errorf("write turn slot bucket: %w", err)
	}
	return nil
}

// scan walks id's probe sequence. visit receives each occupied bucket's value
// and stops the walk by returning true. The walk ends at the first empty
// bucket, so a table whose load factor stays below one always terminates.
func (t *turnSlotTable) scan(id string, visit func(value uint32) (bool, error)) error {
	if t == nil || t.capacity == 0 {
		return nil
	}
	start := turnSlotHash(id) % t.capacity
	for inspected := uint64(0); inspected < t.capacity; {
		buckets, err := t.readBuckets((start+inspected)%t.capacity, turnSlotsProbeRead)
		if err != nil {
			return err
		}
		for _, value := range buckets {
			if value == turnSlotsEmpty {
				return nil
			}
			stop, err := visit(value)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			inspected++
		}
	}
	return nil
}

// turnSlotOf returns the slot of a turn named id, when the table can name one
// the turn table confirms. It otherwise falls back to scanning the turn table.
func (x *Index) turnSlotOf(id string) (uint64, bool, error) {
	var found uint64
	ok := false
	err := x.turnSlots.scan(id, func(value uint32) (bool, error) {
		slot := uint64(value)
		if slot >= x.turns.n {
			return false, nil
		}
		record, err := x.turnRecordAt(slot)
		if err != nil {
			return false, err
		}
		name, err := x.strings.get(record.ID)
		if err != nil {
			return false, err
		}
		if string(name) != id {
			return false, nil
		}
		found, ok = slot, true
		return false, nil
	})
	if err != nil {
		return 0, false, err
	}
	if ok {
		return found, true, nil
	}
	return x.findTurnSlot(id)
}

// findTurnSlot answers turnSlotOf from the turn table alone: the last turn with
// the id wins, which is the record a reopened turn's entries joined. It is the
// fallback for a table that cannot name the id, and the whole answer for a view
// that holds no table.
func (x *Index) findTurnSlot(id string) (uint64, bool, error) {
	const chunk = uint64(256)
	for end := x.turns.n; end > 0; {
		start := end - min(end, chunk)
		buf, err := x.turns.read(start, int(end-start))
		if err != nil {
			return 0, false, err
		}
		for slot := end; slot > start; slot-- {
			record := decodeTurn(buf[(slot-1-start)*turnRecordSize:])
			if int(record.ID.Len) != len(id) {
				continue
			}
			name, err := x.strings.get(record.ID)
			if err != nil {
				return 0, false, err
			}
			if string(name) == id {
				return slot - 1, true, nil
			}
		}
		end = start
	}
	return 0, false, nil
}

// turnRecordAt decodes the turn summary at slot.
func (x *Index) turnRecordAt(slot uint64) (turnRecord, error) {
	buf, err := x.turns.read(slot, 1)
	if err != nil {
		return turnRecord{}, err
	}
	return decodeTurn(buf), nil
}

// recordTurnSlot points id's bucket at slot, replacing the one that named an
// earlier record of the same id, or claiming the first free bucket the probe
// reaches. A full table reports errTurnSlotsFull, and the caller mints a larger
// generation.
func (x *Index) recordTurnSlot(id string, slot uint64) error {
	t := x.turnSlots
	if t == nil || t.capacity == 0 || slot >= uint64(turnSlotsEmpty) {
		return nil
	}
	start := turnSlotHash(id) % t.capacity
	// One bucket index per step, exactly as scan walks: a window read covers
	// turnSlotsProbeRead buckets from the current index, and the next window
	// starts where this one ended.
	for inspected := uint64(0); inspected < t.capacity; {
		buckets, err := t.readBuckets((start+inspected)%t.capacity, min(turnSlotsProbeRead, t.capacity-inspected))
		if err != nil {
			return err
		}
		for _, value := range buckets {
			index := (start + inspected) % t.capacity
			if value == turnSlotsEmpty {
				return t.writeBucket(index, uint32(slot))
			}
			previous := uint64(value)
			if previous < x.turns.n {
				record, err := x.turnRecordAt(previous)
				if err != nil {
					return err
				}
				name, err := x.strings.get(record.ID)
				if err != nil {
					return err
				}
				if string(name) == id {
					return t.writeBucket(index, uint32(slot))
				}
			}
			inspected++
		}
	}
	return errTurnSlotsFull
}

var errTurnSlotsFull = fmt.Errorf("%w: turn slot table is full", errCorrupt)

// buildTurnSlots mints a table naming every recorded turn. A build calls it
// once the records it names are complete.
func (x *Index) buildTurnSlots() error {
	capacity := uint64(turnSlotsMinCapacity)
	for 2*x.turns.n > capacity {
		capacity *= 2
	}
	if err := x.writeTurnSlots(capacity); err != nil {
		return err
	}
	return x.extendTurnSlots(0)
}

// extendTurnSlots names every turn recorded from slot from on.
func (x *Index) extendTurnSlots(from uint64) error {
	if x.turnSlots == nil || x.turns.n <= from {
		return nil
	}
	if 2*x.turns.n > x.turnSlots.capacity {
		return x.buildTurnSlots()
	}
	for slot := from; slot < x.turns.n; slot++ {
		record, err := x.turnRecordAt(slot)
		if err != nil {
			return err
		}
		id, err := x.strings.get(record.ID)
		if err != nil {
			return err
		}
		if err := x.recordTurnSlot(string(id), slot); err != nil {
			return x.buildTurnSlots()
		}
	}
	x.turnSlotsNamed = x.turns.n
	return nil
}

// writeTurnSlots mints a generation of capacity empty buckets, opens it, and
// names it in the meta the next commit publishes.
func (x *Index) writeTurnSlots(capacity uint64) error {
	name, err := randomName()
	if err != nil {
		return err
	}
	name = turnSlotsName + "." + name
	f, err := os.OpenFile(filepath.Join(x.dir, x.build, name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create turn slot table: %w", err)
	}
	empty := make([]byte, 1<<12)
	for i := 0; i < len(empty); i += 4 {
		binary.LittleEndian.PutUint32(empty[i:], turnSlotsEmpty)
	}
	for written := uint64(0); written < capacity*4; {
		n := min(uint64(len(empty)), capacity*4-written)
		if _, err := f.WriteAt(empty[:n], int64(written)); err != nil {
			_ = f.Close()
			return fmt.Errorf("write turn slot table: %w", err)
		}
		written += n
	}
	if x.turnSlots != nil {
		_ = x.turnSlots.file.Close()
	}
	// A cached cut view borrows the table being replaced; it must not outlive it.
	x.cutView, x.cutLength = nil, 0
	x.turnSlots = &turnSlotTable{file: f, name: name, capacity: capacity}
	x.meta.TurnSlotsFile, x.meta.TurnSlotsCapacity = name, capacity
	x.turnSlotsNamed = 0
	return nil
}

// openTurnSlots opens the table the committed meta names. A build that names
// none leaves the handle without one, and every read falls back to the turn
// table itself. A file that does not size up as the meta names it is no table
// either: the fallback is always correct, and the next commit mints a new one.
func (x *Index) openTurnSlots(m meta) error {
	x.closeTurnSlots()
	if m.TurnSlotsFile == "" || m.TurnSlotsCapacity == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(x.dir, x.build, m.TurnSlotsFile), os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: open turn slot table: %w", errCorrupt, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if info.Size() != int64(m.TurnSlotsCapacity)*4 {
		_ = f.Close()
		return nil
	}
	x.turnSlots = &turnSlotTable{file: f, name: m.TurnSlotsFile, capacity: m.TurnSlotsCapacity}
	// The committed table named every committed turn when it was written.
	x.turnSlotsNamed = m.Turns
	return nil
}

// removeOtherTurnSlotTables deletes every table in the build but the live one,
// after the meta naming it committed.
func (x *Index) removeOtherTurnSlotTables() {
	if x.turnSlots == nil {
		return
	}
	entries, err := os.ReadDir(filepath.Join(x.dir, x.build))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), turnSlotsName+".") && entry.Name() != x.turnSlots.name {
			_ = os.Remove(filepath.Join(x.dir, x.build, entry.Name()))
		}
	}
}

// closeTurnSlots releases the table's file.
func (x *Index) closeTurnSlots() {
	if x.turnSlots != nil {
		_ = x.turnSlots.file.Close()
	}
	x.turnSlots = nil
	x.turnSlotsNamed = 0
	// A cached cut view borrows the table, and readAtLocked reuses it whenever
	// the length and incarnation still match, so it must not outlive the file.
	x.cutView, x.cutLength = nil, 0
}
