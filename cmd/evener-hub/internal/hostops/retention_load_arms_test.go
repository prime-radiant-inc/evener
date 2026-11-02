package hostops

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// The persisted-value validators the loader enforces over hand-edited files:
// every tombstone, compaction-mark and removal-marker shape outside the schema
// the compacting and mirror writers emit is refused at the load, never served.
// Each row plants one malformed value in an otherwise loadable store.

// retentionArmTombstone is a schema-valid tombstone the rows bend.
const retentionArmTombstone = `{"id":"00000000000000000003","clientOperationId":"op-h3","host":"h3","kind":"deploy","state":"complete",` +
	`"generation":4,"incarnationId":"inc-h3","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1,"result":{"ok":true,"message":"done"}}`

// retentionArmStore plants a store whose one bent region is named by the row.
func retentionArmStore(bent string) string {
	return `{"version":1,"sequence":2,"allocatorHighWaterMark":2,` +
		`"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
		`"generation":3,"incarnationId":"inc-h1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
		`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` +
		bent + `}`
}

// bendTombstone rewrites one `"field":"value"` pair inside the tombstone
// fragment, so a row bends exactly one field.
func bendTombstone(t *testing.T, field, from, to string) string {
	t.Helper()
	old := `"` + field + `":"` + from + `"`
	if !strings.Contains(retentionArmTombstone, old) {
		t.Fatalf("the tombstone fixture does not carry %s", old)
	}
	return strings.Replace(retentionArmTombstone, old, `"`+field+`":"`+to+`"`, 1)
}

func TestStoreRefusesTombstonesOutsideTheSchema(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		tombstone string
	}{
		{
			name:      "a tombstone with no id",
			tombstone: bendID(t, ""),
		},
		{
			name:      "a tombstone whose id is not controller-assigned",
			tombstone: bendID(t, "abc"),
		},
		{
			name:      "a tombstone with no client operation id",
			tombstone: bendTombstone(t, "clientOperationId", "op-h3", ""),
		},
		{
			name:      "a tombstone naming no host",
			tombstone: bendTombstone(t, "host", "h3", ""),
		},
		{
			name:      "a tombstone whose incarnation id sits over the byte bound",
			tombstone: bendTombstone(t, "incarnationId", "inc-h3", strings.Repeat("i", MaxIncarnationIDBytes+1)),
		},
		{
			name:      "a tombstone with a kind outside the operation set",
			tombstone: bendTombstone(t, "kind", "deploy", "nonsense"),
		},
		{
			name:      "a tombstone carrying a non-terminal state",
			tombstone: bendTombstone(t, "state", "complete", "running"),
		},
		{
			name:      "a tombstone pinning no pair",
			tombstone: bendNumber(t, "generation", 0),
		},
		{
			name:      "a tombstone carrying no timestamps",
			tombstone: bendTombstone(t, "createdAt", "2026-09-26T00:00:00Z", "0001-01-01T00:00:00Z"),
		},
		{
			name:      "a tombstone whose compactedSeq sits outside the store's",
			tombstone: bendNumber(t, "compactedSeq", 9),
		},
		{
			name: "a tombstone carrying a progress entry with no message",
			tombstone: strings.Replace(retentionArmTombstone,
				`"compactedSeq":1,`,
				`"compactedSeq":1,"progress":[{"ts":"2026-09-26T00:00:00Z","message":""}],`, 1),
		},
		{
			name: "a tombstone whose terminal result carries no message",
			tombstone: strings.Replace(retentionArmTombstone,
				`"result":{"ok":true,"message":"done"}`,
				`"result":{"ok":true,"message":""}`, 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			body := retentionArmStore(`"compactSeq":1,"tombstones":[` + tc.tombstone + `]`)
			writeRawStore(t, path, 0o600, body)
			if err := loadAt(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want errors.Is(ErrStoreCorrupt)", err)
			}
		})
	}
}

// bendID rewrites the tombstone's id, which may become an empty or non-numeric
// value the quoted-pair helper cannot express.
func bendID(t *testing.T, id string) string {
	t.Helper()
	return bendTombstone(t, "id", "00000000000000000003", id)
}

// bendNumber rewrites one numeric field inside the tombstone fragment.
func bendNumber(t *testing.T, field string, to uint64) string {
	t.Helper()
	i := strings.Index(retentionArmTombstone, `"`+field+`":`)
	if i < 0 {
		t.Fatalf("the tombstone fixture does not carry the numeric field %q", field)
	}
	rest := retentionArmTombstone[i+len(field)+3:]
	end := strings.Index(rest, `,`)
	if end < 0 {
		end = strings.Index(rest, `}`)
	}
	return retentionArmTombstone[:i+len(field)+3] + strconv.FormatUint(to, 10) + rest[end:]
}

func TestStoreRefusesCompactionMarksOutsideTheSchema(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mark string
	}{
		{
			name: "a mark whose seq sits above the store's compactSeq",
			mark: `{"seq":9,"hosts":{"hB":"00000000000000000003"}}`,
		},
		{
			name: "a mark carrying no affected hosts",
			mark: `{"seq":1,"hosts":{}}`,
		},
		{
			name: "a mark naming an affected host with no name",
			mark: `{"seq":1,"hosts":{"":"00000000000000000003"}}`,
		},
		{
			name: "a mark naming a removed id that is not controller-assigned",
			mark: `{"seq":1,"hosts":{"hB":"abc"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			body := retentionArmStore(`"compactSeq":2,"compactionMarks":[` + tc.mark + `]`)
			writeRawStore(t, path, 0o600, body)
			original := mustReadFile(t, path)
			// The loader refuses the file (the mark's own schema arms run
			// there), but the record set is whole, so §4's custody tolerates
			// the ledger it discards: the boot quarantines and serves the
			// replacement, never bricking over ledger bit-rot.
			store, err := Open(path)
			if err != nil {
				t.Fatalf("Open refused a whole-record store over a bad compaction mark: %v", err)
			}
			wantQuarantined(t, store, path, string(original))
		})
	}
}

func TestStoreRefusesRemovalMarkersOutsideTheSchema(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		marker string
	}{
		{
			name:   "a removal marker carrying a zero removal instant",
			marker: `{"removedAt":"0001-01-01T00:00:00Z","generation":3,"incarnationId":"inc-x"}`,
		},
		{
			name:   "a removal marker pinning no pair",
			marker: `{"removedAt":"2026-09-26T00:00:00Z","generation":0,"incarnationId":""}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			body := retentionArmStore(`"removedHosts":{"hX":` + tc.marker + `}`)
			writeRawStore(t, path, 0o600, body)
			if err := loadAt(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want errors.Is(ErrStoreCorrupt)", err)
			}
		})
	}
}
