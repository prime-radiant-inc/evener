package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// This file holds the ONE write-ahead intent record an instance mutation leaves
// behind - a removal or a rename - and the ONE stamped-name grammar both it and
// the parked copies are named by. Before the consolidation five filename-encoded
// record types did this job (four aside marker spellings, a commit manifest, a
// rename marker and a rename journal); they are gone, and the copy names no
// longer carry any classification: what a parked copy is for is read from the
// intent that recorded the mutation, never guessed from its name.

// stampedName builds the file name of a stamped record: <record><marker><stamp>,
// where record is the record file's own name (its .json suffix included). It is
// the one builder parseStampedName is the parser of, so the two shapes - an
// intent and a parked copy - can never drift apart.
func stampedName(record, marker string, stamp int64) string {
	return record + marker + strconv.FormatInt(stamp, 10)
}

// parseStampedName splits a name built by stampedName back into the record it
// belongs to and its stamp text. It reads the TRAILING marker occurrence that
// leaves an all-digits tail, which is what makes a copy of an instance whose own
// name holds a marker (a legal provider name) parse as a copy of that instance
// rather than of a name cut out of its middle. A name whose record part does not
// end in .json, whose stamp is empty or not all digits, or that holds no marker
// at all is not one of these names.
func parseStampedName(fileName, marker string) (record, stampText string, ok bool) {
	i := strings.LastIndex(fileName, marker)
	if i < 0 {
		return "", "", false
	}
	record, stampText = fileName[:i], fileName[i+len(marker):]
	if !strings.HasSuffix(record, ".json") || stampText == "" {
		return "", "", false
	}
	if strings.IndexFunc(stampText, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return "", "", false
	}
	return record, stampText, true
}

// oauthAsideMarker separates a record's path from the stamp of a copy a removal
// parked (setAsideOAuthFile). It is what tells a parked copy apart from a record
// and from every transaction record when the copies are reclaimed, and the
// single shape is deliberate: the removal that parked it may be in doubt, but
// that is the intent record's business, not the file name's.
const oauthAsideMarker = ".removing-"

// oauthAsideName names the copy a removal parks for inst's record at stamp.
func oauthAsideName(inst string, stamp int64) string {
	return stampedName(inst+".json", oauthAsideMarker, stamp)
}

// parseOAuthAside reports the instance a parked copy belongs to and the stamp it
// was parked under. A record's whole name - its .json suffix included - plus the
// marker and a stamp is a copy, so an instance whose own name holds the marker
// (x.removing-1's record is x.removing-1.json) is not one.
func parseOAuthAside(fileName string) (inst, stampText string, ok bool) {
	record, stampText, ok := parseStampedName(fileName, oauthAsideMarker)
	if !ok {
		return "", "", false
	}
	return strings.TrimSuffix(record, ".json"), stampText, true
}

// A record carries its progress in its NAME, not in a field. One mutation
// writes one record, and the two names below are the two states it can be in:
// still filed under oauthIntentMarker means the mutation has not passed its
// commit point, and renamed to oauthLandedMarker means it has.
//
// The name is the stronger discriminator, and deliberately so. A rename is one
// atomic syscall: it cannot be torn, half written, or left with a value that
// disagrees with the state of the world, and the commit cannot be forged by
// rewriting the record's bytes - a rewrite that loses its race leaves the
// record exactly as it was. The stamp the name already carries then lets
// recovery ORDER the commit against the copies it claims (a landed record older
// than a copy cannot settle that copy), which a flag alone can never do, and
// the writer fsyncs the directory around the rename so the commit survives a
// power failure rather than living only in the page cache.
//
// oauthIntentMarker names a record whose mutation is still in progress, and
// deliberately not the parked copy's marker: a record is a transaction record,
// and the copy rules - the allocation, the sweep, the reclaim - must never
// touch it.
const oauthIntentMarker = ".intent-"

// oauthLandedMarker names a record whose mutation passed its commit point: the
// durable evidence that the removal's credential deletions (and, when it
// changed, its providers.toml write) had all landed, and that the mutation is
// the standing one rather than one still in doubt.
const oauthLandedMarker = ".landed-"

// oauthIntentName names the record a mutation of inst writes at stamp. inst is
// the name the mutation is about: the OLD name for a rename.
func oauthIntentName(inst string, stamp int64) string {
	return stampedName(inst+".json", oauthIntentMarker, stamp)
}

// oauthLandedName names the same record once its mutation passed its commit
// point. The stamp is preserved, so the commit orders against the copies the
// record claims exactly as the record itself does.
func oauthLandedName(inst string, stamp int64) string {
	return stampedName(inst+".json", oauthLandedMarker, stamp)
}

// parseOAuthIntent reports the instance a record is about, the stamp it was
// written under, and whether it has passed its commit point. Both names are one
// grammar, so every reader parses them the same way: a name that is neither
// says ok false.
func parseOAuthIntent(fileName string) (inst, stampText string, landed, ok bool) {
	if record, stampText, ok := parseStampedName(fileName, oauthLandedMarker); ok {
		return strings.TrimSuffix(record, ".json"), stampText, true, true
	}
	record, stampText, ok := parseStampedName(fileName, oauthIntentMarker)
	if !ok {
		return "", "", false, false
	}
	return strings.TrimSuffix(record, ".json"), stampText, false, true
}

// oauthIntentOp is the mutation one intent record describes.
type oauthIntentOp string

const (
	oauthOpRemove oauthIntentOp = "remove"
	oauthOpRename oauthIntentOp = "rename"
)

// oauthRemovalKind is which layer carried the instance a removal was deleting:
// config-backed when the removal changed providers.toml (an authored
// [providers.<name>] entry or a `default` pointer naming the instance at removal
// start), credential-only otherwise. Recovery needs it because the two kinds are
// judged by different evidence: a config-backed removal's progress is written in
// providers.toml itself, while a credential-only instance exists from its record
// alone and has no config entry to ask.
type oauthRemovalKind string

const (
	oauthKindConfigBacked   oauthRemovalKind = "config-backed"
	oauthKindCredentialOnly oauthRemovalKind = "credential-only"
)

// oauthIntent is the record itself: what the mutation is (op), which instance
// it is about, the removal's kind, and the rename's new name. How far the
// mutation got is NOT a field - it is the name the record is filed under
// (oauthIntentMarker vs oauthLandedMarker).
type oauthIntent struct {
	op   oauthIntentOp
	inst string
	new  string           // op=rename only
	kind oauthRemovalKind // op=remove only
}

// removalIntent records a removal of inst that removes the given kind of layer.
func removalIntent(inst string, configBacked bool) oauthIntent {
	kind := oauthKindCredentialOnly
	if configBacked {
		kind = oauthKindConfigBacked
	}
	return oauthIntent{op: oauthOpRemove, inst: inst, kind: kind}
}

// renameIntent records the rename of oldName to newName.
func renameIntent(oldName, newName string) oauthIntent {
	return oauthIntent{op: oauthOpRename, inst: oldName, new: newName}
}

// encode renders the record in the one line order the parser accepts. An empty
// field is left out, so a reader can tell a removal (no `new`) from a rename (no
// `kind`) by what is there.
func (i oauthIntent) encode() []byte {
	var b strings.Builder
	b.WriteString("op=" + string(i.op) + "\n")
	b.WriteString("inst=" + i.inst + "\n")
	if i.op == oauthOpRename {
		b.WriteString("new=" + i.new + "\n")
	}
	if i.op == oauthOpRemove {
		b.WriteString("kind=" + string(i.kind) + "\n")
	}
	return []byte(b.String())
}

// parseOAuthIntentRecord is the one strict reader of an intent record. Every
// line must be a known key=value pair, no key may repeat, and every field the op
// needs must be there - a record that does not read back is an error, never a
// guess: the caller keeps the file and reports it (restoreUncommittedOAuthAsides
// defers everything the record might have classified). path only names the file
// in that error.
func parseOAuthIntentRecord(raw []byte, path string) (oauthIntent, error) {
	var i oauthIntent
	seen := make(map[string]bool, 5)
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			return oauthIntent{}, fmt.Errorf("%s records an empty line", path)
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return oauthIntent{}, fmt.Errorf("%s records %q, which is not a key=value line", path, line)
		}
		if seen[key] {
			return oauthIntent{}, fmt.Errorf("%s records %s twice", path, key)
		}
		seen[key] = true
		switch key {
		case "op":
			switch oauthIntentOp(value) {
			case oauthOpRemove:
				i.op = oauthOpRemove
			case oauthOpRename:
				i.op = oauthOpRename
			default:
				return oauthIntent{}, fmt.Errorf("%s records the unknown op %q", path, value)
			}
		case "inst":
			i.inst = value
		case "new":
			i.new = value
		case "kind":
			switch oauthRemovalKind(value) {
			case oauthKindConfigBacked:
				i.kind = oauthKindConfigBacked
			case oauthKindCredentialOnly:
				i.kind = oauthKindCredentialOnly
			default:
				return oauthIntent{}, fmt.Errorf("%s records the unknown kind %q", path, value)
			}
		default:
			return oauthIntent{}, fmt.Errorf("%s records the unknown field %q", path, key)
		}
	}
	switch i.op {
	case oauthOpRemove:
		if i.kind == "" {
			return oauthIntent{}, fmt.Errorf("%s records a removal with no kind", path)
		}
		if i.new != "" {
			return oauthIntent{}, fmt.Errorf("%s records a removal that also names a new instance (%q)", path, i.new)
		}
	case oauthOpRename:
		if i.new == "" {
			return oauthIntent{}, fmt.Errorf("%s records a rename with no new name", path)
		}
		if i.kind != "" {
			return oauthIntent{}, fmt.Errorf("%s records a rename that also names a removal kind (%q)", path, i.kind)
		}
	default:
		return oauthIntent{}, fmt.Errorf("%s records no op", path)
	}
	if !registry.ValidInstanceName(i.inst) {
		return oauthIntent{}, fmt.Errorf("%s records the invalid instance name %q", path, i.inst)
	}
	if i.op == oauthOpRename && !registry.ValidInstanceName(i.new) {
		return oauthIntent{}, fmt.Errorf("%s records the invalid new instance name %q", path, i.new)
	}
	return i, nil
}

// writeOAuthIntentFile writes one intent record atomically: a temp file beside
// the final name, then a rename over it. The record is ours, so replacing the
// file already there is intended - a phase update replaces the record it is
// updating. The temp file is named so neither the intent nor the copy parser
// reads it, so a crash between the write and the rename leaves inert debris
// rather than a record.
func writeOAuthIntentFile(path string, i oauthIntent) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "intent-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.Write(i.encode()); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	// The record is the mutation's only recovery evidence, so it is fsynced
	// before the rename that publishes it: a commit that exists only in the page
	// cache is no evidence at all after a power failure.
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return serr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		_ = os.Remove(tmp)
		return rerr
	}
	return syncDir(filepath.Dir(path))
}

// landOAuthIntent carries a written record to its commit point by renaming it
// to the landed name, and returns the path it now holds. An empty path is a
// mutation that wrote no record - a removal of a name whose auth directory does
// not exist has nothing a crash could strand - so there is nothing to land and
// nothing to refuse. A record already landed is returned as it is (the caller
// may have landed it before, and landing is idempotent), and a path whose name
// is not a record at all is refused rather than renamed.
//
// The rename is the commit: it is no-replace, so a file already at the landed
// name - another mutation's bytes - is never overwritten, and the caller treats
// the refusal as a failed commit point (it rolls the mutation back).
func landOAuthIntent(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	inst, stampText, landed, ok := parseOAuthIntent(filepath.Base(path))
	if !ok {
		return path, fmt.Errorf("%s is not a record name, so it cannot be carried to its commit point", path)
	}
	if landed {
		return path, nil
	}
	stamp, perr := strconv.ParseInt(stampText, 10, 64)
	if perr != nil {
		return path, fmt.Errorf("%s carries the stamp %q, which cannot be ordered", path, stampText)
	}
	landedPath := filepath.Join(filepath.Dir(path), oauthLandedName(inst, stamp))
	if err := renameNoReplace(path, landedPath); err != nil {
		return path, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return landedPath, err
	}
	return landedPath, nil
}

// unlandOAuthIntent returns a record to the in-doubt name, so recovery reads it
// as a mutation that did not stand. The rollback of a mutation that reached its
// commit point and then failed calls it: with the record back at the in-flight
// name, the copy the failed rollback could not put back is restored by the next
// start instead of being swept. An empty or already-in-flight path is returned
// unchanged, and a name that is not a record is refused.
func unlandOAuthIntent(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	inst, stampText, landed, ok := parseOAuthIntent(filepath.Base(path))
	if !ok {
		return path, fmt.Errorf("%s is not a record name, so it cannot be returned to its in-doubt name", path)
	}
	if !landed {
		return path, nil
	}
	stamp, perr := strconv.ParseInt(stampText, 10, 64)
	if perr != nil {
		return path, fmt.Errorf("%s carries the stamp %q, which cannot be ordered", path, stampText)
	}
	inFlight := filepath.Join(filepath.Dir(path), oauthIntentName(inst, stamp))
	if err := renameNoReplace(path, inFlight); err != nil {
		return path, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return inFlight, err
	}
	return inFlight, nil
}

// syncDir fsyncs a directory, so a rename that has just published (or withdrawn)
// a record survives a power failure. A directory the process cannot open is not
// a failure of the mutation - the rename itself landed, and the next start reads
// the directory as it is - so an open failure is reported, not enforced.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// removeOAuthIntent removes one intent record once its mutation is resolved. A
// record that is already gone is not an error: two callers can race to spend the
// same record, and the loser lost nothing.
func removeOAuthIntent(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// oauthAsideStampText returns the stamp text a parked copy's name carries. The
// caller has already accepted the name as a copy (parseOAuthAside), so the tail
// after its marker is all digits.
func oauthAsideStampText(name string) string {
	_, stampText, _ := parseOAuthAside(name)
	return stampText
}
