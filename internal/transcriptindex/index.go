package transcriptindex

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

const (
	// formatVersion is the sidecar's layout. projectionID names the projection
	// its records reproduce; either changing rebuilds every index.
	formatVersion = 1
	projectionID  = "apptranscript-items-v1/entry-ordinal-positions-v2"

	// tailBytes is how much of the indexed prefix's end validation compares,
	// the check the attention fold cursor uses (agent/session_attention.go).
	tailBytes = 4096

	// maxLineBytes bounds one transcript line, as the server's readers do.
	maxLineBytes = 128 << 20
)

const (
	metaFile    = "meta.json"
	itemsFile   = "items"
	turnsFile   = "turns"
	stringsFile = "strings"
)

// meta is the sidecar's small header. It is written after the tables, so on
// open it never counts a record the tables lack; records past its counts are
// an append that did not finish, and are dropped.
type meta struct {
	Format       int    `json:"format"`
	Projection   string `json:"projection"`
	FileIdentity string `json:"file_identity"`
	// Length is the transcript bytes indexed: the header and every complete
	// entry line. Entries counts those entries; the next one's ordinal.
	Length       int64  `json:"length"`
	Entries      uint64 `json:"entries"`
	TailSHA256   string `json:"tail_sha256"`
	HeaderOffset int64  `json:"header_offset"`
	HeaderLength int64  `json:"header_length"` // 0: no header yet
	Items        uint64 `json:"items"`
	Turns        uint64 `json:"turns"`
	// The open turn, for grouping the next entry. There is one once any
	// entry is indexed.
	Open       bool   `json:"open"`
	OpenTurnID string `json:"open_turn_id"`
	TurnSlot   uint64 `json:"turn_slot"`
}

// Index is an open transcript index. It is safe for concurrent use.
type Index struct {
	mu         sync.Mutex
	path, dir  string
	transcript *os.File
	items      table
	turns      table
	strings    blob
	meta       meta
	prelude    *appwire.Turn
	build      builder
	// stale marks an index a read found inconsistent with its transcript; the
	// next catch-up rebuilds it.
	stale bool
	// rebuilds counts full builds, for tests.
	rebuilds int
}

// Open returns the index for the transcript at path, kept in dir. A missing,
// stale or corrupt index is rebuilt from the transcript. Open catches up to
// the last complete line.
func Open(path, dir string) (*Index, error) {
	x := &Index{path: path, dir: dir}
	if err := x.load(); err != nil {
		if err := x.rebuild(); err != nil {
			_ = x.closeFiles()
			return nil, err
		}
	}
	if err := x.catchUp(); err != nil {
		_ = x.closeFiles()
		return nil, err
	}
	return x, nil
}

// Close releases the index's files.
func (x *Index) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.closeFiles()
}

// CatchUp indexes complete lines appended since the last catch-up. A
// transcript that is no longer the indexed file grown by appends is rebuilt.
func (x *Index) CatchUp() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.catchUp()
}

func (x *Index) catchUp() error {
	info, err := os.Stat(x.path)
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	if x.stale || !x.grownByAppends(info) {
		return x.rebuild()
	}
	if info.Size() == x.meta.Length {
		return nil
	}
	if err := x.scan(); err != nil {
		// The records may hold part of this scan; only a full build is
		// known to be consistent.
		return x.rebuild()
	}
	return x.writeMeta()
}

// grownByAppends reports whether the transcript at path is still the file the
// index describes: the same file, at least as long, with the same bytes
// before the indexed end.
func (x *Index) grownByAppends(info os.FileInfo) bool {
	if x.transcript == nil || x.meta.FileIdentity == "" || apptranscript.FileIdentity(info) != x.meta.FileIdentity || info.Size() < x.meta.Length {
		return false
	}
	sum, err := x.tailSum(x.meta.Length)
	return err == nil && sum == x.meta.TailSHA256
}

func (x *Index) tailSum(end int64) (string, error) {
	start := max(0, end-tailBytes)
	buf := make([]byte, end-start)
	if _, err := x.transcript.ReadAt(buf, start); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// load opens an existing index and checks it still describes the transcript.
func (x *Index) load() error {
	data, err := os.ReadFile(filepath.Join(x.dir, metaFile))
	if err != nil {
		return err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Format != formatVersion || m.Projection != projectionID {
		return errors.New("index format or projection changed")
	}
	x.meta = m
	if err := x.openFiles(false); err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	if !x.grownByAppends(info) {
		return errors.New("transcript is not the indexed file grown by appends")
	}
	if err := x.items.truncate(m.Items); err != nil {
		return err
	}
	if err := x.turns.truncate(m.Turns); err != nil {
		return err
	}
	if err := x.readHeader(); err != nil {
		return err
	}
	return x.restoreBuilder()
}

// restoreBuilder recovers the open turn's state from meta and records.
func (x *Index) restoreBuilder() error {
	b := builder{x: x, grouper: apptranscript.TurnGrouper{Open: x.meta.Open, TurnID: x.meta.OpenTurnID}, calls: map[string]uint64{}, names: map[string]toolName{}}
	if x.meta.Entries > 0 {
		buf, err := x.turns.read(x.meta.TurnSlot, 1)
		if err != nil {
			return err
		}
		b.turn, b.turnSlot = decodeTurn(buf), x.meta.TurnSlot
		// The open turn's items are the newest records.
		for slot := x.items.n; slot > 0; slot-- {
			buf, err := x.items.read(slot-1, 1)
			if err != nil {
				return err
			}
			record := decodeItem(buf)
			if uint64(record.Turn) != b.turnSlot {
				break
			}
			if record.Call.Len > 0 {
				call, err := x.strings.get(record.Call)
				if err != nil {
					return err
				}
				b.calls[string(call)] = slot - 1
			}
		}
	}
	x.build = b
	return nil
}

func (x *Index) readHeader() error {
	x.prelude = nil
	if x.meta.HeaderLength == 0 {
		return nil
	}
	line := make([]byte, x.meta.HeaderLength)
	if _, err := x.transcript.ReadAt(line, x.meta.HeaderOffset); err != nil {
		return err
	}
	header, err := transcript.DecodeHeader(bytes.TrimSpace(line))
	if err != nil {
		return err
	}
	x.prelude = apptranscript.PreludeTurn(header)
	return nil
}

// rebuild discards the sidecar and indexes the transcript from its start.
func (x *Index) rebuild() error {
	if err := x.closeFiles(); err != nil {
		return err
	}
	if err := os.MkdirAll(x.dir, 0o755); err != nil {
		return fmt.Errorf("create transcript index dir: %w", err)
	}
	for _, name := range []string{metaFile, itemsFile, turnsFile, stringsFile} {
		if err := os.Remove(filepath.Join(x.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale transcript index: %w", err)
		}
	}
	if err := x.openFiles(true); err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	x.meta = meta{Format: formatVersion, Projection: projectionID, FileIdentity: apptranscript.FileIdentity(info)}
	x.prelude = nil
	x.stale = false
	x.build = builder{x: x, global: map[string]string{}}
	x.rebuilds++
	if err := x.scan(); err != nil {
		return err
	}
	// Only the open turn's names are kept past a build: memory stays bounded
	// by the open turn, and errRebuild covers the rest.
	x.build.global = nil
	return x.writeMeta()
}

// scan indexes complete lines from the indexed end to the end of the file.
func (x *Index) scan() error {
	reader := bufio.NewReaderSize(io.NewSectionReader(x.transcript, x.meta.Length, math.MaxInt64-x.meta.Length), 64<<10)
	offset := x.meta.Length
	for {
		line, complete, n, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil {
			return err
		}
		if !complete {
			break
		}
		start := offset
		offset += n
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			x.meta.Length = offset
			continue
		}
		if x.meta.HeaderLength == 0 {
			if _, err := transcript.DecodeHeader(trimmed); err != nil {
				return fmt.Errorf("parse transcript header: %w", err)
			}
			x.meta.HeaderOffset, x.meta.HeaderLength = start, int64(len(line))
			if err := x.readHeader(); err != nil {
				return err
			}
		} else {
			entry, err := transcript.DecodeEntry(trimmed)
			if err != nil {
				return fmt.Errorf("parse transcript entry: %w", err)
			}
			if err := x.build.apply(x.meta.Entries, start, uint32(len(line)), &entry.Turn); err != nil {
				return err
			}
			x.meta.Entries++
		}
		x.meta.Length = offset
	}
	return nil
}

func (x *Index) writeMeta() error {
	sum, err := x.tailSum(x.meta.Length)
	if err != nil {
		return err
	}
	b := &x.build
	x.meta.TailSHA256 = sum
	x.meta.Items, x.meta.Turns = x.items.n, x.turns.n
	x.meta.Open, x.meta.OpenTurnID = b.grouper.Open, b.grouper.TurnID
	x.meta.TurnSlot = b.turnSlot
	data, err := json.Marshal(x.meta)
	if err != nil {
		return err
	}
	// No fsync: the index is derived, and a meta lost to a crash rebuilds or
	// replays (see meta).
	tmp := filepath.Join(x.dir, metaFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write transcript index meta: %w", err)
	}
	return os.Rename(tmp, filepath.Join(x.dir, metaFile))
}

func (x *Index) openFiles(create bool) error {
	var err error
	if x.transcript, err = os.Open(x.path); err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	open := func(name string) (*os.File, int64, error) {
		f, err := os.OpenFile(filepath.Join(x.dir, name), flags, 0o600)
		if err != nil {
			return nil, 0, err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, err
		}
		return f, info.Size(), nil
	}
	var size int64
	if x.items.file, size, err = open(itemsFile); err != nil {
		return err
	}
	x.items.size, x.items.n = itemRecordSize, uint64(size/itemRecordSize)
	if x.turns.file, size, err = open(turnsFile); err != nil {
		return err
	}
	x.turns.size, x.turns.n = turnRecordSize, uint64(size/turnRecordSize)
	if x.strings.file, size, err = open(stringsFile); err != nil {
		return err
	}
	x.strings.n = uint64(size)
	return nil
}

func (x *Index) closeFiles() error {
	var errs []error
	for _, f := range []**os.File{&x.transcript, &x.items.file, &x.turns.file, &x.strings.file} {
		if *f != nil {
			errs = append(errs, (*f).Close())
			*f = nil
		}
	}
	return errors.Join(errs...)
}
