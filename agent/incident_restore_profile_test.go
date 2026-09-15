package agent

// Opt-in diagnostic harness: never runs in the default suite. The runner places
// the test binary in a network-less mount namespace with copied state only.
import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type incidentIdentity struct {
	Seq  int
	Kind schema.TurnKind
	ID   string
}

func TestIncidentRestoreProfile(t *testing.T) {
	if os.Getenv("EVENER_INCIDENT_PROFILE") != "1" {
		t.Skip("isolated incident profile opt-in")
	}
	if os.Getenv("EVENER_INCIDENT_ISOLATED") != "1" {
		t.Fatal("run through isolated runner")
	}
	root, out := os.Getenv("EVENER_INCIDENT_STATE"), os.Getenv("EVENER_INCIDENT_OUTPUT")
	id := os.Getenv("EVENER_INCIDENT_SID")
	path := filepath.Join(root, "sessions", id+".transcript.jsonl")
	// Independent oracle: stdlib JSON decoding, not transcript.DecodeEntry.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	scan := bufio.NewScanner(io.TeeReader(f, h))
	scan.Buffer(make([]byte, 65536), transcript.DefaultMaxLineBytes+1)
	var ids []incidentIdentity
	var header struct {
		SessionID string `json:"session_id"`
	}
	if !scan.Scan() {
		t.Fatal("missing header")
	}
	if err := json.Unmarshal(scan.Bytes(), &header); err != nil {
		t.Fatal(err)
	}
	for scan.Scan() {
		var e struct {
			Seq  int `json:"seq"`
			Turn struct {
				Kind         schema.TurnKind `json:"kind"`
				StableTurnID string          `json:"stable_turn_id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, incidentIdentity{e.Seq, e.Turn.Kind, e.Turn.StableTurnID})
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if header.SessionID != id {
		t.Fatal("input header ownership mismatch")
	}
	inputHash := h.Sum(nil)
	inputInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("input session=%s entries=%d sha256=%s", id, len(ids), hex.EncodeToString(h.Sum(nil)))
	measure := func(name string, run func()) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu, err := os.Create(filepath.Join(out, name+".cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		pprof.Do(context.Background(), pprof.Labels("stage", name), func(context.Context) { run() })
		elapsed := time.Since(start)
		pprof.StopCPUProfile()
		if err := cpu.Close(); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		t.Logf("stage=%s wall=%s allocated_bytes=%d mallocs=%d heap_alloc=%d", name, elapsed, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, after.HeapAlloc)
		// Allocation samples are process-cumulative; only MemStats deltas above
		// are stage-local (and include profiling overhead).
		mem, err := os.Create(filepath.Join(out, name+".cumulative-allocs.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.Lookup("allocs").WriteTo(mem, 0); err != nil {
			t.Fatal(err)
		}
		if err := mem.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var meta schema.SessionMeta
	measure("metadata", func() {
		var err error
		meta, err = schema.LoadSessionMeta(root, id)
		if err != nil {
			t.Fatal(err)
		}
	})
	if meta.ID != id {
		t.Fatal("metadata ownership mismatch")
	}
	var entries []transcript.Entry
	var writer *transcript.Writer
	measure("transcript-open", func() {
		var err error
		writer, entries, err = transcript.OpenWriterForSession(path, id)
		if err != nil {
			t.Fatal(err)
		}
	})
	checkIDs := func(entries []transcript.Entry) {
		got := make([]incidentIdentity, len(entries))
		for i, e := range entries {
			got[i] = incidentIdentity{e.Seq, e.Turn.Kind, e.Turn.StableTurnID}
		}
		if !reflect.DeepEqual(got, ids) {
			t.Fatalf("transcript identity mismatch: got=%d want=%d", len(got), len(ids))
		}
	}
	checkIDs(entries)
	entryDigests := func(entries []transcript.Entry) [][sha256.Size]byte {
		digests := make([][sha256.Size]byte, len(entries))
		for i, entry := range entries {
			b, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			digests[i] = sha256.Sum256(b)
		}
		return digests
	}
	originalEntryDigests := entryDigests(entries)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var history []schema.Turn
	measure("resume-history", func() { history = ResumeHistory(entries) })
	t.Logf("history_count=%d model_responses=%d accepted_inputs=%d", len(history), meta.TurnCount, meta.AcceptedInputTurns)
	if os.Getenv("EVENER_INCIDENT_FULL_RESTORE") != "1" {
		return
	}
	store, err := delegatestore.Open(filepath.Join(root, "sessions", id, "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	durable, err := delegatestore.Fold(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	phaseCounts := make(map[delegatestore.Phase]int)
	missing := 0
	for delegateID, a := range durable {
		phaseCounts[a.Phase]++
		childID := a.Descriptor.ChildSessionID
		if childID == "" {
			continue
		}
		child, err := schema.LoadSessionMeta(root, childID)
		if err != nil || child.ID != childID {
			t.Logf("missing/corrupt descendant metadata delegate=%s session=%s", delegateID, childID)
			missing++
			continue
		}
		if _, err := validateStrictChildTranscript(filepath.Join(root, "sessions", childID+".transcript.jsonl"), childID, 0); err != nil {
			t.Logf("missing/corrupt descendant transcript delegate=%s session=%s", delegateID, childID)
			missing++
			continue
		}
		// Recreate ONLY the directory existence contract inside the empty mount
		// namespace. No live working-tree content is mounted or inspected.
		if wd := a.Descriptor.WorkingDir; wd != "" {
			if err := os.MkdirAll(wd, 0700); err != nil {
				t.Fatalf("namespace environment cannot recreate child working directory: %v", err)
			}
			if info, err := os.Stat(wd); err != nil || !info.IsDir() {
				t.Fatalf("namespace child working-directory prerequisite failed: %v", err)
			}
		}
	}
	if missing != 0 {
		t.Fatalf("descendant closure incomplete: %d", missing)
	}
	t.Logf("canonical descendant closure=%d primary_phases=%v", len(durable), phaseCounts)
	// Only external startup configuration is changed. Durable conversations,
	// journals, metadata counters, and identities stay at the primary input.
	meta.Config.PluginDirs = nil
	meta.Config.NoProjectPrompts = true
	var session *Session
	measure("full-restore", func() {
		var err error
		session, err = RestoreSessionFromMetaWithConfig(llm.NewClient(), NewOpenAIProfile(meta.Model), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: root, ForceRealIO: true, deferRestoreSideEffects: true, testOnly: testConfig{skipGitSnapshot: true}})
		if err != nil {
			t.Fatal(err)
		}
	})
	defer session.Close()
	// No ownership callback, host daemon, ProcessInput, or Run invocation exists.
	// Thus this runtime owns only its private copy; there is no external claim.
	if session.ID() != id || session.stateDir != root || session.cfg.StateDir != root || session.cfg.AcquireSessionOwnership != nil {
		t.Fatal("restore ownership invariant")
	}
	if !session.ownsDelegateController || session.delegateRootSessionID != id || session.delegateController.stateDir != root {
		t.Fatal("restore delegate controller ownership invariant")
	}
	rh, re, ok := session.RestoredTranscript()
	if !ok || rh.SessionID != id {
		t.Fatal("retained transcript ownership mismatch")
	}
	// Bootstrap may append legitimate crash recovery entries; original identity
	// prefix must remain unchanged, and final entries are checked against disk.
	if len(re) < len(ids) {
		t.Fatal("restore dropped durable entries")
	}
	checkIDs(re[:len(ids)])
	if !reflect.DeepEqual(entryDigests(re[:len(ids)]), originalEntryDigests) {
		t.Fatal("restore changed original durable entry content")
	}
	reloaded, err := readTranscriptFull(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Header, rh) || !reflect.DeepEqual(reloaded.Entries, re) {
		t.Fatal("restore retained transcript differs from canonical disk reload")
	}
	prefix, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	prefixHash := sha256.New()
	if _, err := io.CopyN(prefixHash, prefix, inputInfo.Size()); err != nil {
		t.Fatal(err)
	}
	if err := prefix.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prefixHash.Sum(nil), inputHash) {
		t.Fatal("restore changed original transcript bytes")
	}
	post := session.delegateController.durable
	if len(post) != len(durable) {
		t.Fatalf("delegate count changed: before=%d after=%d", len(durable), len(post))
	}
	postPhases := make(map[delegatestore.Phase]int)
	for delegateID, before := range durable {
		after := post[delegateID]
		if after == nil || !reflect.DeepEqual(before.Descriptor, after.Descriptor) || before.Generation != after.Generation {
			t.Fatalf("delegate identity/descriptor/generation changed: %s", delegateID)
		}
		postPhases[after.Phase]++
		// Stable idle/closed delegates must not be terminalized merely because
		// the harness omitted a working directory or a descendant input.
		if !before.CurrentRunOpen && before.PendingStopSeq == 0 && before.PreparedTerminal == nil {
			if before.Phase != after.Phase || before.Resumable != after.Resumable || !reflect.DeepEqual(before.LatestOutcome, after.LatestOutcome) {
				t.Fatalf("stable delegate status changed: %s phase=%s->%s resumable=%t->%t", delegateID, before.Phase, after.Phase, before.Resumable, after.Resumable)
			}
		}
	}
	t.Logf("restored entries=%d history=%d state=%s delegates=%d phases=%v ownership_callback=nil", len(re), len(session.history), session.State(), len(post), postPhases)
}
