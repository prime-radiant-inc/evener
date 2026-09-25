package transcriptindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// Record sizes. Records are fixed-size so a record's slot names its offset,
// and an append can rewrite one in place.
const (
	itemRecordSize   = 112
	turnRecordSize   = 96
	contributorSize  = 32
	updateRecordSize = 24
)

var errCorrupt = errors.New("transcript index is corrupt")

// strRef names bytes in the strings file. Len 0 means absent.
type strRef struct {
	Off uint64
	Len uint32
}

// contributor is one transcript entry that contributes to an item: where its
// line sits, its entry ordinal, and the tool name the whole-file projection
// resolved for the item's call when it projected that entry. Name is absent
// unless the entry is a tool-results entry whose result for the call carried
// no name of its own and the projection knew one.
type contributor struct {
	Offset  int64
	Ordinal uint64
	Length  uint32
	Name    strRef
}

// itemRecord is one projected item. Records are sorted by position.
type itemRecord struct {
	Entry     uint64 // position.Entry: the opener's entry ordinal + 1
	Part      uint32 // position.Item: the opener's content part
	Turn      uint32 // turn summary slot
	Version   uint64 // highest contributing entry ordinal + 1
	Call      strRef // the call id, for an item that merges by call id
	Opener    contributor
	Completer contributor // Length 0 until a later entry contributes
	Middle    strRef      // contributors between opener and completer, encoded
}

// Turn statuses, as the summary's latest lifecycle entry sets them.
const (
	statusCompleted uint32 = iota
	statusFailed
	statusInterrupted
)

// turnRecord summarizes one logical turn: everything needed to stamp the turn
// without reading its entries.
//
// Lifecycle names the latest entry that sets the turn's status, and Status is
// the status it set. Today's grouping has one rule for it: a turn is failed
// once any TURN_FAILURE is in it, and the latest one carries the diagnostic;
// otherwise it is interrupted once an interrupted STEERING is in it.
type turnRecord struct {
	ID              strRef
	FirstOffset     int64
	FirstOrdinal    uint64
	LifecycleOffset int64
	LifecycleLength uint32 // 0: no entry carries the status's diagnostic
	Status          uint32
	Started         bool     // StartedAt is set
	StartedAt       int64    // unix ms of the first entry with a timestamp
	Usage           [4]int64 // input, output, cache read, total tokens
	Version         uint64   // highest accounted entry ordinal + 1
}

// Update-log record kinds.
const (
	updatedItem uint32 = iota + 1
	updatedTurn
)

// updateRecord logs one in-place update: the record it changed and the offset
// of the entry that changed it. Records are in append order, so their offsets
// never decrease.
type updateRecord struct {
	Kind   uint32
	Slot   uint64
	Offset int64
}

func encodeUpdate(r updateRecord) []byte {
	c := codec{buf: make([]byte, updateRecordSize)}
	c.putU32(r.Kind)
	c.putU32(0)
	c.putU64(r.Slot)
	c.putI64(r.Offset)
	return c.buf
}

func decodeUpdate(buf []byte) updateRecord {
	var r updateRecord
	c := codec{buf: buf}
	c.u32(&r.Kind)
	c.at += 4 // padding
	c.u64(&r.Slot)
	c.i64(&r.Offset)
	return r
}

// codec reads and writes little-endian fields over a fixed buffer.
type codec struct {
	buf []byte
	at  int
}

func (c *codec) u64(v *uint64) {
	*v = binary.LittleEndian.Uint64(c.buf[c.at:])
	c.at += 8
}

func (c *codec) u32(v *uint32) {
	*v = binary.LittleEndian.Uint32(c.buf[c.at:])
	c.at += 4
}

func (c *codec) i64(v *int64) {
	*v = int64(binary.LittleEndian.Uint64(c.buf[c.at:]))
	c.at += 8
}

func (c *codec) putU64(v uint64) {
	binary.LittleEndian.PutUint64(c.buf[c.at:], v)
	c.at += 8
}

func (c *codec) putU32(v uint32) {
	binary.LittleEndian.PutUint32(c.buf[c.at:], v)
	c.at += 4
}

func (c *codec) putI64(v int64) { c.putU64(uint64(v)) }

// bool and putBool keep a u32 slot, so the record layout stays aligned.
func (c *codec) bool(v *bool) {
	var u uint32
	c.u32(&u)
	*v = u != 0
}

func (c *codec) putBool(v bool) {
	var u uint32
	if v {
		u = 1
	}
	c.putU32(u)
}

func (c *codec) ref(r *strRef)   { c.u64(&r.Off); c.u32(&r.Len) }
func (c *codec) putRef(r strRef) { c.putU64(r.Off); c.putU32(r.Len) }

func (c *codec) contributor(v *contributor) {
	c.i64(&v.Offset)
	c.u64(&v.Ordinal)
	c.u32(&v.Length)
	c.ref(&v.Name)
}

func (c *codec) putContributor(v contributor) {
	c.putI64(v.Offset)
	c.putU64(v.Ordinal)
	c.putU32(v.Length)
	c.putRef(v.Name)
}

func encodeItem(r itemRecord) []byte {
	c := codec{buf: make([]byte, itemRecordSize)}
	c.putU64(r.Entry)
	c.putU32(r.Part)
	c.putU32(r.Turn)
	c.putU64(r.Version)
	c.putRef(r.Call)
	c.putContributor(r.Opener)
	c.putContributor(r.Completer)
	c.putRef(r.Middle)
	return c.buf
}

func decodeItem(buf []byte) itemRecord {
	var r itemRecord
	c := codec{buf: buf}
	c.u64(&r.Entry)
	c.u32(&r.Part)
	c.u32(&r.Turn)
	c.u64(&r.Version)
	c.ref(&r.Call)
	c.contributor(&r.Opener)
	c.contributor(&r.Completer)
	c.ref(&r.Middle)
	return r
}

func encodeTurn(r turnRecord) []byte {
	c := codec{buf: make([]byte, turnRecordSize)}
	c.putRef(r.ID)
	c.putI64(r.FirstOffset)
	c.putU64(r.FirstOrdinal)
	c.putI64(r.LifecycleOffset)
	c.putU32(r.LifecycleLength)
	c.putU32(r.Status)
	c.putBool(r.Started)
	c.putI64(r.StartedAt)
	for _, v := range r.Usage {
		c.putI64(v)
	}
	c.putU64(r.Version)
	return c.buf
}

func decodeTurn(buf []byte) turnRecord {
	var r turnRecord
	c := codec{buf: buf}
	c.ref(&r.ID)
	c.i64(&r.FirstOffset)
	c.u64(&r.FirstOrdinal)
	c.i64(&r.LifecycleOffset)
	c.u32(&r.LifecycleLength)
	c.u32(&r.Status)
	c.bool(&r.Started)
	c.i64(&r.StartedAt)
	for i := range r.Usage {
		c.i64(&r.Usage[i])
	}
	c.u64(&r.Version)
	return r
}

func encodeContributors(list []contributor) []byte {
	c := codec{buf: make([]byte, len(list)*contributorSize)}
	for _, v := range list {
		c.putContributor(v)
	}
	return c.buf
}

func decodeContributors(buf []byte) ([]contributor, error) {
	if len(buf)%contributorSize != 0 {
		return nil, fmt.Errorf("%w: contributor list of %d bytes", errCorrupt, len(buf))
	}
	list := make([]contributor, len(buf)/contributorSize)
	c := codec{buf: buf}
	for i := range list {
		c.contributor(&list[i])
	}
	return list, nil
}

// table is a file of fixed-size records.
type table struct {
	file *os.File
	size int64 // record size
	n    uint64
}

func (t *table) read(slot uint64, count int) ([]byte, error) {
	if slot+uint64(count) > t.n {
		return nil, fmt.Errorf("%w: records %d..%d past %d", errCorrupt, slot, slot+uint64(count), t.n)
	}
	buf := make([]byte, int64(count)*t.size)
	if _, err := t.file.ReadAt(buf, int64(slot)*t.size); err != nil {
		return nil, fmt.Errorf("%w: read records: %w", errCorrupt, err)
	}
	return buf, nil
}

func (t *table) write(slot uint64, record []byte) error {
	if _, err := t.file.WriteAt(record, int64(slot)*t.size); err != nil {
		return fmt.Errorf("write index record: %w", err)
	}
	return nil
}

func (t *table) append(record []byte) (uint64, error) {
	slot := t.n
	if err := t.write(slot, record); err != nil {
		return 0, err
	}
	t.n++
	return slot, nil
}

// blob is the append-only strings file.
type blob struct {
	file *os.File
	n    uint64
}

func (b *blob) put(data []byte) (strRef, error) {
	if len(data) == 0 {
		return strRef{}, nil
	}
	ref := strRef{Off: b.n, Len: uint32(len(data))}
	if _, err := b.file.WriteAt(data, int64(b.n)); err != nil {
		return strRef{}, fmt.Errorf("write index strings: %w", err)
	}
	b.n += uint64(len(data))
	return ref, nil
}

func (b *blob) get(ref strRef) ([]byte, error) {
	if ref.Len == 0 {
		return nil, nil
	}
	if ref.Off+uint64(ref.Len) > b.n {
		return nil, fmt.Errorf("%w: string past the strings file", errCorrupt)
	}
	buf := make([]byte, ref.Len)
	if _, err := b.file.ReadAt(buf, int64(ref.Off)); err != nil {
		return nil, fmt.Errorf("%w: read strings: %w", errCorrupt, err)
	}
	return buf, nil
}

// available is how many whole records the file holds, counted or not.
func (t *table) available() (uint64, error) {
	info, err := t.file.Stat()
	if err != nil {
		return 0, err
	}
	return uint64(info.Size() / t.size), nil
}

// truncate drops records past n: whatever an extension that did not finish
// left past the counts.
func (t *table) truncate(n uint64) error {
	if err := t.file.Truncate(int64(n) * t.size); err != nil {
		return fmt.Errorf("truncate index records: %w", err)
	}
	t.n = n
	return nil
}

// search returns the first slot whose record before rejects, as sort.Search
// does, reading one record per probe. before must hold for a prefix of the
// table and fail for the rest.
func (t *table) search(before func(record []byte) bool) (uint64, error) {
	lo, hi := uint64(0), t.n
	for lo < hi {
		mid := lo + (hi-lo)/2
		buf, err := t.read(mid, 1)
		if err != nil {
			return 0, err
		}
		if before(buf) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}
