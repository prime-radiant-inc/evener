package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// This is an in-process route test, not a large websocket frame test. Only the
// downstream target writes are recorded; ordinary registration, bound Manager
// admission, shared executors and the production image validator are unchanged.
func TestHubRPCMutationImageAdmissionBounds(t *testing.T) {
	const smallPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aXioAAAAASUVORK5CYII="
	png, err := base64.StdEncoding.DecodeString(smallPNG)
	if err != nil {
		t.Fatal(err)
	}
	// Data is bytes, base64-encoded only by JSON. Exercise the actual per-image
	// field boundary without a websocket's encoded-frame limit masking it.
	boundary := make([]byte, 8*1024*1024)
	copy(boundary, png)
	oversize := append(append([]byte(nil), boundary...), 0)
	root, workingDir := t.TempDir(), t.TempDir()
	sessionID := buildRPCParentSessionWithWorkingDir(t, filepath.Join(root, "projects", "image-target-0000000000"), workingDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	ref := "local:" + sessionID
	for _, surface := range []string{"ordinary", "manager"} {
		for _, method := range []string{appwire.MethodTurnStart, appwire.MethodTurnSteer, appwire.MethodTurnQueue, appwire.MethodTurnDrainAsSteer} {
			for _, imageType := range []string{"image", "input_image"} {
				for _, test := range []struct {
					name  string
					count int
					data  []byte
					valid bool
				}{
					{"nine_images", 9, png, false},
					{"oversize_data", 1, oversize, false},
					{"eight_images", 8, png, true},
					{"exact_data_boundary", 1, boundary, true},
				} {
					t.Run(surface+"/"+method+"/"+imageType+"/"+test.name, func(t *testing.T) {
						ctx := t.Context()
						input := make([]appwire.InputItem, test.count)
						for i := range input {
							input[i] = appwire.InputItem{Type: imageType, MediaType: "image/png", Data: test.data}
						}
						target := &imageAdmissionTarget{scriptedAppSource: &scriptedAppSource{id: "local", thread: appwire.Thread{
							ID: sessionID, SessionID: sessionID, Source: "local",
							Evener: appwire.EvenerThread{Ref: ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Queue: true}},
						}}}
						resumes := 0
						cfg := hubcore.WebConfig{HubID: "image-hub", HubStateRoot: t.TempDir(), Past: past,
							Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
								resumes++
								return rendezvous.Entry{}, appwire.Unavailable("recorded resume attempt")
							}},
						}
						if !hubKnowsRef(cfg, ref) {
							t.Fatal("image target is not eligible for ordinary resume")
						}
						sources := appsource.NewRegistry()
						sources.Add(target)
						server := newHubAppServer(cfg, sources) // All four actual public registrations.
						t.Cleanup(func() { server.Shutdown(context.Background()) })
						const mutationID = "image-admission"
						start := appwire.TurnStartParams{Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, Input: input}
						steer := appwire.TurnSteerParams{Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, Input: input}
						queue := appwire.TurnQueueParams{Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, Input: input}
						drain := appwire.TurnDrainAsSteerParams{Ref: ref, ClientMutationID: mutationID, ExpectedInstanceID: sessionID, ExpectedQueueRevision: 1, Input: input}
						var operation appwire.SessionOperationRequest
						var params any
						switch method {
						case appwire.MethodTurnStart:
							params, operation.Start = start, &start
						case appwire.MethodTurnSteer:
							params, operation.Steer = steer, &steer
						case appwire.MethodTurnQueue:
							params, operation.Queue = queue, &queue
						case appwire.MethodTurnDrainAsSteer:
							params = drain
							operation.Control = &appwire.SessionControlRequest{Action: "drain_queue", DrainQueue: &drain}
						}
						var callErr error
						if surface == "ordinary" {
							raw, err := json.Marshal(params)
							if err != nil {
								t.Fatal(err)
							}
							_, callErr = server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
						} else {
							// Use the actual bound dispatcher and its live-source caller
							// check, not owner.Invoke with an asserted ManagerView.
							entry := rendezvous.Entry{SourceID: "local", SessionID: "image-manager", ThreadID: "image-manager", InstanceID: "image-manager-instance", WorkspaceRef: "local:image-manager"}
							local := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
								return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: "image-manager", Manager: true}}
							}, nil)
							bound := &boundSessionOperations{
								owner: &sessionOperations{cfg: cfg, sources: sources, server: server, relays: newHubRelay(server, cfg, sources)},
								local: local, binding: appwire.SessionBinding{Ref: "local:image-manager", InstanceID: entry.InstanceID, HubID: cfg.HubID, BindingID: "image-manager-binding"},
							}
							_, callErr = bound.Invoke(ctx, operation)
						}
						if !test.valid {
							wire, ok := errors.AsType[appwire.WireError](callErr)
							if !ok || wire.Code != appwire.CodeInvalidParams || target.writes != 0 || target.resumes != 0 || resumes != 0 {
								t.Fatalf("image admission: code=%d wire=%v writes=%d sourceResumes=%d spawnerResumes=%d err=%v", wire.Code, ok, target.writes, target.resumes, resumes, callErr)
							}
							return
						}
						if callErr != nil || target.writes != 1 || target.method != method || target.mutationID != mutationID || !reflect.DeepEqual(target.input, input) || target.resumes != 0 || resumes != 0 {
							t.Fatalf("boundary was not forwarded intact exactly once: method=%q writes=%d mutation=%q sourceResumes=%d spawnerResumes=%d err=%v", target.method, target.writes, target.mutationID, target.resumes, resumes, callErr)
						}
						if surface == "manager" {
							if target.proof == nil || target.proof.CallerRef != "local:image-manager" || target.proof.Actor.Kind != "manager" {
								t.Fatalf("bound Manager provenance missing: %+v", target.proof)
							}
						} else if target.proof != nil {
							t.Fatalf("ordinary input acquired Manager proof: %+v", target.proof)
						}
					})
				}
			}
		}
	}
}

// Reuse the existing target Source fixture. These methods only record writes
// after the real hub has admitted them; they do not implement validation.
type imageAdmissionTarget struct {
	*scriptedAppSource
	writes, resumes    int
	method, mutationID string
	input              []appwire.InputItem
	proof              *appwire.SessionActorProof
}

func (s *imageAdmissionTarget) record(method, id string, input []appwire.InputItem, proof *appwire.SessionActorProof) {
	s.writes++
	s.method, s.mutationID, s.input, s.proof = method, id, input, proof
}

func (s *imageAdmissionTarget) StartTurn(_ context.Context, p appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.record(appwire.MethodTurnStart, p.ClientMutationID, p.Input, p.ManagerProof)
	return appwire.TurnStartResponse{}, nil
}

func (s *imageAdmissionTarget) SteerTurn(_ context.Context, p appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	s.record(appwire.MethodTurnSteer, p.ClientMutationID, p.Input, p.ManagerProof)
	return appwire.TurnSteerResponse{}, nil
}

func (s *imageAdmissionTarget) QueueTurn(_ context.Context, p appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	s.record(appwire.MethodTurnQueue, p.ClientMutationID, p.Input, p.ManagerProof)
	return appwire.TurnQueueResponse{}, nil
}

func (s *imageAdmissionTarget) DrainAsSteer(_ context.Context, p appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	s.record(appwire.MethodTurnDrainAsSteer, p.ClientMutationID, p.Input, p.ManagerProof)
	return appwire.TurnDrainAsSteerResponse{}, nil
}

func (s *imageAdmissionTarget) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	s.resumes++
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("recorded source resume attempt")
}
