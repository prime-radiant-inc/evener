package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// quarantinedStoreJSON is the fixture §4's quarantine tests start from: two
// records with a dense id set below the file's own allocator high-water mark —
// one open `orphan-unverified` fence and one terminal record — whose file is
// nevertheless schema-invalid because the store's state-transition sequence sits
// below the stamp the terminal record carries. Every record parses and
// validates, so custody can be complete; the store-level sequence value is
// exactly the kind of corruption the custody snapshot does not depend on.
const quarantinedStoreJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`

// quarantinedStoreWithRemoteFenceJSON is the same shape with a third record: a
// fencing-quarantine record carrying a `remote-fencing` boundary, so the
// custody fence's `quarantine` discriminator has both values to pin.
const quarantinedStoreWithRemoteFenceJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":3,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2},` +
	`{"id":"00000000000000000003","clientOperationId":"op-h3","host":"h3","kind":"restart","state":"orphan-unverified",` +
	`"generation":4,"incarnationId":"inc-h3","orphanBoundary":[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":4},` +
	`"guardEpoch":9,"leaseEntries":[]}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`

// quarantineArtifact reads the one file in dir whose name matches the suffix.
func quarantineArtifact(t *testing.T, dir, infix string) (string, []byte) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var found []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), infix) {
			found = append(found, entry.Name())
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d artifacts matching %q in %s (%v), want exactly one", len(found), infix, dir, found)
	}
	return filepath.Join(dir, found[0]), mustReadFile(t, filepath.Join(dir, found[0]))
}

// fsQuarantineArtifact is quarantineArtifact through an afero filesystem.
func fsQuarantineArtifact(t *testing.T, fs afero.Fs, dir, infix string) (string, []byte) {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var found []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), infix) {
			found = append(found, entry.Name())
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d artifacts matching %q in %s (%v), want exactly one", len(found), infix, dir, found)
	}
	body := mustReadFSFile(t, fs, filepath.Join(dir, found[0]))
	return filepath.Join(dir, found[0]), body
}

// wantQuarantined asserts one corrupt store file took §4's custody-first
// quarantine: the handle serves a replacement store, the operator-visible
// signal names the file, and the aside file holds the original bytes verbatim.
func wantQuarantined(t *testing.T, store *Store, path, original string) {
	t.Helper()
	if store == nil {
		t.Fatalf("a quarantined store came back without a handle")
	}
	signal := store.Quarantine()
	if signal == nil || signal.QuarantinedFile != path || signal.CustodyFile == "" || signal.QuarantineEpoch == 0 {
		t.Fatalf("Quarantine() = %+v, want the signal naming %s and its custody file", signal, path)
	}
	_, aside := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
	if string(aside) != original {
		t.Fatalf("the aside file does not hold the corrupt bytes verbatim:\n got %s\nwant %s", aside, original)
	}
}

// decodeCustodyBody decodes the custody file's top level as a raw key map so a
// test can pin the schema key-for-key rather than through the struct the code
// writes.
func decodeCustodyBody(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("custody file is not a JSON object: %v\n%s", err, body)
	}
	return top
}

func sameKeySet(t *testing.T, got []string, want ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
	}
	if len(got) != len(want) {
		t.Fatalf("key set %v, want %v", got, want)
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("key set %v is missing %q, want %v", got, name, want)
		}
	}
}

// TestQuarantineIsCustodyFirstAndNeverDeletesTheCorruptFile pins §4's order and
// the artifacts it leaves: the intent and custody file land beside the store
// (mode 0600), the corrupt file is renamed aside — never deleted — and the
// replacement store serves with the custody imports.
func TestQuarantineIsCustodyFirstAndNeverDeletesTheCorruptFile(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	original := mustReadFile(t, path)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a corrupt store: err = %v, want the custody-first quarantine", err)
	}

	dir := filepath.Dir(path)
	asidePath, asideBytes := quarantineArtifact(t, dir, ".quarantined-")
	if !bytes.Equal(asideBytes, original) {
		t.Fatalf("the aside file %s does not hold the corrupt file's bytes verbatim:\n got %s\nwant %s",
			asidePath, asideBytes, original)
	}
	custodyPath, custodyBytes := quarantineArtifact(t, dir, ".custody-")
	epochPath := quarantineEpochPath(path)
	epochBytes := mustReadFile(t, epochPath)

	for name, filePath := range map[string]string{
		"custody":     custodyPath,
		"epoch":       epochPath,
		"aside":       asidePath,
		"replacement": path,
	} {
		info, err := os.Stat(filePath)
		if err != nil {
			t.Fatalf("Stat(%s %s): %v", name, filePath, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s %s mode = %04o, want 0600", name, filePath, got)
		}
	}
	if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the completed quarantine left its intent behind (stat err = %v)", err)
	}
	if temps := leftoverTemps(t, dir); len(temps) > 0 {
		t.Fatalf("the quarantine left temp files behind: %v", temps)
	}

	// The replacement store the handle serves is the custody import set.
	binding, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatalf("the fence import %q is missing from the replacement store: %+v", "00000000000000000001", store.Records())
	}
	if binding.State != StateOrphanUnverified || binding.Host != "h1" || binding.Kind != KindRestart ||
		binding.Generation != 3 || binding.IncarnationID != "inc-h1" || binding.ClientOperationID != "op-h1" {
		t.Fatalf("the fence import = %+v, want the custodial identity under its original id", binding)
	}
	ownership, ok := store.Record("00000000000000000003")
	if !ok {
		t.Fatalf("the ownership-only import %q is missing from the replacement store: %+v", "00000000000000000003", store.Records())
	}
	if ownership.State != StateOrphanUnverified || ownership.Host != "h2" || ownership.Kind != KindRestart {
		t.Fatalf("the ownership-only import = %+v, want an orphan-unverified restart record for h2", ownership)
	}

	// Zero outstanding tokens, no mirrored boundaries, no history: the store
	// starts otherwise empty.
	state := storeSnapshotForTest(store)
	if len(state.Tokens) != 0 || len(state.Tombstones) != 0 || len(state.Boundaries) != 0 ||
		len(state.RemovedHosts) != 0 || state.Sequence != 0 || state.CompactSeq != 0 {
		t.Fatalf("the replacement store did not start empty: %+v", state)
	}
	if got, want := store.CursorEpoch().QuarantineEpoch, uint64(1); got != want {
		t.Fatalf("the replacement store's quarantine epoch = %d, want %d", got, want)
	}
	if got, want := store.CursorEpoch().CompactSeq, uint64(0); got != want {
		t.Fatalf("the replacement store's compactSeq = %d, want %d", got, want)
	}
	if got := state.AllocatorHighWaterMark; got != 3 {
		t.Fatalf("the replacement allocator high-water mark = %d, want 3 (above the custodial 2)", got)
	}
	signal := store.Quarantine()
	if signal == nil || signal.QuarantinedFile != path || signal.CustodyFile != custodyPath || signal.QuarantineEpoch != 1 {
		t.Fatalf("Quarantine() = %+v, want the operator-visible signal naming %s and %s at epoch 1", signal, path, custodyPath)
	}

	// The epoch is persisted outside the replaceable store file: the store file
	// itself carries no such key, and a fresh open still reads it.
	if strings.Contains(string(mustReadFile(t, path)), "quarantineEpoch") {
		t.Fatalf("the replacement store file carries a quarantineEpoch key; the counter must live outside it")
	}
	var sidecar struct {
		QuarantineEpoch uint64 `json:"quarantineEpoch"`
	}
	if err := json.Unmarshal(epochBytes, &sidecar); err != nil {
		t.Fatalf("the epoch sidecar is not JSON: %v\n%s", err, epochBytes)
	}
	if sidecar.QuarantineEpoch != 1 {
		t.Fatalf("the epoch sidecar = %d, want 1\n%s", sidecar.QuarantineEpoch, epochBytes)
	}
	reopened := reopenFresh(t, path)
	if got := reopened.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("a fresh open after the quarantine reads epoch %d, want 1", got)
	}
	if !strings.Contains(string(custodyBytes), `"quarantineEpoch":1`) {
		t.Fatalf("the custody file does not carry epoch 1:\n%s", custodyBytes)
	}
}

// TestQuarantineCustodySchema pins §4's custody schema field-for-field.
func TestQuarantineCustodySchema(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreWithRemoteFenceJSON)
	if _, err := Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	custodyPath, body := quarantineArtifact(t, filepath.Dir(path), ".custody-")
	top := decodeCustodyBody(t, body)
	sameKeySet(t, keysFromMap(top),
		"quarantineEpoch", "quarantinedFile", "custodiedAt", "recordIds", "allocatorHighWaterMark", "fences", "ownership")

	var quarantinedFile string
	if err := json.Unmarshal(top["quarantinedFile"], &quarantinedFile); err != nil {
		t.Fatalf("quarantinedFile: %v", err)
	}
	if quarantinedFile != path {
		t.Fatalf("quarantinedFile = %q, want %q", quarantinedFile, path)
	}
	var custodiedAt string
	if err := json.Unmarshal(top["custodiedAt"], &custodiedAt); err != nil {
		t.Fatalf("custodiedAt: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, custodiedAt); err != nil {
		t.Fatalf("custodiedAt = %q, want an RFC3339 instant: %v", custodiedAt, err)
	}
	var hwm uint64
	if err := json.Unmarshal(top["allocatorHighWaterMark"], &hwm); err != nil || hwm != 3 {
		t.Fatalf("allocatorHighWaterMark = %d (err %v), want the pre-quarantine maximum 3", hwm, err)
	}

	var recordIDs []struct {
		RecordID string `json:"recordId"`
		Host     string `json:"host"`
	}
	if err := json.Unmarshal(top["recordIds"], &recordIDs); err != nil {
		t.Fatalf("recordIds: %v", err)
	}
	var wantIDs []string
	for _, row := range recordIDs {
		wantIDs = append(wantIDs, row.RecordID+"="+row.Host)
	}
	if strings.Join(wantIDs, ",") != "00000000000000000001=h1,00000000000000000003=h3,00000000000000000004=h2" {
		t.Fatalf("recordIds = %v, want every imported record's id verbatim", wantIDs)
	}

	var fences []map[string]json.RawMessage
	if err := json.Unmarshal(top["fences"], &fences); err != nil {
		t.Fatalf("fences: %v", err)
	}
	if len(fences) != 2 {
		t.Fatalf("fences holds %d entries, want one per open orphan-unverified record\n%s", len(fences), top["fences"])
	}
	for _, fence := range fences {
		// The retired boundary/quarantine payload is never re-emitted.
		sameKeySet(t, keysFromMap(fence), "recordId", "host", "kind", "clientOperationId",
			"generation", "incarnationId")
	}

	var ownerships []map[string]json.RawMessage
	if err := json.Unmarshal(top["ownership"], &ownerships); err != nil {
		t.Fatalf("ownership: %v", err)
	}
	if len(ownerships) != 3 {
		t.Fatalf("ownership holds %d entries, want one per name the file yielded\n%s", len(ownerships), top["ownership"])
	}
	for _, own := range ownerships {
		sameKeySet(t, keysFromMap(own), "quarantineRecordId", "host", "kind", "clientOperationId",
			"generation", "highWaterMark", "incarnationId")
	}
	var h2 map[string]json.RawMessage
	for _, own := range ownerships {
		var host string
		if err := json.Unmarshal(own["host"], &host); err != nil {
			t.Fatalf("ownership host: %v", err)
		}
		if host == "h2" {
			h2 = own
		}
	}
	if h2 == nil {
		t.Fatalf("no ownership entry for h2\n%s", top["ownership"])
	}
	var kind string
	if err := json.Unmarshal(h2["kind"], &kind); err != nil || kind != "restart" {
		t.Fatalf("h2 ownership kind = %q (err %v), want %q", kind, err, "restart")
	}
	var highWater, generation uint64
	if err := json.Unmarshal(h2["highWaterMark"], &highWater); err != nil {
		t.Fatalf("h2 highWaterMark: %v", err)
	}
	if err := json.Unmarshal(h2["generation"], &generation); err != nil {
		t.Fatalf("h2 generation: %v", err)
	}
	if highWater != 7 || generation != 7 {
		t.Fatalf("h2 ownership pair = generation %d highWaterMark %d, want the name's high-water 7", generation, highWater)
	}
	var quarantineRecordID string
	if err := json.Unmarshal(h2["quarantineRecordId"], &quarantineRecordID); err != nil {
		t.Fatalf("h2 quarantineRecordId: %v", err)
	}
	if quarantineRecordID != "00000000000000000004" {
		t.Fatalf("h2 quarantineRecordId = %q, want the fresh controller-assigned id above the high-water mark", quarantineRecordID)
	}
	// The custody file the recovery path reads is the one this test just pinned.
	if _, err := readCustodyFile(afero.NewOsFs(), custodyPath, path); err != nil {
		t.Fatalf("the written custody file failed the reader's own completeness check: %v", err)
	}
}

// TestQuarantineFailsStartupWhenCustodyIsIncomplete pins §4's all-or-nothing
// rule: any shortfall — an unparseable file, a record that fails validation, a
// gap below the file's own allocator high-water mark, an invalid boundary
// mirror — refuses startup without renaming or rewriting anything.
func TestQuarantineFailsStartupWhenCustodyIsIncomplete(t *testing.T) {
	record := `{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"deploy","state":"pending",` +
		`"generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}`
	// Every fixture below is a store-level corruption — the strict decode
	// succeeds and every record parses — whose custody snapshot is incomplete in
	// exactly one way. The store-level corruption is what routes the file into
	// the quarantine path at all; without it the file is corrupt in no way this
	// store refuses.
	terminal := func(id string, stamp uint64) string {
		return `{"id":"` + id + `","clientOperationId":"op-` + id + `","host":"h9","kind":"deploy","state":"complete",` +
			`"generation":7,"incarnationId":"inc-2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
			`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":` + strconv.FormatUint(stamp, 10) + `}`
	}
	cases := map[string]string{
		"truncated json": `{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[`,
		"unknown field":  `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"futureKey":0}`,
		"version 2":      `{"version":2,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		"invalid record": `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
			strings.Replace(record, `"pending"`, `"queued"`, 1) + `,` + terminal("00000000000000000002", 2) + `]}`,
		// A gap below the high-water mark: id 2 is missing with no removal
		// evidence, so the file cannot show its record set whole. (The file is
		// corrupt independently: the store sequence sits below record 2's stamp.)
		"gap below hwm": `{"version":1,"sequence":1,"allocatorHighWaterMark":3,"records":[` + record + `,` +
			terminal("00000000000000000003", 2) + `]}`,
		"residue above hwm": `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[` + record + `]}`,
		"invalid boundary mirror": `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` + record + `,` +
			terminal("00000000000000000002", 2) + `],` +
			`"boundaries":{"h9":{"generation":0,"incarnationId":"","presenceEpoch":0}}}`,
		// Removal evidence evicted: id 1 is accounted for by nothing, and the
		// file cannot prove it was a compacted record rather than a lost one.
		"evicted removal evidence": `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
			terminal("00000000000000000002", 2) + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			before := mustReadFile(t, path)
			store, err := Open(path)
			if store != nil {
				t.Fatalf("Open on an incomplete-custody store returned a handle: %v", store)
			}
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Open = %v, want an ErrStoreCorrupt refusal", err)
			}
			if !errors.Is(err, ErrQuarantineIncomplete) {
				t.Fatalf("Open = %v, want the refusal to name ErrQuarantineIncomplete", err)
			}
			if got := mustReadFile(t, path); !bytes.Equal(got, before) {
				t.Fatalf("a refused quarantine rewrote the store file:\n got %s\nwant %s", got, before)
			}
			dir := filepath.Dir(path)
			for _, infix := range []string{".quarantined-", ".custody-"} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatalf("ReadDir: %v", err)
				}
				for _, entry := range entries {
					if strings.Contains(entry.Name(), infix) {
						t.Fatalf("a refused quarantine wrote %s", entry.Name())
					}
				}
			}
			if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused quarantine wrote an intent (stat err = %v)", err)
			}
		})
	}
}

// TestQuarantineRecoversACrashBeforeTheRename pins the first crash window §4
// names: the intent and custody landed, the rename did not. The next boot boots
// covered by re-running the rename and serving the replacement — with the epoch
// advanced exactly once.
func TestQuarantineRecoversACrashBeforeTheRename(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state")
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := afero.WriteFile(fs, path, []byte(quarantinedStoreJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	crash := storeFaults{afterCustodyWrite: func() error { return errors.New("crash before the rename") }}
	if store, err := openFS(fs, path, crash); err == nil {
		t.Fatalf("openFS across the crash window = %v, want the injected failure", store)
	}
	if _, err := lstat(fs, path); err != nil {
		t.Fatalf("the corrupt file left its path before the rename: %v", err)
	}
	intentBytes, err := afero.ReadFile(fs, quarantineIntentPath(path))
	if err != nil {
		t.Fatalf("the intent did not survive the crash: %v", err)
	}
	var intent quarantineIntent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatalf("intent is not JSON: %v", err)
	}
	if intent.CorruptFile != path || intent.CustodyFile == "" || intent.AsideFile == "" || intent.QuarantinedAt.IsZero() {
		t.Fatalf("the intent = %+v, want the corrupt file, custody file and boot timestamp", intent)
	}
	if intent.QuarantineEpoch != 1 {
		t.Fatalf("the intent carries epoch %d, want 1", intent.QuarantineEpoch)
	}
	forgetStore(path)

	store, err := openFS(fs, path, storeFaults{})
	if err != nil {
		t.Fatalf("the recovery boot refused: %v", err)
	}
	if !strings.HasPrefix(intent.AsideFile, path+".quarantined-") {
		t.Fatalf("the aside file %q is not the store renamed aside", intent.AsideFile)
	}
	aside, err := afero.ReadFile(fs, intent.AsideFile)
	if err != nil {
		t.Fatalf("ReadFile(aside): %v", err)
	}
	if string(aside) != quarantinedStoreJSON {
		t.Fatalf("the recovered aside does not hold the corrupt bytes verbatim:\n%s", aside)
	}
	if _, ok := store.Record("00000000000000000001"); !ok {
		t.Fatalf("the recovered replacement is missing the fence import: %+v", store.Records())
	}
	if got := store.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("the recovered epoch = %d, want exactly one advance from 0", got)
	}
	var sidecar struct {
		QuarantineEpoch uint64 `json:"quarantineEpoch"`
	}
	epochBytes, err := afero.ReadFile(fs, quarantineEpochPath(path))
	if err != nil {
		t.Fatalf("ReadFile(epoch): %v", err)
	}
	if err := json.Unmarshal(epochBytes, &sidecar); err != nil || sidecar.QuarantineEpoch != 1 {
		t.Fatalf("the epoch sidecar = %+v (err %v), want 1", sidecar, err)
	}
	if _, err := lstat(fs, quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the completed recovery left its intent behind (stat err = %v)", err)
	}
	// A second fresh open sees no pending work and the same epoch.
	forgetStore(path)
	again, err := openFS(fs, path, storeFaults{})
	if err != nil {
		t.Fatalf("second fresh open: %v", err)
	}
	if got := again.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("second fresh open reads epoch %d, want 1 (never two advances for one quarantine)", got)
	}
}

// TestQuarantineRecoversACrashBeforeTheReplacementOpen pins the second crash
// window: the rename landed, the replacement store did not. The next boot opens
// the replacement from the custody file alone.
func TestQuarantineRecoversACrashBeforeTheReplacementOpen(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state")
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := afero.WriteFile(fs, path, []byte(quarantinedStoreJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	crash := storeFaults{afterRename: func() error { return errors.New("crash before the replacement opened") }}
	if store, err := openFS(fs, path, crash); err == nil {
		t.Fatalf("openFS across the crash window = %v, want the injected failure", store)
	}
	intentBytes, err := afero.ReadFile(fs, quarantineIntentPath(path))
	if err != nil {
		t.Fatalf("the intent did not survive the crash: %v", err)
	}
	var intent quarantineIntent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatalf("intent is not JSON: %v", err)
	}
	if _, err := lstat(fs, path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the store path still exists after the rename (stat err = %v)", err)
	}
	if _, err := lstat(fs, intent.AsideFile); err != nil {
		t.Fatalf("the aside file is missing after the rename: %v", err)
	}
	forgetStore(path)

	store, err := openFS(fs, path, storeFaults{})
	if err != nil {
		t.Fatalf("the recovery boot refused: %v", err)
	}
	if _, ok := store.Record("00000000000000000001"); !ok {
		t.Fatalf("the recovered replacement is missing the fence import: %+v", store.Records())
	}
	if _, ok := store.Record("00000000000000000003"); !ok {
		t.Fatalf("the recovered replacement is missing the ownership-only import: %+v", store.Records())
	}
	if got := store.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("the recovered epoch = %d, want 1", got)
	}
	reopened := mustReadFSFile(t, fs, path)
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	if _, err := openFS(fs, path, storeFaults{}); err != nil {
		t.Fatalf("third fresh open: %v", err)
	}
	if len(reopened) == 0 {
		t.Fatalf("the recovered replacement store file is empty")
	}
}

// TestQuarantineRecoversACrashAfterTheReplacementWrite pins the window after the
// replacement store landed but before the intent was cleared: the store file
// loads cleanly, so the boot serves it as-is (no second advance, no re-import)
// and clears the stale intent.
func TestQuarantineRecoversACrashAfterTheReplacementWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state")
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := afero.WriteFile(fs, path, []byte(quarantinedStoreJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	crash := storeFaults{beforeIntentClear: func() error { return errors.New("crash before the intent cleared") }}
	if store, err := openFS(fs, path, crash); err == nil {
		t.Fatalf("openFS across the crash window = %v, want the injected failure", store)
	}
	replacement := mustReadFSFile(t, fs, path)
	forgetStore(path)

	store, err := openFS(fs, path, storeFaults{})
	if err != nil {
		t.Fatalf("the recovery boot refused: %v", err)
	}
	if got := mustReadFSFile(t, fs, path); !bytes.Equal(got, replacement) {
		t.Fatalf("the recovery boot rewrote the replacement store:\n got %s\nwant %s", got, replacement)
	}
	if got := store.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("the recovery boot advanced the epoch to %d, want 1", got)
	}
	if _, err := lstat(fs, quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the recovery boot left the intent behind (stat err = %v)", err)
	}
}

// TestQuarantineFailsStartupWhenTheIntentHasNoCompleteCustody pins the two
// fail-closed recovery states: an intent whose custody never landed, and a
// quarantined-aside file with no complete custody beside it. Neither may serve.
func TestQuarantineFailsStartupWhenTheIntentHasNoCompleteCustody(t *testing.T) {
	t.Run("intent without custody", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := StorePath("/state")
		if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := afero.WriteFile(fs, path, []byte(quarantinedStoreJSON), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := fs.Chmod(path, 0o600); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		crash := storeFaults{afterIntentWrite: func() error { return errors.New("crash before the custody write") }}
		if store, err := openFS(fs, path, crash); err == nil {
			t.Fatalf("openFS = %v, want the injected failure", store)
		}
		forgetStore(path)

		store, err := openFS(fs, path, storeFaults{})
		if store != nil {
			t.Fatalf("Open with an intent but no custody returned a handle")
		}
		if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("Open = %v, want an ErrStoreCorrupt/ErrQuarantineIncomplete refusal", err)
		}
		if _, err := lstat(fs, path); err != nil {
			t.Fatalf("the refused boot renamed the corrupt file aside: %v", err)
		}
	})

	t.Run("aside without complete custody", func(t *testing.T) {
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("Open: %v", err)
		}
		dir := filepath.Dir(path)
		custodyPath, _ := quarantineArtifact(t, dir, ".custody-")
		if err := os.Remove(custodyPath); err != nil {
			t.Fatalf("Remove(custody): %v", err)
		}
		forgetStore(path)

		store, err := Open(path)
		if store != nil {
			t.Fatalf("Open with an aside file and no custody returned a handle")
		}
		if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("Open = %v, want an ErrStoreCorrupt/ErrQuarantineIncomplete refusal", err)
		}
	})
}

// TestQuarantineRefusesAPreQuarantineCursor pins §4's cursor rule end to end: a
// cursor minted against the pre-quarantine store is a typed stale-entry re-list
// refusal against the replacement, because the live epoch moved.
func TestQuarantineRefusesAPreQuarantineCursor(t *testing.T) {
	store, path := openTestStore(t)
	createTestRecord(t, store, "h1")
	mirrorCursorBoundary(t, store, "h1", 7, "incarnation-h1", 1)
	page, err := store.ReadOperations(OperationsQuery{Host: "h1"})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	preQuarantine, err := DecodeCursor(page.NextCursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if preQuarantine.QuarantineEpoch != 0 {
		t.Fatalf("the pre-quarantine cursor pins epoch %d, want 0", preQuarantine.QuarantineEpoch)
	}

	// The store file rots on disk behind the handle's back.
	writeRawStore(t, path, 0o600, corruptSequenceFile(t))
	forgetStore(path)

	replacement, err := Open(path)
	if err != nil {
		t.Fatalf("Open after the corruption: %v", err)
	}
	if got := replacement.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("the replacement epoch = %d, want 1", got)
	}
	_, err = replacement.ReadOperations(OperationsQuery{Host: "h1", Cursor: page.NextCursor})
	if err == nil {
		t.Fatalf("a pre-quarantine cursor was admitted against the replacement store")
	}
	var stale *CursorStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("ReadOperations = %v, want a typed stale-entry refusal", err)
	}
	if !strings.Contains(stale.Reason, "quarantine epoch") {
		t.Fatalf("the refusal reason = %q, want it to name the quarantine epoch", stale.Reason)
	}
}

// TestSecondQuarantineAdvancesTheEpochExactlyOnce pins the durable counter
// across two quarantines: each corrupt file takes its own custody snapshot and
// aside file, and the epoch moves by exactly one per quarantine, so the stop
// between them is one epoch apart and no cursor minted at the first survives the
// second.
func TestSecondQuarantineAdvancesTheEpochExactlyOnce(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if got := first.CursorEpoch().QuarantineEpoch; got != 1 {
		t.Fatalf("the first quarantine's epoch = %d, want 1", got)
	}
	// The replacement store rots on disk; the second corrupt file is quarantined
	// in its own custody-first order.
	writeRawStore(t, path, 0o600, corruptSequenceFile(t))
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if got := second.CursorEpoch().QuarantineEpoch; got != 2 {
		t.Fatalf("the second quarantine's epoch = %d, want exactly 2", got)
	}
	if signal := second.Quarantine(); signal == nil || signal.QuarantineEpoch != 2 {
		t.Fatalf("Quarantine() = %+v, want the newest custody at epoch 2", signal)
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var asides, custodies int
	for _, entry := range entries {
		switch {
		case strings.Contains(entry.Name(), ".quarantined-"):
			asides++
		case strings.Contains(entry.Name(), ".custody-"):
			custodies++
		}
	}
	if asides != 2 || custodies != 2 {
		t.Fatalf("after two quarantines the directory holds %d asides and %d custody files, want 2 each: %v", asides, custodies, entries)
	}
}

// corruptSequenceFile rewrites a valid one-record store into the fixture §4's
// quarantine path is built around: the record set stays whole, the store-level
// sequence value drops below the stamp its terminal record carries.
func corruptSequenceFile(t *testing.T) string {
	t.Helper()
	return `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"incarnation-h1","createdAt":"2026-09-26T00:00:00Z",` +
		`"updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`
}

// TestAssembleCustodyRefusesAnUnbackedFence pins the completeness check's
// ownership clause directly: a fence entry whose host carries no ownership entry
// cannot be imported, so no custody snapshot may be written from it.
func TestAssembleCustodyRefusesAnUnbackedFence(t *testing.T) {
	fence := custodyFence{
		RecordID:          "00000000000000000001",
		Host:              "h1",
		Kind:              KindRestart,
		ClientOperationID: "op-h1",
		Generation:        3,
		IncarnationID:     "inc-h1",
		Boundary:          json.RawMessage(`[{"kind":"local-markerless","platform":"linux","nonce":"n"}]`),
	}
	_, err := assembleCustody(custodyFile{
		QuarantineEpoch:        1,
		QuarantinedFile:        "/state/hostops/operations.json",
		CustodiedAt:            time.Now().UTC(),
		AllocatorHighWaterMark: 1,
		RecordIDs:              []custodyRecordID{{RecordID: fence.RecordID, Host: fence.Host}},
		Fences:                 []custodyFence{fence},
	})
	if err == nil {
		t.Fatalf("assembleCustody accepted a fence with no ownership entry")
	}
	if !errors.Is(err, ErrQuarantineIncomplete) {
		t.Fatalf("assembleCustody = %v, want ErrQuarantineIncomplete", err)
	}
}

// TestQuarantineFailsStartupWhenTheCustodyFileIsMalformed pins the recovery
// reader: a custody file that is not the schema is not a complete custody, so a
// boot that depends on it refuses.
func TestQuarantineFailsStartupWhenTheCustodyFileIsMalformed(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := StorePath("/state")
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := afero.WriteFile(fs, path, []byte(quarantinedStoreJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	crash := storeFaults{afterRename: func() error { return errors.New("crash") }}
	if _, err := openFS(fs, path, crash); err == nil {
		t.Fatalf("openFS across the crash window succeeded, want the injected failure")
	}
	custodyPath, custodyBytes := func() (string, []byte) {
		entries, err := afero.ReadDir(fs, filepath.Dir(path))
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".custody-") {
				body, err := afero.ReadFile(fs, filepath.Join(filepath.Dir(path), entry.Name()))
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				return filepath.Join(filepath.Dir(path), entry.Name()), body
			}
		}
		t.Fatalf("no custody file found")
		return "", nil
	}()
	_ = custodyBytes
	// The custody file is replaced by a schema-invalid body.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(custodyBytes, &parsed); err != nil {
		t.Fatalf("custody: %v", err)
	}
	parsed["unexpected"] = json.RawMessage(`1`)
	malformed, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := afero.WriteFile(fs, custodyPath, malformed, 0o600); err != nil {
		t.Fatalf("WriteFile(custody): %v", err)
	}
	forgetStore(path)

	store, err := openFS(fs, path, storeFaults{})
	if store != nil {
		t.Fatalf("Open over a malformed custody file returned a handle")
	}
	if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
		t.Fatalf("Open = %v, want an ErrStoreCorrupt/ErrQuarantineIncomplete refusal", err)
	}
}

// keysFromMap lists a decoded map's keys.
func keysFromMap(raw map[string]json.RawMessage) []string {
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	return names
}

// storeSnapshotForTest returns a copy of the store's in-memory state.
func storeSnapshotForTest(store *Store) snapshot {
	store.cell.mu.Lock()
	defer store.cell.mu.Unlock()
	return cloneSnapshot(store.cell.state)
}

// mustReadFSFile reads a file through an afero filesystem.
func mustReadFSFile(t *testing.T, fs afero.Fs, path string) []byte {
	t.Helper()
	body, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return body
}
