package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// The process-launch boundary fails before any child can run. The server log
// must retain correlated, classified failure metadata even if no RPC caller
// remains to receive the returned error. The path deliberately carries opaque
// sensitive data: logging the raw exec error would disclose it.
func TestThreadLifecycleLoggingLaunchFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	const secret = "PRIVATE_LAUNCH_PATH_opaque_sentinel"
	var hubLog bytes.Buffer
	_, err := resumeDaemon(context.Background(), filepath.Join(dir, secret), filepath.Join(dir, "run"), hubcore.ResumeRequest{SessionID: sessionID}, 0, &hubLog)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("launch fixture: got %v, want missing executable", err)
	}
	if strings.Contains(hubLog.String(), secret) || strings.Contains(hubLog.String(), dir) {
		t.Fatalf("lifecycle log leaked executable path: %s", &hubLog)
	}
	records := threadLifecycleLogRecords(t, hubLog.String())
	if len(records) == 0 {
		t.Fatal("missing structured lifecycle records for failed resume launch")
	}
	requestID := records[0]["request_id"]
	if requestID == "" || requestID == "-" {
		t.Fatal("missing request correlation")
	}
	var launchBegin, launchComplete, daemonComplete bool
	for _, record := range records {
		if record["request_id"] != requestID || record["session_id"] != sessionID || record["operation"] != "resume" {
			t.Fatalf("lost resume correlation: %#v", record)
		}
		for _, field := range []string{"elapsed_ms", "stage_elapsed_ms"} {
			if _, err := strconv.ParseUint(record[field], 10, 64); err != nil {
				t.Fatalf("invalid %s: %#v", field, record)
			}
		}
		switch record["stage"] + "/" + record["state"] {
		case "launch/begin":
			launchBegin = true
		case "launch/complete":
			if !launchBegin || record["result"] != "error" || record["error_class"] != "not_found" {
				t.Fatalf("unclassified or unordered launch failure: %#v", record)
			}
			launchComplete = true
		case "daemon/complete":
			if !launchComplete || record["result"] != "error" || record["error_class"] != "not_found" {
				t.Fatalf("unclassified or premature final outcome: %#v", record)
			}
			daemonComplete = true
		}
	}
	if !launchBegin || !launchComplete || !daemonComplete {
		t.Fatalf("incomplete failed-launch lifecycle: %#v", records)
	}
}

// The production fallback launches the bare name "evener" and lets exec.Command
// search $PATH: HubSpawner.EvenerBinary defaults to "" and spawnDaemon and
// resumeDaemon then run exec.Command("evener", ...). A bare name missing from
// $PATH fails as *exec.Error wrapping exec.ErrNotFound, which is not
// fs.ErrNotExist — so the test above does not cover it: its path carries a
// separator and fails as *fs.PathError. Both must record not_found.
func TestThreadLifecycleLoggingLaunchFailureBareName(t *testing.T) {
	t.Parallel()
	// A bare name with no separator that no $PATH entry can resolve, which is
	// the shape os/exec's own lookup failure takes.
	const absentBinary = "evener-bare-name-absent-from-path"
	dir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	var hubLog bytes.Buffer
	_, err := resumeDaemon(context.Background(), absentBinary, dir, hubcore.ResumeRequest{SessionID: sessionID}, 0, &hubLog)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("launch fixture: got %v, want bare-name lookup failure", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("launch fixture: %v wraps fs.ErrNotExist, so it is not the uncovered case", err)
	}
	records := assertThreadLifecycleRecords(t, hubLog.String())
	assertThreadLifecycleOutcome(t, records, "launch", "error", "not_found")
	assertThreadLifecycleOutcome(t, records, "daemon", "error", "not_found")
}

// Lifecycle fields are bounded metadata tokens, never free-form prose. Parse
// the emitted records rather than pinning timestamps or an entire log snapshot.
func threadLifecycleLogRecords(t *testing.T, output string) []map[string]string {
	t.Helper()
	var records []map[string]string
	for line := range strings.SplitSeq(output, "\n") {
		body, ok := strings.CutPrefix(line, "[hub] lifecycle ")
		if !ok {
			continue
		}
		record := make(map[string]string)
		for field := range strings.FieldsSeq(body) {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key == "" || value == "" {
				t.Fatalf("invalid structured lifecycle field %q", field)
			}
			if _, exists := record[key]; exists {
				t.Fatalf("duplicate structured lifecycle field %q", key)
			}
			record[key] = value
		}
		records = append(records, record)
	}
	return records
}

func TestThreadLifecycleLoggingResumeSuccess(t *testing.T) {
	var sessionID string
	cfg, id, calls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: sessionID}}}, nil
		})
	})
	sessionID = id
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	var output bytes.Buffer
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", id, &output)
	response, err := hubThreadResume(ctx, cfg, newHubSourceRegistry(cfg), appwire.ThreadResumeParams{Session: id})
	if err != nil || response.Thread.SessionID != id || *calls != 1 {
		t.Fatalf("resume fixture: session=%s calls=%d err=%v", response.Thread.SessionID, *calls, err)
	}
	records := assertThreadLifecycleRecords(t, output.String())
	for _, stage := range []string{"request", "ownership", "lock_wait", "lock_held", "ownership_recheck", "discovery", "request_preparation", "spawner_resume", "post_launch_discovery", "daemon_read"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
	}
}

func TestThreadLifecycleLoggingResumeAliasCorrelation(t *testing.T) {
	var currentID string
	cfg, id, calls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: currentID, SessionID: currentID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: currentID}}}, nil
		})
	})
	currentID = id
	requestedID := hubtest.SessionID(t)
	if requestedID == currentID {
		t.Fatal("alias fixture requires distinct requested and current session IDs")
	}
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{requestedID, currentID}
	finish := locks.BeginForceStop(aliases)
	if err := locks.PersistForceStop(aliases, currentID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(currentID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	cfg.ResumeLocks = locks
	var output bytes.Buffer
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", requestedID, &output)
	response, err := hubThreadResume(ctx, cfg, newHubSourceRegistry(cfg), appwire.ThreadResumeParams{Ref: "local:" + requestedID})
	if err != nil || response.Thread.SessionID != currentID || *calls != 1 {
		t.Fatalf("alias resume fixture: session=%s calls=%d err=%v", response.Thread.SessionID, *calls, err)
	}
	records := assertThreadLifecycleRecords(t, output.String())
	requestID := records[0]["request_id"]
	for _, stage := range []string{"request_preparation", "spawner_resume", "daemon_read"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
		for _, record := range records {
			if record["stage"] == stage && (record["session_id"] != requestedID || record["resolved_session_id"] != currentID || record["request_id"] != requestID) {
				t.Fatalf("lost requested/resolved correlation: %#v, want session=%s resolved=%s request=%s", record, requestedID, currentID, requestID)
			}
		}
	}
}

func TestThreadLifecycleLoggingResumeErrorAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			id := hubtest.SessionID(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const secret = "PRIVATE_LAUNCH_ERROR_opaque_sentinel"
			cfg := hubcore.WebConfig{RunDir: t.TempDir(), Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				if canceled {
					cancel()
					return rendezvous.Entry{}, context.Canceled
				}
				return rendezvous.Entry{}, errors.New(secret)
			}}}
			var output bytes.Buffer
			ctx, _ = withThreadLifecycleLog(ctx, "resume", id, &output)
			_, err := hubThreadResume(ctx, cfg, nil, appwire.ThreadResumeParams{Session: id})
			if err == nil {
				t.Fatal("resume fixture unexpectedly succeeded")
			}
			if strings.Contains(output.String(), secret) {
				t.Fatalf("raw error leaked: %s", &output)
			}
			records := assertThreadLifecycleRecords(t, output.String())
			result, class := "error", "failed"
			if canceled {
				result, class = "canceled", "canceled"
			}
			assertThreadLifecycleOutcome(t, records, "spawner_resume", result, class)
			assertThreadLifecycleOutcome(t, records, "request", result, class)
		})
	}
}

type threadLifecycleWaitWriter struct {
	mu      sync.Mutex
	output  bytes.Buffer
	waiting chan struct{}
	once    sync.Once
}

func (w *threadLifecycleWaitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(p)
	if bytes.Contains(p, []byte("stage=lock_wait state=begin")) {
		w.once.Do(func() { close(w.waiting) })
	}
	return n, err
}

func (w *threadLifecycleWaitWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestThreadLifecycleLoggingLockWaiting(t *testing.T) {
	id := hubtest.SessionID(t)
	locks := hubcore.NewResumeLocks()
	lock := locks.For(id)
	lock.Lock()
	release := sync.OnceFunc(lock.Unlock)
	defer release()
	w := &threadLifecycleWaitWriter{waiting: make(chan struct{})}
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", id, w)
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
	done := make(chan error, 1)
	finished := false
	defer func() {
		release()
		if !finished {
			<-done
		}
	}()
	go func() {
		_, err := hubThreadResume(ctx, cfg, nil, appwire.ThreadResumeParams{Session: id})
		done <- err
	}()
	// A missing record is a regression, not a reason to leave the fixture's
	// mutex held until the package timeout. This timer is only a tripwire;
	// the log event, not elapsed time, advances the successful test.
	tripwire := time.NewTimer(5 * time.Second)
	defer tripwire.Stop()
	select {
	case <-w.waiting:
	case err := <-done:
		finished = true
		t.Fatalf("resume returned before waiting for lock: %v", err)
	case <-tripwire.C:
		t.Fatal("no lock-wait entry record")
	}
	// Ownership of the actual mutex proves acquisition cannot yet have occurred;
	// no sleeps or elapsed-time threshold stand in for that causal boundary.
	for _, record := range threadLifecycleLogRecords(t, w.String()) {
		if record["stage"] == "lock_wait" && record["state"] == "complete" {
			t.Fatalf("lock acquisition reported while fixture still owns mutex: %#v", record)
		}
	}
	release()
	err := <-done
	finished = true
	if err == nil {
		t.Fatal("missing-spawner fixture unexpectedly succeeded")
	}
	records := assertThreadLifecycleRecords(t, w.String())
	assertThreadLifecycleOutcome(t, records, "lock_wait", "success", "none")
	assertThreadLifecycleOutcome(t, records, "lock_held", "success", "none")
	assertThreadLifecycleOutcome(t, records, "request", "error", "failed")
}

func assertThreadLifecycleOutcome(t *testing.T, records []map[string]string, stage, result, class string) {
	t.Helper()
	for _, record := range records {
		if record["stage"] == stage && record["state"] == "complete" {
			if record["result"] != result || record["error_class"] != class {
				t.Fatalf("%s outcome: %#v, want %s/%s", stage, record, result, class)
			}
			return
		}
	}
	t.Fatalf("missing %s completion: %#v", stage, records)
}

func assertThreadLifecycleRecords(t *testing.T, output string) []map[string]string {
	t.Helper()
	records := threadLifecycleLogRecords(t, output)
	if len(records) == 0 {
		t.Fatal("missing structured lifecycle records")
	}
	requestID := records[0]["request_id"]
	if requestID == "" || requestID == "-" {
		t.Fatal("missing request correlation")
	}
	open := make(map[string]bool)
	for _, record := range records {
		if record["request_id"] != requestID {
			t.Fatalf("lost request correlation: %#v", record)
		}
		for _, key := range []string{"elapsed_ms", "stage_elapsed_ms", "tail_bytes", "pid"} {
			if _, err := strconv.ParseUint(record[key], 10, 64); err != nil {
				t.Fatalf("invalid %s: %#v", key, record)
			}
		}
		stage := record["stage"]
		switch record["state"] {
		case "begin":
			if open[stage] {
				t.Fatalf("duplicate stage entry: %#v", record)
			}
			open[stage] = true
		case "complete":
			if !open[stage] && stage != "failed_start" {
				t.Fatalf("stage completed without entry: %#v", record)
			}
			delete(open, stage)
		default:
			t.Fatalf("invalid lifecycle state: %#v", record)
		}
	}
	if len(open) != 0 {
		t.Fatalf("stages left incomplete: %#v", open)
	}
	return records
}

// The original exact banner assertion still checks every non-lifecycle byte.
// New lifecycle records are independently parsed and checked, not ignored.
func threadLifecycleBannerLog(t *testing.T, output string) string {
	t.Helper()
	records := assertThreadLifecycleRecords(t, output)
	for _, stage := range []string{"launch", "rendezvous", "daemon"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
	}
	var banner strings.Builder
	for _, line := range strings.SplitAfter(output, "\n") {
		if !strings.HasPrefix(line, "[hub] lifecycle ") {
			banner.WriteString(line)
		}
	}
	return banner.String()
}

func TestThreadLifecycleLoggingFailedCandidateMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	id := hubtest.SessionID(t)
	canonical := filepath.Join(runDir, daemonLogDirName, daemonLogName(id))
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	const earlier = "earlier log remains byte-identical\n"
	if err := os.WriteFile(canonical, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	const diagnostic = "PRIVATE_DAEMON_OUTPUT_opaque_sentinel"
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, "#!/bin/sh\nprintf '%s' '"+diagnostic+"' >&2\nexit 23\n")
	var output bytes.Buffer
	_, err := resumeDaemon(t.Context(), bin, runDir, hubcore.ResumeRequest{SessionID: id}, 0, &output)
	if err == nil || !strings.Contains(err.Error(), diagnostic) {
		t.Fatalf("existing RPC diagnostic lost: %v", err)
	}
	if strings.Contains(output.String(), diagnostic) {
		t.Fatalf("daemon output leaked into hub log: %s", &output)
	}
	records := assertThreadLifecycleRecords(t, output.String())
	assertThreadLifecycleOutcome(t, records, "rendezvous", "error", "process_exit")
	assertThreadLifecycleOutcome(t, records, "daemon", "error", "process_exit")
	var preserved bool
	for _, record := range records {
		if record["stage"] != "failed_start" {
			continue
		}
		preserved = true
		if record["tail_bytes"] != strconv.Itoa(len(diagnostic)) || record["tail_at_limit"] != "false" || record["exit_code"] != "23" || record["pid"] == "0" {
			t.Fatalf("failed candidate metadata: %#v", record)
		}
	}
	if !preserved {
		t.Fatal("missing persistent failed-start metadata")
	}
	got, err := os.ReadFile(canonical)
	if err != nil || string(got) != earlier {
		t.Fatalf("canonical log changed: %q, %v", got, err)
	}
	files, err := os.ReadDir(filepath.Dir(canonical))
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(canonical) {
		t.Fatalf("candidate cleanup changed: %v, %v", files, err)
	}
}

func TestThreadLifecycleLoggingPreparationCorrelation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := hubtest.SessionID(t)
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = launch-check ]; then\n  printf '%%s' '{\"protocol\":%q,\"launch_flags\":[\"api-log\"]}'\n  exit 0\nfi\nexit 17\n", appwire.ProtocolVersion))
	var output bytes.Buffer
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", id, &output)
	spawner := &HubSpawner{EvenerBinary: bin, RunDir: filepath.Join(dir, "run")}
	_, err := spawner.Resume(ctx, hubcore.ResumeRequest{SessionID: id, StateDir: filepath.Join(dir, "state")})
	if err == nil {
		t.Fatal("scripted daemon should exit before rendezvous")
	}
	records := assertThreadLifecycleRecords(t, output.String())
	for _, stage := range []string{"launch_preparation", "launch_contract", "launch"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
	}
	assertThreadLifecycleOutcome(t, records, "rendezvous", "error", "process_exit")
	assertThreadLifecycleOutcome(t, records, "daemon", "error", "process_exit")
	for _, record := range records {
		if record["session_id"] != id {
			t.Fatalf("lost session correlation: %#v", record)
		}
	}
}

func TestThreadLifecycleLoggingHandlerSpawnerCorrelation(t *testing.T) {
	dir := t.TempDir()
	workingDir := t.TempDir()
	stateDir := hubtest.ProjectDir(t, filepath.Join(dir, "projects"), "logging")
	id := buildRPCParentSessionWithWorkingDir(t, stateDir, workingDir)
	past := hubcore.NewPastIndex(filepath.Join(dir, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = launch-check ]; then\n  printf '%%s' '{\"protocol\":%q,\"launch_flags\":[\"api-log\"]}'\n  exit 0\nfi\nexit 19\n", appwire.ProtocolVersion))
	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past, RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Spawner: &HubSpawner{EvenerBinary: bin, RunDir: runDir}}
	var output bytes.Buffer
	ctx, _ := withThreadLifecycleLog(t.Context(), "resume", id, &output)
	// Both Evener layers are real. Only the external executable is scripted;
	// replacing the handler's spawner context loses its logger and correlation.
	_, err := hubThreadResume(ctx, cfg, nil, appwire.ThreadResumeParams{Session: id})
	if err == nil {
		t.Fatal("scripted daemon should exit before rendezvous")
	}
	records := assertThreadLifecycleRecords(t, output.String())
	for _, stage := range []string{"request_preparation", "launch_preparation", "launch_contract", "launch"} {
		assertThreadLifecycleOutcome(t, records, stage, "success", "none")
	}
	for _, stage := range []string{"rendezvous", "daemon", "spawner_resume"} {
		assertThreadLifecycleOutcome(t, records, stage, "error", "process_exit")
	}
	// The RPC error deliberately wraps launch details as a protocol error; its
	// final classification stays bounded rather than parsing that message.
	assertThreadLifecycleOutcome(t, records, "request", "error", "failed")
	for _, record := range records {
		if record["session_id"] != id {
			t.Fatalf("lost cross-layer session correlation: %#v", record)
		}
	}
}

func TestThreadLifecycleLoggingBoundedMetadata(t *testing.T) {
	t.Parallel()
	const secret = "PRIVATE_SESSION_AND_ERROR_opaque_sentinel\nforged=value"
	for _, tc := range []struct {
		err   error
		class string
	}{
		{nil, "none"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{errRendezvousTimeout, "timeout"},
		{errRendezvousCanceled, "canceled"},
		{fs.ErrPermission, "permission"},
		{errors.New(secret), "failed"},
	} {
		var output bytes.Buffer
		ctx, trace := withThreadLifecycleLog(t.Context(), "resume", secret, &output)
		done := trace.stage(ctx, "request")
		done(tc.err)
		if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "forged=") {
			t.Fatalf("unsafe metadata: %s", &output)
		}
		records := assertThreadLifecycleRecords(t, output.String())
		if records[1]["session_id"] != "-" || records[1]["error_class"] != tc.class {
			t.Fatalf("classification/identity: %#v, want %s", records[1], tc.class)
		}
		if len(output.String()) > 2048 {
			t.Fatal("unbounded lifecycle record")
		}
	}
}

func TestThreadLifecycleLoggingConcreteErrorPrecedesContext(t *testing.T) {
	t.Parallel()
	const secret = "PRIVATE_CLASSIFICATION_ERROR_opaque_sentinel\nforged=value"
	exitErr := exec.Command("/bin/sh", "-c", "exit 23").Run()
	if exit, ok := errors.AsType[*exec.ExitError](exitErr); !ok || exit.ExitCode() != 23 {
		t.Fatalf("process fixture: got %v, want exit 23", exitErr)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelDeadline := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancelDeadline()
	for _, contextCase := range []struct {
		name           string
		ctx            context.Context
		err            error
		fallbackClass  string
		fallbackResult string
	}{
		{"active", t.Context(), nil, "failed", "error"},
		{"canceled", canceled, context.Canceled, "canceled", "canceled"},
		{"deadline", expired, context.DeadlineExceeded, "timeout", "error"},
	} {
		t.Run(contextCase.name, func(t *testing.T) {
			if !errors.Is(contextCase.ctx.Err(), contextCase.err) {
				t.Fatalf("context fixture: got %v, want %v", contextCase.ctx.Err(), contextCase.err)
			}
			for _, tc := range []struct {
				name     string
				err      error
				class    string
				result   string
				exitCode string
			}{
				{"nil", nil, "none", "success", "-1"},
				{"permission", fmt.Errorf("%s: %w", secret, fs.ErrPermission), "permission", "error", "-1"},
				{"not_found", fmt.Errorf("%s: %w", secret, fs.ErrNotExist), "not_found", "error", "-1"},
				{"exec_not_found", fmt.Errorf("%s: %w", secret, &exec.Error{Name: "evener", Err: exec.ErrNotFound}), "not_found", "error", "-1"},
				{"process_exit", fmt.Errorf("%s: %w", secret, exitErr), "process_exit", "error", "23"},
				{"explicit_cancel", fmt.Errorf("%s: %w", secret, context.Canceled), "canceled", "canceled", "-1"},
				{"explicit_deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "timeout", "error", "-1"},
				{"rendezvous_cancel", fmt.Errorf("%s: %w", secret, errRendezvousCanceled), "canceled", "canceled", "-1"},
				{"rendezvous_timeout", fmt.Errorf("%s: %w", secret, errRendezvousTimeout), "timeout", "error", "-1"},
				{"permission_and_cancel", errors.Join(fs.ErrPermission, context.Canceled), "canceled", "canceled", "-1"},
				{"permission_and_deadline", errors.Join(fs.ErrPermission, context.DeadlineExceeded), "timeout", "error", "-1"},
				{"unclassified", errors.New(secret), contextCase.fallbackClass, contextCase.fallbackResult, "-1"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if got := lifecycleErrorClass(contextCase.ctx, tc.err); got != tc.class {
						t.Errorf("error class = %s, want %s", got, tc.class)
					}
					id := hubtest.SessionID(t)
					var output bytes.Buffer
					ctx, trace := withThreadLifecycleLog(contextCase.ctx, "resume", id, &output)
					done := trace.stage(ctx, "request")
					done(tc.err)
					if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "forged=") {
						t.Fatalf("raw error leaked into lifecycle log: %s", &output)
					}
					records := assertThreadLifecycleRecords(t, output.String())
					if len(records) != 2 {
						t.Fatalf("got %d records, want begin and complete", len(records))
					}
					if records[0]["error_class"] != "none" || records[0]["result"] != "pending" {
						t.Fatalf("nil-error begin affected by context: %#v", records[0])
					}
					for _, record := range records {
						if record["session_id"] != id || record["resolved_session_id"] != "-" || record["operation"] != "resume" {
							t.Fatalf("lost lifecycle identity: %#v", record)
						}
					}
					if records[1]["exit_code"] != tc.exitCode {
						t.Fatalf("exit code = %s, want %s", records[1]["exit_code"], tc.exitCode)
					}
					assertThreadLifecycleOutcome(t, records, "request", tc.result, tc.class)
				})
			}
		})
	}
}
