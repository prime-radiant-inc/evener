package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/internal/testfs"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

// This is a finite measurement, not a guarantee about unbounded metadata. The
// original server fixture supplies all 4,000 calls, the maximal escaped excerpt,
// uint64 boot/revision/epoch, active execution and both frontier copies. Its
// three turn-ID populations are fresh, mutation 4000 and mutation MaxUint64.
// Real Session construction supplies Manager metadata and authoritative v1
// provenance; real daemon mutation wiring supplies voice presentation support.
// Real hub producers add project identity, capabilities, artifacts and scratch.
// The CWD, state root and scratch base are each exactly 256 ASCII bytes, with
// fixed leaf names, so TempDir and random identities cannot change their widths.
// Use the widest real UTC RFC3339Nano timestamp and maximal echoed request
// generation. The original artificial TargetSessionToolsSupported=true case
// remains in the server test, while this measurement keeps the actual source's
// field. Ordinary larger metadata can still be refused by the existing cap.
//
// Mutation caught: leaving the normalized maximum at 3160 after adding mandatory
// final hub metadata makes the maximal mutation-ID result exceed 256 KiB.
func TestE2E_PendingAskPageWholeResponseMeasurement(t *testing.T) {
	// The original maximality oracle ran in short mode too. Keep it there:
	// this fixture uses no live provider or separately built Evener executable.
	e2ecap.RequireLoopbackBind(t)
	workingDir := pendingAskMeasurementDir(t, "pending-page-project")
	stateRoot := pendingAskMeasurementDir(t, "state")
	scratchBase := pendingAskMeasurementDir(t, "scratch")
	project := wantProject(t, workingDir)
	stateDir := filepath.Join(stateRoot, "projects", project.ID)
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	endpoints := startPendingAskMeasurementDaemons(t, workingDir, stateDir)
	var entries []appsource.LocalDaemonEntry
	for _, endpoint := range endpoints {
		// These are real allocator-owned directories and persisted metadata,
		// consumed by sessionScratchRoot, not copied response paths.
		scratch, err := agentsandbox.OpenSessionScratch(scratchBase, workingDir, endpoint.SessionID, endpoint.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := scratch.Cleanup(); err != nil {
				t.Error(err)
			}
		})
		meta, err := schema.LoadSessionMeta(stateDir, endpoint.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		meta.ScratchTempDir = scratchBase
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, appsource.LocalDaemonEntry{Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion, Endpoint: endpoint.URL, SourceID: "local",
			SessionID: endpoint.SessionID, ThreadID: endpoint.SessionID,
			WorkspaceRef: "local:" + endpoint.SessionID, InstanceID: endpoint.SessionID,
		}, SessionID: endpoint.SessionID})
	}
	cfg := hubcore.WebConfig{
		Past: newRebuiltPastIndex(t, filepath.Join(stateRoot, "projects", "*")),
		Registry: newProbeRegistry(t, t.TempDir(), nil, nil, map[string]registry.Provider{
			"measurement-voice": {Base: "openai", APIKey: "fixture-not-a-secret"},
		}),
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, nil)
	operations := newProjectReadOperations(t, cfg, source)
	t.Cleanup(func() { operations.server.Shutdown(context.Background()) })
	managerRef := "local:" + identifier.MustNewSessionID()
	caller := appwire.ManagerView{Ref: managerRef, Manager: true}
	largestCommon := 4000
	for _, endpoint := range endpoints {
		turnID := endpoint.TurnID
		t.Run(turnID, func(t *testing.T) {
			revision, one := uint64(math.MaxUint64), 1
			params := appwire.ThreadReadParams{Ref: "local:" + endpoint.SessionID,
				ExpectedAskRevision: &revision, ExpectedAskBootGeneration: appwire.BootGeneration(math.MaxUint64), Limit: &one,
				RequestGeneration: math.MaxUint64,
			}
			final, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Read: &params})
			if err != nil {
				t.Fatal(err)
			}
			if final.Read == nil || final.Read.PendingAskPage == nil || len(final.Read.PendingAskPage.Composites) != 1 || final.Actor.Kind != "manager" || final.Actor.ManagerRef != managerRef || len(final.FollowUps) != 1 || final.FollowUps[0].Tool != "job_status" || final.FollowUps[0].Arguments.Hub.Ref != params.Ref {
				t.Fatalf("incomplete final Invoke response: %+v", final)
			}
			read := final.Read.Internal()
			pending := read.Thread.Evener.PendingAsk
			if read.Epoch != math.MaxUint64 || read.BootGeneration != appwire.BootGeneration(math.MaxUint64) || read.RequestGeneration != math.MaxUint64 || pending == nil || pending.Revision != math.MaxUint64 || pending.BootGeneration != read.BootGeneration || len(pending.Calls) != 10 || !pending.CallsTruncated || pending.CompositeTotal != 4000 {
				t.Fatal("final Invoke lost the maximal epoch, incarnation, revision, generation or clamped row")
			}
			if read.LatestExecutionTurnID != turnID || read.Thread.Evener.LatestExecutionTurnID != turnID || read.Thread.Evener.ActiveTurnID != turnID {
				t.Fatal("final Invoke lost an execution frontier copy or active execution")
			}
			assertThreadProject(t, read.Thread, project)
			caps := read.Thread.Evener.Capabilities
			artifacts, err := schema.SessionArtifactsDir(stateDir, endpoint.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !caps.VoiceArm || !caps.PageBefore || !caps.ForkFromTurn || read.Thread.Evener.ArtifactsDir != artifacts || read.Thread.Evener.ScratchDir != agentsandbox.SessionScratchDirPath(scratchBase, endpoint.SessionID, endpoint.SessionID) {
				t.Fatalf("final hub enrichment missing: caps=%+v artifacts=%q scratch=%q", caps, read.Thread.Evener.ArtifactsDir, read.Thread.Evener.ScratchDir)
			}
			// Fetch the complete backing population through real, pair-fenced
			// source reads. No test-side reconstruction of the 4,000-call list.
			state := &appwire.PendingAskState{}
			for offset := 0; offset < 4000; {
				huge := math.MaxInt
				pageParams := params
				pageParams.Offset, pageParams.Limit = &offset, &huge
				page, err := source.ReadThread(appwire.WithHistoryServingFacts(t.Context()), pageParams)
				if err != nil {
					t.Fatal(err)
				}
				if page.PendingAskPage == nil || len(page.PendingAskPage.Composites) == 0 || page.PendingAskPage.Total != 4000 || page.PendingAskPage.CalllessCount != 2 {
					t.Fatal("finite backing population was not served")
				}
				state.Calls = append(state.Calls, page.PendingAskPage.Composites...)
				offset += len(page.PendingAskPage.Composites)
			}
			// RFC3339Nano omits trailing fractional zeroes. Measure the widest
			// timestamp the real UTC stamping path can emit, not today's clock.
			read.ServingFacts.AsOf = time.Date(2026, time.November, 8, 0, 0, 0, 999999999, time.UTC).Format(time.RFC3339Nano)
			responseAt := func(n int) appwire.SessionOperationResponse {
				pageRead := appwire.HistoryReadResponse(read)
				if pageRead.LatestExecutionTurnID != turnID || !pageRead.Authoritative || pageRead.ServingFactsUnavailableReason != "" || pageRead.FallbackReason != "" {
					t.Fatalf("normalizer dropped or combined live serving facts: %+v", pageRead)
				}
				pageRead.PendingAskPage = &appwire.PendingAskPage{Composites: state.Calls[:n], Total: 4000, CalllessCount: 2}
				if n < 4000 {
					next := n
					pageRead.PendingAskPage.NextOffset = &next
				}
				response := final
				response.Read = &pageRead
				return response
			}
			size := func(n int) int {
				response := responseAt(n)
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return len(encoded)
			}
			low, high := 0, 4000
			for low < high {
				mid := (low + high + 1) / 2
				if size(mid) < 262144 {
					low = mid
				} else {
					high = mid - 1
				}
			}
			largestCommon = min(largestCommon, low)
			t.Logf("largestWholeBytes=%d largestPlusOneBytes=%d turnIDBytes=%d", size(low), size(low+1), len(turnID))
			t.Logf("turnID=%s largest=%d max=%d wholeBytes=%d maxPlusOneBytes=%d", turnID, low, appwire.MaxPendingAskPageLimit, size(appwire.MaxPendingAskPageLimit), size(appwire.MaxPendingAskPageLimit+1))
			if low == 0 {
				t.Fatal("finite fixture cannot fit at a positive page count without contract changes")
			}
			huge := math.MaxInt
			params.Limit = &huge
			normalized, invokeErr := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Read: &params})
			t.Logf("actual final huge-limit Invoke: normalized=%d refused=%t", pendingAskMeasurementCount(normalized), invokeErr != nil)
			if size(appwire.MaxPendingAskPageLimit) >= 262144 {
				t.Fatal("normalized page exceeds whole Manager-result cap")
			}
			if invokeErr != nil || pendingAskMeasurementCount(normalized) != appwire.MaxPendingAskPageLimit {
				t.Fatalf("normalized final Invoke did not fit: count=%d err=%v", pendingAskMeasurementCount(normalized), invokeErr)
			}
			page := normalized.Read.PendingAskPage
			if page.Total != 4000 || page.CalllessCount != 2 || page.NextOffset == nil || *page.NextOffset != appwire.MaxPendingAskPageLimit || !slices.Equal(page.Composites, state.Calls[:appwire.MaxPendingAskPageLimit]) {
				t.Fatal("final huge-limit Invoke changed the pair-fenced pending population")
			}
			t.Logf("normalized page: composites=%d total=%d calllessCount=%d nextOffset=%d", len(page.Composites), page.Total, page.CalllessCount, *page.NextOffset)
			encoded, err := json.Marshal(normalized)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) >= 262144 || managerReadWithinCap(responseAt(low)) != nil || managerReadWithinCap(responseAt(low+1)) == nil {
				t.Fatal("final whole-response acceptance/refusal boundary changed")
			}
			var refusal appwire.WireError
			if !errors.As(managerReadWithinCap(responseAt(low+1)), &refusal) || refusal.Code != appwire.CodeInvalidRequest {
				t.Fatal("oversize response lost its typed whole-read refusal")
			}
			limit := appwire.MaxPendingAskPageLimit
			params.Limit = &limit
			atLimit, err := operations.Invoke(t.Context(), caller, appwire.SessionOperationRequest{Read: &params})
			if err != nil || pendingAskMeasurementCount(atLimit) != limit {
				t.Fatalf("explicit at-limit Invoke: count=%d err=%v", pendingAskMeasurementCount(atLimit), err)
			}
			t.Logf("at-limit actual final bytes=%d conservative bytes=%d next-boundary refusal bytes=%d", len(encoded), size(appwire.MaxPendingAskPageLimit), size(low+1))
		})
	}
	if appwire.MaxPendingAskPageLimit != largestCommon {
		t.Fatalf("maximum=%d, largest measured common page=%d", appwire.MaxPendingAskPageLimit, largestCommon)
	}
}

func pendingAskMeasurementCount(response appwire.SessionOperationResponse) int {
	if response.Read == nil || response.Read.PendingAskPage == nil {
		return 0
	}
	return len(response.Read.PendingAskPage.Composites)
}

func pendingAskMeasurementDir(t *testing.T, leaf string) string {
	t.Helper()
	root := testfs.CanonicalTempDir(t)
	padding := 256 - len(root) - len(leaf) - 2
	if padding < 1 || padding > 255 {
		t.Fatalf("cannot construct finite 256-byte fixture path from %q", root)
	}
	dir := filepath.Join(root, strings.Repeat("p", padding), leaf)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type pendingAskMeasurementEndpoint struct{ SessionID, TurnID, URL string }

func startPendingAskMeasurementDaemons(t *testing.T, workingDir, stateDir string) []pendingAskMeasurementEndpoint {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "pending-ask-server.test")
	args := []string{"test", "-c", "-o", binary}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				args = append(args, "-race")
			}
		}
	}
	args = append(args, "./server")
	t.Logf("finite daemon fixture build arguments: %v", args)
	build := exec.CommandContext(t.Context(), "go", args...)
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOFLAGS=-p=2")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build finite daemon fixture: %v\n%s", err, output)
	}
	config := struct {
		WorkingDir, StateDir string
		Sessions             []pendingAskMeasurementEndpoint
	}{WorkingDir: workingDir, StateDir: stateDir}
	for _, turnID := range []string{identifier.MustNewTurnID(), appwire.ClientMutationTurnID(4000), appwire.ClientMutationTurnID(math.MaxUint64)} {
		config.Sessions = append(config.Sessions, pendingAskMeasurementEndpoint{SessionID: identifier.MustNewSessionID(), TurnID: turnID})
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	logPath := filepath.Join(t.TempDir(), "daemon-fixture.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// T.Context is cancelled before cleanups. Own this process until stdin EOF
	// and Wait instead, so a passing test proves orderly daemon/session teardown.
	command := exec.Command(binary, "-test.run=^TestPendingAskPageMeasurementDaemon$", "-test.v")
	command.Env = append(os.Environ(), "EVENER_PENDING_ASK_MEASUREMENT_CHILD=1")
	command.ExtraFiles = []*os.File{readyWrite}
	command.Stdout, command.Stderr = log, log
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		readyWrite.Close()
		log.Close()
		t.Fatal(err)
	}
	readyWrite.Close()
	t.Cleanup(func() {
		input.Close()
		err := command.Wait()
		log.Close()
		t.Logf("finite daemon fixture joined: err=%v", err)
		if err != nil || t.Failed() {
			body, readErr := os.ReadFile(logPath)
			t.Logf("finite daemon fixture exit=%v logRead=%v\n%s", err, readErr, body)
		}
		if err != nil {
			t.Errorf("finite daemon fixture did not terminate cleanly: %v", err)
		}
	})
	if err := json.NewEncoder(input).Encode(config); err != nil {
		t.Fatal(err)
	}
	var endpoints []pendingAskMeasurementEndpoint
	if err := json.NewDecoder(readyRead).Decode(&endpoints); err != nil {
		if errors.Is(err, io.EOF) {
			t.Fatal("finite daemon fixture exited before publishing ready sockets")
		}
		t.Fatal(err)
	}
	if len(endpoints) != 3 {
		t.Fatalf("ready daemon populations=%d, want 3", len(endpoints))
	}
	t.Logf("finite fixture ready: cwd=%d stateRoot parent=%d populations=%d", len(workingDir), len(filepath.Dir(filepath.Dir(stateDir))), len(endpoints))
	return endpoints
}
