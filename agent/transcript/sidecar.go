package transcript

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"primeradiant.com/evener/internal/fileident"
)

// resumeSidecarVersion is the resume sidecar format version. Bump on any
// incompatible change to the shape below.
const resumeSidecarVersion = 1

// resumeSidecarAnchorBytes mirrors turnIndexAnchorBytes: the byte window each
// anchor stamps, sized to bind the sidecar to the transcript's identity
// without reading the whole prefix.
const resumeSidecarAnchorBytes = 256

// sidecarLimit bounds the sidecar file read. The sidecar is metadata plus a
// small fold snapshot; a transcript whose sidecar exceeds this limit is
// treated as corrupt (fallback to full scan), never trusted.
const resumeSidecarLimit = 1 << 20

// sidecarSuffix is the path suffix of the resume sidecar relative to its
// transcript: "<transcript>.resume-sidecar.json".
const resumeSidecarSuffix = ".resume-sidecar.json"

// SidecarPath returns the resume sidecar path for a transcript path.
func SidecarPath(transcriptPath string) string {
	return transcriptPath + resumeSidecarSuffix
}

// sidecarAnchor is a bounded byte-range stamp over the transcript, copied
// from turnIndexAnchor: one sha256 over [Offset, Offset+Length).
type sidecarAnchor struct {
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	Stamp  string `json:"stamp"`
}

// SidecarPendingAttention snapshots one delegate attention whose lifecycle
// spans the prefix boundary: the fold needs its content to resolve a suffix
// resolution turn and its disposition to answer pendingIDs correctly.
type SidecarPendingAttention struct {
	AttentionID      string      `json:"attention_id"`
	Message          JSONMessage `json:"message"`
	Resolution       string      `json:"resolution,omitempty"`
	ResumeGeneration uint64      `json:"resume_generation,omitempty"`
}

// JSONMessage preserves an llm message's JSON bytes inside the sidecar
// without the transcript package depending on the llm message shape: the
// fold re-decodes it on the agent side. Marshalling through RawMessage keeps
// the sidecar a byte-level artifact of this package.
type JSONMessage json.RawMessage

// MarshalJSON implements json.Marshaler.
func (m JSONMessage) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte("null"), nil
	}
	return m, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (m *JSONMessage) UnmarshalJSON(data []byte) error {
	*m = JSONMessage(append(json.RawMessage(nil), data...))
	return nil
}

// SidecarDeliveryCommit snapshots one committed delegate delivery.
type SidecarDeliveryCommit struct {
	DeliveryID string `json:"delivery_id"`
	ToolCallID string `json:"tool_call_id"`
}

// ResumeSidecar is the durable validated-offset sidecar for transcript
// resume. Its contract: every field describes the transcript PREFIX strictly
// before Offset, and Offset points AT the last checkpoint entry — so the
// suffix a validated windowed resume decodes is exactly ResumeHistory's
// window. A transcript without a checkpoint gets no sidecar: all its entries
// are live history, and skipping any of them would change restore behavior.
type ResumeSidecar struct {
	Version                 int    `json:"version"`
	TranscriptFormatVersion int    `json:"transcript_format_version"`
	SessionID               string `json:"session_id"`

	// TranscriptSize is the file size observed when the sidecar was written.
	// A current size smaller than this is a truncation the sidecar cannot
	// vouch for: full scan. A larger size is append-only growth, the expected
	// case between an anchor and the next resume.
	TranscriptSize int64 `json:"transcript_size"`
	// ValidBytes is TranscriptSize minus any unterminated crash tail the
	// anchor observed. The offset is bounded by it.
	ValidBytes int64 `json:"valid_bytes"`

	// Offset is the byte offset where the suffix begins — the start of the
	// last checkpoint entry, so the windowed suffix a resume decodes is
	// exactly ResumeHistory's window ([last checkpoint, ...subsequent]).
	// The entry ENDING at Offset is the last prefix entry.
	Offset int64 `json:"offset"`

	// MaxSeq is the highest entry.Seq at or before Offset.
	MaxSeq int `json:"max_seq"`
	// EntryCount is the number of entries at or before Offset.
	EntryCount int `json:"entry_count"`
	// PrefixTurnCount is the number of turn positions a full AppWire
	// projection of the whole file holds strictly below the suffix: the
	// prelude turn (when the header projects one) plus one position for
	// every prefix entry that projects at least one thread item. Only the
	// post-full-scan anchor computes it (it decoded the prefix and knows
	// which kinds projected); the compaction anchor writes -1, and a resume
	// that needs windowed turn paging then falls back rather than guessing.
	// Entries that project nothing (empty-text marker turns) are common in
	// compacted prefixes, so the figure is genuinely not derivable from
	// EntryCount.
	PrefixTurnCount int `json:"prefix_turn_count"`
	// FailureFloor is the failed-tool-call count over the prefix entries the
	// anchor observed, bounded by the same divergence rule TrackFailures
	// applies. -1 means "not computed by this anchor"; callers that need it
	// fall back to a full scan.
	FailureFloor int `json:"failure_floor"`

	// FileIdentity, ModTimeUnixNS, FirstAnchor, and TailAnchor bind the
	// sidecar to one file incarnation, mirroring turnIndexDisk's identity
	// fields. FirstAnchor stamps the first bytes of the file; TailAnchor
	// stamps the bytes immediately before Offset.
	FileIdentity  string        `json:"file_identity"`
	ModTimeUnixNS int64         `json:"mod_time_unix_ns"`
	FirstAnchor   sidecarAnchor `json:"first_anchor"`
	TailAnchor    sidecarAnchor `json:"tail_anchor"`

	// BoundarySeq cross-checks Offset: the entry that ends exactly at Offset
	// must carry this seq. A mismatching boundary record is corruption.
	BoundarySeq int `json:"boundary_seq"`

	// SnapshotsComplete reports whether the fold snapshots below were
	// computed over every entry. The post-full-scan anchor always sets it
	// (it decoded the whole file). The compaction anchor never does: it
	// cannot reconstruct the prefix's fold state without re-reading the
	// transcript, so it writes SnapshotsComplete=false and a resume that
	// NEEDS the fold falls back to the full scan rather than folding an
	// incomplete state.
	SnapshotsComplete bool `json:"snapshots_complete"`

	// Fold snapshots (valid when SnapshotsComplete).
	PendingAttention []SidecarPendingAttention `json:"pending_attention,omitempty"`
	DeliveryCommits  []SidecarDeliveryCommit   `json:"delivery_commits,omitempty"`
	// ClientMutationTurns maps mutation IDs to the stable turn IDs of their
	// prefix turns; restore consults it to avoid re-executing a mutation
	// whose turns predate the offset.
	ClientMutationTurns map[string]string `json:"client_mutation_turns,omitempty"`

	// IntegrityStamp is the sha256 of the sidecar JSON with this field
	// empty; a mismatch is corruption.
	IntegrityStamp string `json:"integrity_stamp"`
}

// sidecarIntegrityStamp computes the integrity stamp for a sidecar value.
func sidecarIntegrityStamp(sidecar ResumeSidecar) string {
	sidecar.IntegrityStamp = ""
	data, err := json.Marshal(sidecar)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadSidecar reads and structurally validates the resume sidecar for a
// transcript. It returns ok=false for a missing, unreadable, oversized,
// malformed, version-mismatched, or integrity-failing sidecar — every such
// case is a full-scan fallback, never an error to the caller.
func ReadSidecar(transcriptPath string) (sidecar ResumeSidecar, ok bool) {
	data, err := os.ReadFile(SidecarPath(transcriptPath))
	if err != nil || int64(len(data)) > resumeSidecarLimit {
		return ResumeSidecar{}, false
	}
	var candidate ResumeSidecar
	if err := json.Unmarshal(data, &candidate); err != nil {
		return ResumeSidecar{}, false
	}
	if candidate.Version != resumeSidecarVersion || candidate.TranscriptFormatVersion != FormatVersion {
		return ResumeSidecar{}, false
	}
	if candidate.IntegrityStamp == "" || candidate.IntegrityStamp != sidecarIntegrityStamp(candidate) {
		return ResumeSidecar{}, false
	}
	return candidate, true
}

// WriteSidecar persists the sidecar atomically: temp file in the same
// directory, fsync, rename. The transcript package's own durability model
// (writeTranscript's forceSync path) uses the same discipline. A write
// failure is returned to the caller; the resume fast path treats it as
// non-fatal (the next resume simply falls back).
//
// A sidecar that would exceed the read limit is refused here rather than
// written-and-forever-rejected: ReadSidecar caps the read at
// resumeSidecarLimit, so an oversized write would cycle pointlessly —
// every resume rewrites it, every read discards it. Refusing at the write
// keeps the (correct) fallback to the full scan instead.
func WriteSidecar(transcriptPath string, sidecar ResumeSidecar) error {
	sidecar.IntegrityStamp = sidecarIntegrityStamp(sidecar)
	data, err := json.Marshal(sidecar)
	if err != nil {
		return fmt.Errorf("marshal resume sidecar: %w", err)
	}
	if int64(len(data)) > resumeSidecarLimit {
		return fmt.Errorf("resume sidecar is %d bytes, over the %d read limit", len(data), resumeSidecarLimit)
	}
	path := SidecarPath(transcriptPath)
	temp, err := os.CreateTemp(filepath.Dir(path), ".resume-sidecar-*")
	if err != nil {
		return fmt.Errorf("create resume sidecar temp: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath) //nolint:errcheck // best-effort cleanup after rename/failure
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write resume sidecar: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync resume sidecar: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close resume sidecar temp: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename resume sidecar: %w", err)
	}
	return nil
}

// sidecarAnchors computes the first and tail anchors for a transcript prefix
// [0, offset). It mirrors transcriptAnchors from turn_index.go, with the tail
// anchored at the offset rather than the file end.
func sidecarAnchors(file sidecarReaderAt, offset int64) (first, tail sidecarAnchor) {
	if offset <= 0 {
		return sidecarAnchor{}, sidecarAnchor{}
	}
	firstLength := min(int64(resumeSidecarAnchorBytes), offset)
	first = sidecarAnchorAt(file, 0, int(firstLength))
	tailOffset := max(offset-int64(resumeSidecarAnchorBytes), 0)
	tail = sidecarAnchorAt(file, tailOffset, int(offset-tailOffset))
	return first, tail
}

// sidecarReaderAt is the read-at surface both anchor paths need: *os.File
// directly, or an afero.File when that is all the caller holds (the Writer's
// fs may be injected).
type sidecarReaderAt interface {
	ReadAt(b []byte, off int64) (int, error)
}

func sidecarAnchorAt(file sidecarReaderAt, offset int64, length int) sidecarAnchor {
	data := make([]byte, length)
	n, _ := file.ReadAt(data, offset)
	sum := sha256.Sum256(data[:n])
	return sidecarAnchor{Offset: offset, Length: n, Stamp: hex.EncodeToString(sum[:])}
}

// sidecarAnchorsMatch re-reads both anchor windows and compares stamps.
// Mirrors anchorsMatchObserved from turn_index.go.
func sidecarAnchorsMatch(file sidecarReaderAt, first, tail sidecarAnchor) bool {
	for _, anchor := range []sidecarAnchor{first, tail} {
		if anchor.Length <= 0 || anchor.Stamp == "" {
			return false
		}
		data := make([]byte, anchor.Length)
		n, err := file.ReadAt(data, anchor.Offset)
		if err != nil && err != io.EOF {
			return false
		}
		sum := sha256.Sum256(data[:n])
		if n != anchor.Length || hex.EncodeToString(sum[:]) != anchor.Stamp {
			return false
		}
	}
	return true
}

// sidecarFileIdentity derives the file incarnation identity through the
// shared internal/fileident package (also used by internal/apptranscript's
// turn index, which this package cannot import).
func sidecarFileIdentity(info os.FileInfo) string {
	return fileident.FileIdentity(info)
}
