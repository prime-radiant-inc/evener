package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/transcriptindex"
)

// The #4344 regression shape: a hub serving a storm of expensive thread/reads
// must still discover a resuming daemon's rendezvous entry within its normal
// poll cadence. The incident (2026-10-11): CPU-saturated by its own read
// storm — every read paying a full inline sidecar rebuild — the hub's
// rendezvous stage took 21.5s and 27.1s to see daemons that had registered in
// ~100ms, so resume-bearing reconnects died past the client heartbeat and the
// user's sends queued forever. #4343 moved the rebuild off the read path; this
// test holds that line at the latency of the whole resume journey.
//
// The storm is a continuously re-staled sidecar, not a once-staled fixture: a
// healthy rebuild heals one refusal and every later read answers data (the
// heal-after-one property), while a flip of the sidecar's projection meta back
// to a foreign identity makes the next read refuse again. The re-staler keeps
// at least one refusal in flight across the whole measurement window, so both
// the green storm (reads answering rebuilding while a real repair runs) and
// the probe red (a reintroduced inline projection rebuilding under every read)
// stay alive for the window's duration.
//
// The probe contract this test must survive: mutate NewCacheWithOpen's open
// back to the pre-#4343 non-refusing NewCache (app_threadread.go) and the
// storm turns per-read expensive again — one shared inline rebuild (the
// cache single-flights the open; the healed handle's in-memory state makes
// the other reads fast-extend) plus every read's full-transcript response
// scan, together holding the serial worker past the resume for well over
// the bound. The queue arithmetic is load-bearing: a serial request runs
// without waiting for an in-flight slow read, so the resume only queues
// once the 16-slot slow-read cap is full (internal/appserver/server.go
// acquireSlowReadSlot) — the queued read count must exceed the cap, and
// the fixture scale must make the shared-rebuild-plus-scans window exceed
// 2x the bound. The spec's implementation notes carry the measured
// numbers this scale was tuned from.
func TestResumeRendezvousSurvivesAReadStorm(t *testing.T) {
	// Three entries per round at seedSyntheticPastSession's density
	// (~1.7KB/round) lands the transcript in the ~67MB class: the probe's
	// one shared rebuild plus 60 response scans costs well past the 4s red
	// acceptance at this scale. Under the race detector the same rebuild is
	// several times slower and the cleanup join must wait out the last
	// repair's whole recipe, so the race lane runs a smaller fixture: its
	// subject is the machinery's memory safety, not the latency bound, and
	// the mutation probe the 67MB scale exists for is a manual exercise.
	stormRounds := 40000
	if raceDetectorEnabled {
		stormRounds = 8000
	}
	const (
		// Exceeds the 16-slot slow-read cap so the worker parks, while
		// initialize + reads + resume stays at 62 frames, under the
		// 64-frame queue cap, so no send ever blocks.
		queuedReads = 60
		// Short enough that a healed sidecar is re-staled before the
		// reads drain in green; one flip yields one refusal cycle.
		restaleEvery = 100 * time.Millisecond
		// The resume's request-to-discovery bound: ~40 healthy poll
		// intervals of headroom, a tenth of the incident's starved
		// stages (21.5s/27.1s).
		discoveryBound = 2 * time.Second
	)

	// --- fixture: one project, two sessions --------------------------------
	// The storm session carries the load-bearing 67MB transcript and its
	// sidecar; the resumed session is an ordinary small past session the
	// resume can own. Both sidecars exist up front — a never-indexed session
	// is a different fixture class (its first read answers rebuilding while
	// a repair builds the very first sidecar).
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-storm-0000000000")
	workDir := t.TempDir()
	stormID := hubtest.SessionID(t)
	resumeID := hubtest.SessionID(t)
	stormPath := seedSyntheticPastSession(t, stateDir, stormID, workDir, stormRounds)
	if info, err := os.Stat(stormPath); err == nil {
		t.Logf("storm transcript: %.1f MB over %d rounds", float64(info.Size())/(1<<20), stormRounds)
	}
	indexPastTranscriptForTest(t, stateDir, stormID)
	if _, err := os.Stat(filepath.Join(transcriptindex.DirFor(stormPath), "CURRENT")); err != nil {
		t.Fatal("storm session's sidecar did not build")
	}
	buildRPCSessionWithWorkingDir(t, stateDir, resumeID, workDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// --- repair accounting ---------------------------------------------------
	// The cleanup below joins the storm's detached repairs by counting them:
	// arms counts every repair the registry starts for the storm path, and
	// recorded counts every repair whose outcome is recorded and whose
	// post-recording hook has fired — the repair goroutine's last statement.
	// The wrappers only observe: the recipe runs unchanged for every path,
	// and other paths' hooks pass through untouched.
	stormArms, stormRecorded := installRepairAccounting(t, stormPath)

	// --- fake daemon: script for spawn/rendezvous, Go server for the read ---
	// The spawned "daemon" is two pieces. The script binary answers the
	// spawner's launch-check and, on serve, writes the rendezvous entry the
	// poller discovers — the entry points at an in-process appserver that
	// answers the post-rendezvous confirm read with the resumed session's
	// identity (the shape app_session_resume_recovery_test's daemon serves).
	// A dead endpoint cannot complete a resume: every completion path reads
	// through the spawned daemon's endpoint (ReadSpawnedThread's confirm, or
	// the roster's live entry), so the fixture's daemon must actually serve.
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	daemon := appserver.NewServer(appserver.ServerConfig{SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: resumeID, SessionID: resumeID,
			Evener: appwire.EvenerThread{Ref: localAppRef(resumeID), InstanceID: resumeID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		}}, nil
	})
	peer := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(peer.Close)
	daemonEndpoint := "ws" + strings.TrimPrefix(peer.URL, "http")
	daemonAddress := strings.TrimPrefix(peer.URL, "http://")
	binary := filepath.Join(root, "fixture-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"` + daemonAddress + `","protocol":"evener-appwire-v7","source_id":"local","endpoint":"` + daemonEndpoint + `","session_id":"` + resumeID + `","thread_id":"` + resumeID + `","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	writeFakeEvener(t, binary, script)

	// The repair registry is armed by the hub server's own construction —
	// web.go wires it with the server's lifetime, the same wiring a directly
	// constructed production server uses to recover a staled sidecar on its
	// own — so the storm's refused reads arm real repairs with the real
	// recipe. The only wrapper is installRepairAccounting's observing
	// counter, which chains to the real recipe unchanged.
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Past:        past,
		ResumeLocks: hubcore.NewResumeLocks(),
		Spawner:     &HubSpawner{Cfg: DefaultConfig(), RunDir: runDir, EvenerBinary: binary},
	}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()

	// --- one real connection, ordered sends ---------------------------------
	// The typed client answers one request per call, so ordering reads ahead
	// of the resume needs the raw transport: send N reads, then the resume,
	// then match responses by id. The resume must be sent while the reads are
	// still queued behind the slow-read cap, exactly as the incident's resume
	// arrived behind a browser's read storm.
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	defer transport.Close()
	send := func(id int64, method string, params any) {
		t.Helper()
		if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(id), method, params)); err != nil {
			t.Fatalf("send %s: %v", method, err)
		}
	}
	// The resume's response arrives while read responses still stream, so a
	// receive must hold every frame — matching ones too — for its own later
	// receive: the cleanup drain below re-checks the map, and a matching
	// frame returned without being stored would leave the drain waiting for
	// an answer that already arrived, out to the context deadline.
	received := make(map[int64]appwire.Message)
	receive := func(id int64) appwire.Message {
		t.Helper()
		if m, ok := received[id]; ok {
			return m
		}
		for {
			message, err := transport.Recv(ctx)
			if err != nil {
				t.Fatalf("recv: %v", err)
			}
			switch {
			case message.Response != nil:
				received[message.Response.ID.Int64()] = message
				if message.Response.ID.Int64() == id {
					return message
				}
			case message.Error != nil:
				received[message.Error.ID.Int64()] = message
				if message.Error.ID.Int64() == id {
					return message
				}
			}
			// Notifications carry no id; skip them.
		}
	}
	send(1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if message := receive(1); message.Error != nil {
		t.Fatalf("initialize: %+v", message.Error)
	}

	// --- the re-staler -------------------------------------------------------
	// A flip racing a build rotation can hit a missing CURRENT or meta (the
	// rotation renamed them); that flip is skipped — the next tick re-reads
	// CURRENT and stales whatever is committed now.
	restale := func() error {
		_, err := flipSidecarProjection(stormPath)
		return err
	}
	if err := restale(); err != nil {
		t.Fatalf("initial stale: %v", err)
	}
	stopRestaler := make(chan struct{})
	restalerDone := make(chan struct{})
	t.Cleanup(func() {
		close(stopRestaler)
		// Join the goroutine so its last flip cannot outlive the test and
		// every error it reported is counted.
		<-restalerDone
		// The repair registry runs its rebuilds detached by design (main.go:
		// a multi-minute rebuild must not hold hub exit), so the storm's
		// last-armed repair can still be live here — including past the
		// registry's own bookkeeping, whose in-flight flag clears inside
		// run's critical section while the goroutine's post-recording hook
		// runs after it. This cleanup runs before the TempDir removal
		// (cleanup is LIFO and the TempDir was registered first), and an
		// unjoined repair would race that removal and fire its hook into
		// whatever later test has installed one.
		awaitRepairsSettled(t, stormPath, stormArms, stormRecorded)
	})
	go func() {
		defer close(restalerDone)
		ticker := time.NewTicker(restaleEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stopRestaler:
				return
			case <-ticker.C:
			}
			// t.Errorf is safe from any goroutine; a rotation race is an
			// expected skip, not an error.
			if err := restale(); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("re-staler: %v", err)
			}
		}
	}()

	// --- the storm, then the resume -----------------------------------------
	stormRef := localAppRef(stormID)
	readParams := appwire.ThreadReadParams{Ref: stormRef, IncludeTurns: true, ItemLimit: 40}
	readIDs := make([]int64, 0, queuedReads)
	for i := range queuedReads {
		readIDs = append(readIDs, int64(2+i))
	}

	// drainRead receives id's answer without failing the test on a
	// transport error. The cleanup drain below runs on failure exits where
	// the original failure is already reported; its job is only to let
	// in-flight read handlers finish, not to judge their answers.
	drainRead := func(id int64) error {
		if _, ok := received[id]; ok {
			return nil
		}
		for {
			message, err := transport.Recv(ctx)
			if err != nil {
				return err
			}
			switch {
			case message.Response != nil:
				received[message.Response.ID.Int64()] = message
			case message.Error != nil:
				received[message.Error.ID.Int64()] = message
			}
			if _, ok := received[id]; ok {
				return nil
			}
		}
	}
	// Every exit path must receive every read's answer before the transport
	// closes and the cleanup joins repairs. A read's handler arms its repair
	// before its answer is written (answerRead runs before the typed error is
	// returned), so an answer received is an arm the join's counters will
	// see; an answer still outstanding at cleanup is a handler whose arm has
	// not landed — a repair the join would never count, running past the
	// accounting restore and the fixture removal. The drain rides a defer so
	// no Fatalf exit can skip it — registered before the first read is
	// sent, so even a send failure mid-storm leaves every already-sent read
	// covered. It is idempotent through the received map, so the green
	// path's explicit receives make it a no-op.
	defer func() {
		for _, id := range readIDs {
			if err := drainRead(id); err != nil {
				t.Errorf("storm drain: read %d never answered: %v", id, err)
				return
			}
		}
	}()
	for _, id := range readIDs {
		send(id, appwire.MethodThreadRead, readParams)
	}
	resumeRequest := int64(2 + queuedReads)
	resumeSent := time.Now()
	send(resumeRequest, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: localAppRef(resumeID)})
	resumeMessage := receive(resumeRequest)
	window := time.Since(resumeSent)
	if resumeMessage.Error != nil {
		// A resume failure is a fixture defect, not a timing result; drain
		// the storm's reads first so the failure names what they answered.
		for _, id := range readIDs {
			message := receive(id)
			switch {
			case message.Error != nil:
				t.Logf("read %d error: %+v", id, message.Error.Error)
			case message.Response != nil:
				t.Logf("read %d answered data", id)
			}
		}
		t.Fatalf("resume under the read storm failed: %+v", resumeMessage.Error)
	}
	t.Logf("resume request-to-discovery window: %v", window)
	// The resume is the queue's last request, so a window inside the bound
	// also proves the queued storm drained within it — the green direction
	// of the sustainment pin.
	if window > discoveryBound {
		t.Fatalf("resume discovery window = %v, want <= %v — the rendezvous wait starved behind the read storm (the #4344 incident shape)", window, discoveryBound)
	}

	// --- shape guard: the storm really was a refusal storm ------------------
	// Existential, not universal: a read landing while a repair holds the
	// lock answers the typed locked answer, and a read in a healed instant
	// answers data — both are healthy. What the window must contain is at
	// least one rebuilding-shaped refusal (the storm refused), and what it
	// must never contain is a history-failed answer (the repair budget broke
	// under continuous re-staling) or any unexpected error (flagged per
	// read where it lands).
	rebuilding, locked, answered, historyFailed := 0, 0, 0, 0
	for _, id := range readIDs {
		message := receive(id)
		switch {
		case message.Error != nil:
			raw, err := json.Marshal(message.Error.Error.Data)
			if err != nil {
				t.Fatalf("read %d error data: %v", id, err)
			}
			var data appwire.HistoryReadErrorData
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatalf("read %d error data: %v", id, err)
			}
			switch data.EvenerErrorInfo {
			case appwire.ErrorTranscriptIndexRebuilding:
				rebuilding++
			case appwire.ErrorTranscriptIndexLocked:
				locked++
			case appwire.ErrorTranscriptHistoryFailed:
				historyFailed++
			default:
				t.Errorf("read %d answered unexpected error info %q: %+v", id, data.EvenerErrorInfo, message.Error.Error)
			}
		case message.Response != nil:
			answered++
		default:
			t.Errorf("read %d answered neither a response nor an error", id)
		}
	}
	t.Logf("storm reads: %d rebuilding, %d locked, %d answered data, %d history-failed", rebuilding, locked, answered, historyFailed)
	if rebuilding == 0 {
		t.Fatal("no read in the window answered rebuilding — the re-staled storm never refused, so the window measured an empty storm")
	}
	if historyFailed > 0 {
		t.Fatalf("%d reads answered history-failed — the repair budget exhausted under continuous re-staling", historyFailed)
	}
}

// installRepairAccounting wraps the repair recipe and the post-recording hook
// with path-scoped counters a cleanup can join on: arms counts every repair
// the registry starts for path, and recorded counts every repair whose outcome
// is recorded and whose post-recording hook has fired — the last statement of
// the repair goroutine. Both wrappers only observe: the recipe runs unchanged
// for every path, and other paths' hooks pass through to the hook installed
// before.
func installRepairAccounting(t *testing.T, path string) (arms, recorded *atomic.Uint64) {
	t.Helper()
	arms = &atomic.Uint64{}
	recorded = &atomic.Uint64{}
	savedRecipe := runTranscriptIndexRepair
	savedHook := transcriptIndexRepairRecorded
	runTranscriptIndexRepair = func(p, refused string) error {
		if p == path {
			arms.Add(1)
		}
		return savedRecipe(p, refused)
	}
	transcriptIndexRepairRecorded = func(p string) {
		if p == path {
			recorded.Add(1)
			return
		}
		if savedHook != nil {
			savedHook(p)
		}
	}
	t.Cleanup(func() {
		runTranscriptIndexRepair = savedRecipe
		transcriptIndexRepairRecorded = savedHook
	})
	return arms, recorded
}

// awaitRepairsSettled joins every repair armed for path before the caller's
// cleanup restores test state. An in-flight flag alone is not a join: the
// registry clears it inside run's critical section while the goroutine's
// post-recording hook runs after it, so a poll keyed only on that flag can
// return with the goroutine still live — able to fire its hook into whatever
// later test has installed one. The counters close both windows: a repair is
// armed before its recipe runs, so a goroutine anywhere between its arm and
// its hook keeps arms ahead of recorded or the in-flight flag set, and the
// join returns only once every armed repair's hook has fired. A repair that
// cannot be joined within the budget fails the test instead of escaping the
// cleanup silently.
func awaitRepairsSettled(t *testing.T, path string, arms, recorded *atomic.Uint64) {
	t.Helper()
	const joinBudget = 30 * time.Second
	deadline := time.Now().Add(joinBudget)
	settled := func() bool {
		// The flag first, the counters second — the order is load-bearing.
		// A repair increments arms at its recipe's start and only then lets
		// the recording clear the flag, so observing the flag clear first
		// guarantees the counters already reflect every armed repair.
		// Sampling the counters first lets a whole recipe-and-recording
		// land between the two loads: the comparison holds against the
		// stale count, the flag reads clear, and the join returns with the
		// goroutine still live at its hook — the failure this helper
		// exists to prevent. Producers are quiesced by the time this runs
		// (the hub's server is closed and the re-staler joined, so no new
		// arms can appear behind the observed flag).
		if transcriptIndexRepairs.inFlight(path) {
			return false
		}
		return recorded.Load() >= arms.Load()
	}
	for !settled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !settled() {
		t.Errorf("repair join timed out after %s: %d armed, %d recorded, in flight = %v — the cleanup cannot prove the last repair joined", joinBudget, arms.Load(), recorded.Load(), transcriptIndexRepairs.inFlight(path))
	}
}
