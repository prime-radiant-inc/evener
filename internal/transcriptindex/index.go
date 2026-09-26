package transcriptindex

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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

const (
	// formatVersion is the sidecar's layout. projectionID names the projection
	// its records reproduce; either changing rebuilds every index.
	formatVersion = 4
	projectionID  = "apptranscript-items-v1/entry-ordinal-positions-v2"

	// tailBytes is how much of the covered prefix's end validation compares,
	// the check the attention fold cursor uses (agent/session_attention.go).
	tailBytes = 4096

	// maxLineBytes bounds one transcript line, as the server's readers do.
	maxLineBytes = 128 << 20
)

// The sidecar directory holds a lock file, a pointer to the live build, and one
// directory per build. A rebuild writes a new build and renames the pointer
// over, so a reader never sees a half-built index.
const (
	lockFileName    = "lock"
	currentFileName = "CURRENT"
	metaFile        = "meta.json"
	itemsFile       = "items"
	turnsFile       = "turns"
	updatesFile     = "updates"
	stringsFile     = "strings"
)

// meta is a build's small header. The extender writes it after the
// records, so it never counts a record the tables lack. Records past its
// counts belong to an extension that did not finish; nothing reads them, and
// the next extension writes over them.
type meta struct {
	Format     int    `json:"format"`
	Projection string `json:"projection"`
	// Incarnation names what the records describe. It changes only when the
	// transcript stops being an extension of the covered prefix; a rebuild
	// the index needs for itself (errRebuild) keeps it.
	Incarnation  string `json:"incarnation"`
	FileIdentity string `json:"file_identity"`
	// Length is the transcript bytes covered: the header and every complete
	// entry line. Entries counts those entries; the next one's ordinal.
	Length       int64  `json:"length"`
	Entries      uint64 `json:"entries"`
	TailSHA256   string `json:"tail_sha256"`
	HeaderOffset int64  `json:"header_offset"`
	HeaderLength int64  `json:"header_length"` // 0: no header yet
	Items        uint64 `json:"items"`
	Turns        uint64 `json:"turns"`
	Updates      uint64 `json:"updates"`
	// The open turn, for grouping the next entry. There is one once any
	// entry is covered.
	Open       bool   `json:"open"`
	OpenTurnID string `json:"open_turn_id"`
	TurnSlot   uint64 `json:"turn_slot"`
}

// Index is an open transcript index. It is safe for concurrent use, and other
// handles, in this process or another, may share its sidecar directory.
type Index struct {
	mu         sync.Mutex
	path, dir  string
	lock       *os.File
	transcript *os.File
	build      string // the live build's directory name
	items      table
	turns      table
	updates    table
	strings    blob
	meta       meta
	prelude    *appwire.Turn
	builder    builder
	// builderStale reports records another handle extended, which the
	// builder's open-turn state does not reflect yet.
	builderStale bool
	// stale marks an index a read found inconsistent with its transcript; the
	// next extension rebuilds it.
	stale bool
	// rebuilds counts full builds by this handle, for tests.
	rebuilds int
}

// Open returns the index for the transcript at path, kept in dir. A missing,
// stale or corrupt index is rebuilt from the transcript. Open catches up to
// the last complete line.
func Open(path, dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create transcript index dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open transcript index lock: %w", err)
	}
	x := &Index{path: path, dir: dir, lock: lock}
	if err := x.CatchUp(); err != nil {
		_ = x.Close()
		return nil, err
	}
	return x, nil
}

// Close releases the index's files.
func (x *Index) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	err := x.closeBuild()
	if x.lock != nil {
		err = errors.Join(err, x.lock.Close())
		x.lock = nil
	}
	return err
}

// CatchUp indexes every complete line in the transcript.
func (x *Index) CatchUp() error {
	info, err := os.Stat(x.path)
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	return x.CatchUpTo(info.Size())
}

// CatchUpTo indexes the complete lines that end at or before length: the
// recorded length, for a caller that knows it. A transcript that is no longer
// the covered file grown by appends is rebuilt, under a new incarnation.
func (x *Index) CatchUpTo(length int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.locked(true, func() error { return x.extend(length) })
}

// locked runs fn holding the sidecar lock, shared or exclusive, after taking
// up whatever other handles wrote.
func (x *Index) locked(exclusive bool, fn func() error) error {
	if x.lock == nil {
		return errors.New("transcript index is closed")
	}
	if err := lockFile(x.lock, exclusive); err != nil {
		return fmt.Errorf("lock transcript index: %w", err)
	}
	defer func() { _ = unlockFile(x.lock) }()
	if err := x.refresh(); err != nil && !exclusive {
		// Only an extender may rebuild; a reader with no usable index has
		// nothing to read.
		return err
	}
	return fn()
}

// refresh takes up the sidecar as it is on disk: another build reopens,
// and a changed covered state (another handle extended) is adopted.
func (x *Index) refresh() error {
	current, err := os.ReadFile(filepath.Join(x.dir, currentFileName))
	if err != nil {
		_ = x.closeBuild()
		return errors.New("transcript index has no live build")
	}
	name := strings.TrimSpace(string(current))
	if name != x.build {
		_ = x.closeBuild()
		if err := x.load(name); err != nil {
			_ = x.closeBuild()
			return err
		}
		return nil
	}
	m, err := x.readMeta()
	if err != nil {
		_ = x.closeBuild()
		return err
	}
	if m != x.meta {
		if err := x.adopt(m); err != nil {
			_ = x.closeBuild()
			return err
		}
	}
	return nil
}

func (x *Index) readMeta() (meta, error) {
	var m meta
	data, err := os.ReadFile(filepath.Join(x.dir, x.build, metaFile))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Format != formatVersion || m.Projection != projectionID {
		return m, errors.New("transcript index format or projection changed")
	}
	return m, nil
}

// adopt takes m as the covered state. The builder's open-turn state is
// restored from records before the next extension.
func (x *Index) adopt(m meta) error {
	items, err := x.items.available()
	if err != nil {
		return err
	}
	turns, err := x.turns.available()
	if err != nil {
		return err
	}
	updates, err := x.updates.available()
	if err != nil {
		return err
	}
	if m.Items > items || m.Turns > turns || m.Updates > updates {
		return fmt.Errorf("%w: meta counts records the tables lack", errCorrupt)
	}
	if m.HeaderLength < 0 || m.HeaderOffset < 0 || m.HeaderOffset+m.HeaderLength > m.Length {
		return fmt.Errorf("%w: header_offset/header_length outside the covered transcript", errCorrupt)
	}
	info, err := x.strings.file.Stat()
	if err != nil {
		return err
	}
	x.meta, x.items.n, x.turns.n, x.updates.n, x.strings.n = m, m.Items, m.Turns, m.Updates, uint64(info.Size())
	x.builderStale = true
	return x.readHeader()
}

// load opens build name, which must describe the transcript at path.
func (x *Index) load(name string) error {
	x.build = name
	if err := x.openFiles(false); err != nil {
		return err
	}
	m, err := x.readMeta()
	if err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	if apptranscript.FileIdentity(info) != m.FileIdentity {
		return errors.New("transcript index describes another file")
	}
	return x.adopt(m)
}

// extend covers complete lines up to length, rebuilding when the covered
// prefix is no longer the transcript's.
func (x *Index) extend(length int64) error {
	info, err := os.Stat(x.path)
	if err != nil {
		return fmt.Errorf("stat transcript: %w", err)
	}
	length = min(length, info.Size())
	if x.build == "" || x.stale || !x.grownByAppends(info) {
		return x.rebuild(length, "")
	}
	if length <= x.meta.Length {
		return nil
	}
	// Whatever an extension that did not finish left past the counts goes
	// before this one appends.
	for _, t := range []*table{&x.items, &x.turns, &x.updates} {
		if err := t.truncate(t.n); err != nil {
			return err
		}
	}
	if x.builderStale {
		if err := x.restoreBuilder(); err != nil {
			return x.rebuild(length, "")
		}
	}
	if err := x.scan(length); err != nil {
		// The records may hold part of this scan; only a full build is
		// known to be consistent. The transcript still extends the covered
		// prefix when the index only needed the whole file's tool names.
		incarnation := ""
		if errors.Is(err, errRebuild) {
			incarnation = x.meta.Incarnation
		}
		return x.rebuild(length, incarnation)
	}
	return x.writeMeta()
}

// grownByAppends reports whether the transcript at path is still the file the
// index covers: the same file, at least as long, with the same bytes before
// the covered end.
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

// restoreBuilder recovers the open turn's state from meta and records.
func (x *Index) restoreBuilder() error {
	b := builder{x: x, grouper: apptranscript.TurnGrouper{Open: x.meta.Open, TurnID: x.meta.OpenTurnID}, calls: map[string]uint64{}, names: map[string]toolName{}, commCalls: map[string]commState{}}
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
	x.builder = b
	x.builderStale = false
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

// rebuild indexes the transcript from its start into a new build, then makes
// it the live one. incarnation carries the covered records' incarnation over
// when the transcript still extends them; "" mints a new one. A build that
// fails is removed, and the live build stays as it was.
func (x *Index) rebuild(length int64, incarnation string) error {
	err := x.buildNew(length, incarnation)
	if err != nil && x.build != "" {
		failed := filepath.Join(x.dir, x.build)
		err = errors.Join(err, x.closeBuild(), os.RemoveAll(failed))
	}
	return err
}

func (x *Index) buildNew(length int64, incarnation string) error {
	if err := x.closeBuild(); err != nil {
		return err
	}
	name, err := randomName()
	if err != nil {
		return err
	}
	if incarnation == "" {
		if incarnation, err = randomName(); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(x.dir, name), 0o755); err != nil {
		return fmt.Errorf("create transcript index build: %w", err)
	}
	x.build = name
	if err := x.openFiles(true); err != nil {
		return err
	}
	info, err := x.transcript.Stat()
	if err != nil {
		return err
	}
	length = min(length, info.Size())
	x.meta = meta{Format: formatVersion, Projection: projectionID, Incarnation: incarnation, FileIdentity: apptranscript.FileIdentity(info)}
	x.prelude = nil
	x.stale, x.builderStale = false, false
	x.builder = builder{x: x, global: map[string]string{}, commCalls: map[string]commState{}, lastAssistantKnown: true}
	x.rebuilds++
	if err := x.scan(length); err != nil {
		return err
	}
	// Only the open turn's names are kept past a build: memory stays bounded
	// by the open turn, and errRebuild covers the rest.
	x.builder.global = nil
	if err := x.writeMeta(); err != nil {
		return err
	}
	if err := writeFileAtomically(filepath.Join(x.dir, currentFileName), []byte(name+"\n")); err != nil {
		return err
	}
	x.removeOtherBuilds()
	return nil
}

// removeOtherBuilds deletes every build but the live one. A reader in another
// process may still hold an old one's files open; where the platform refuses
// the removal, the next rebuild tries again.
func (x *Index) removeOtherBuilds() {
	entries, err := os.ReadDir(x.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != x.build {
			_ = os.RemoveAll(filepath.Join(x.dir, entry.Name()))
		}
	}
}

// scan covers complete lines from the covered end up to length.
func (x *Index) scan(length int64) error {
	reader := bufio.NewReaderSize(io.NewSectionReader(x.transcript, x.meta.Length, length-x.meta.Length), 64<<10)
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
			if err := x.builder.apply(x.meta.Entries, start, uint32(len(line)), &entry.Turn); err != nil {
				return err
			}
			x.meta.Entries++
		}
		x.meta.Length = offset
	}
	return nil
}

// writeMeta publishes the covered state, after the records it counts.
func (x *Index) writeMeta() error {
	sum, err := x.tailSum(x.meta.Length)
	if err != nil {
		return err
	}
	b := &x.builder
	x.meta.TailSHA256 = sum
	x.meta.Items, x.meta.Turns, x.meta.Updates = x.items.n, x.turns.n, x.updates.n
	x.meta.Open, x.meta.OpenTurnID = b.grouper.Open, b.grouper.TurnID
	x.meta.TurnSlot = b.turnSlot
	data, err := json.Marshal(x.meta)
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(x.dir, x.build, metaFile), data)
}

// writeFileAtomically replaces path by rename. There is no fsync: the index is
// derived, and whatever a crash loses rebuilds or replays.
func writeFileAtomically(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write transcript index: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish transcript index: %w", err)
	}
	return nil
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
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(x.dir, x.build, name), flags, 0o600)
	}
	if x.items.file, err = open(itemsFile); err != nil {
		return err
	}
	x.items.size = itemRecordSize
	if x.turns.file, err = open(turnsFile); err != nil {
		return err
	}
	x.turns.size = turnRecordSize
	if x.updates.file, err = open(updatesFile); err != nil {
		return err
	}
	x.updates.size = updateRecordSize
	x.strings.file, err = open(stringsFile)
	return err
}

// closeBuild drops the open build and its view of the transcript.
func (x *Index) closeBuild() error {
	var errs []error
	for _, f := range []**os.File{&x.transcript, &x.items.file, &x.turns.file, &x.updates.file, &x.strings.file} {
		if *f != nil {
			errs = append(errs, (*f).Close())
			*f = nil
		}
	}
	x.build = ""
	x.meta = meta{}
	x.prelude = nil
	x.items.n, x.turns.n, x.updates.n, x.strings.n = 0, 0, 0, 0
	return errors.Join(errs...)
}

func randomName() (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
