package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func installModelControlIdentity(t *testing.T, srv *Server, instanceID, generation, ref string) {
	t.Helper()
	prepared, err := PrepareAppIdentityForRef("local", instanceID, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration(generation), nil)
}

func modelControlSnapshot(instanceID, generation string, revision uint64, model string) appwire.ModelControlSnapshot {
	return appwire.ModelControlSnapshot{
		Authority: appwire.ModelControlAuthority{InstanceID: instanceID, BootGeneration: generation},
		Revision:  revision,
		Active: appwire.ModelControlActive{
			ModelProvider:         "openai",
			Model:                 model,
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
		},
	}
}

func modelControlSetParams(ref, instanceID, generation, mutationID, providerID, model string) appwire.ThreadModelSetParams {
	return appwire.ThreadModelSetParams{
		Ref:                    ref,
		ModelProvider:          providerID,
		Model:                  model,
		ClientMutationID:       mutationID,
		ExpectedInstanceID:     instanceID,
		ExpectedBootGeneration: generation,
	}
}

func requireModelControlWireError(t *testing.T, err error, code int, info appwire.ErrorInfo) {
	t.Helper()
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("error type = %T, want appwire.WireError", err)
	}
	data, ok := appwire.ErrorDataOf(wire.Data)
	if !ok || wire.Code != code || data.EvenerErrorInfo != info {
		t.Fatalf("wire error = %+v data=%+v, want code %d and info %q", wire, data, code, info)
	}
}

func TestHandleAppThreadModelSet_CapableResponseAndReadUseOneSnapshot(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
	snapshot := modelControlSnapshot("instance-1", "7", 2, "gpt-5.5")
	source := &stubThreadEnvelopeSource{modelControl: snapshot}
	srv.SetThreadEnvelopeSource(source)
	var got appwire.ThreadModelSetParams
	srv.SetModelControlBindFunc(func(authority appwire.ModelControlAuthority) (ModelControlBinding, error) {
		if authority != snapshot.Authority {
			t.Fatalf("bound authority = %+v, want %+v", authority, snapshot.Authority)
		}
		return ModelControlBinding{
			Envelope: source,
			Set: func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
				got = params
				return appwire.ThreadModelSetResponse{
					Disposition:  appwire.ModelControlDispositionAccepted,
					ModelControl: &snapshot,
				}, nil
			},
		}, nil
	})
	params := modelControlSetParams("local:workspace", "instance-1", "7", "pick-1", "openrouter", "anthropic/claude-opus")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err := srv.handleAppThreadModelSet(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelProvider != params.ModelProvider || got.Model != params.Model {
		t.Fatalf("bound target = %q/%q, want unsplit %q/%q", got.ModelProvider, got.Model, params.ModelProvider, params.Model)
	}
	if response.ModelControl == nil || !reflect.DeepEqual(*response.ModelControl, snapshot) {
		t.Fatalf("response snapshot = %+v, want %+v", response.ModelControl, snapshot)
	}
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Capabilities.ModelControlVersion != appwire.ModelControlVersion1 {
		t.Fatalf("capability = %d, want 1", read.Thread.Evener.Capabilities.ModelControlVersion)
	}
	if read.Thread.Evener.ModelControl == nil || !reflect.DeepEqual(*read.Thread.Evener.ModelControl, snapshot) {
		t.Fatalf("read snapshot = %+v, want %+v", read.Thread.Evener.ModelControl, snapshot)
	}
}

func TestHandleAppThreadModelSet_RejectsPartialOrStaleAuthorityBeforeBinding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*appwire.ThreadModelSetParams)
		wantCode   int
		wantReason appwire.ErrorInfo
	}{
		{name: "empty identity", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ClientMutationID, p.ExpectedInstanceID, p.ExpectedBootGeneration = "", "", ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "mutation only", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ExpectedInstanceID, p.ExpectedBootGeneration = "", ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "instance only", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ClientMutationID, p.ExpectedBootGeneration = "", ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "generation only", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ClientMutationID, p.ExpectedInstanceID = "", ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "mutation and instance", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ExpectedBootGeneration = ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "mutation and generation", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ExpectedInstanceID = ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "instance and generation", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ClientMutationID = ""
		}, wantCode: appwire.CodeInvalidParams, wantReason: appwire.ErrorInvalidParams},
		{name: "stale instance", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ExpectedInstanceID = "old"
		}, wantCode: appwire.CodeConflict, wantReason: appwire.ErrorModelControlAuthorityStale},
		{name: "stale generation", mutate: func(p *appwire.ThreadModelSetParams) {
			p.ExpectedBootGeneration = "6"
		}, wantCode: appwire.CodeConflict, wantReason: appwire.ErrorModelControlAuthorityStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
			snapshot := modelControlSnapshot("instance-1", "7", 1, "gpt-5.4")
			source := &stubThreadEnvelopeSource{modelControl: snapshot}
			srv.SetThreadEnvelopeSource(source)
			called := false
			srv.SetModelControlBindFunc(func(appwire.ModelControlAuthority) (ModelControlBinding, error) {
				called = true
				return ModelControlBinding{}, nil
			})
			params := modelControlSetParams("not-a-stable-ref", "instance-1", "7", "pick-1", "openai", "gpt-5.5")
			if tc.wantReason == appwire.ErrorModelControlAuthorityStale {
				params.Ref = "local:workspace"
			}
			tc.mutate(&params)
			_, err := srv.handleAppThreadModelSet(context.Background(), params)
			if err == nil {
				t.Fatal("expected authority error")
			}
			requireModelControlWireError(t, err, tc.wantCode, tc.wantReason)
			if called {
				t.Fatal("invalid authority reached the binder")
			}
			if !reflect.DeepEqual(source.modelControl, snapshot) {
				t.Fatalf("authority refusal changed source snapshot: %+v", source.modelControl)
			}
		})
	}
}

func TestHandleAppThreadModelSet_IdentityFreeRequestIsRefused(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
	snapshot := modelControlSnapshot("instance-1", "7", 2, "gpt-5.5")
	source := &stubThreadEnvelopeSource{modelControl: snapshot}
	srv.SetThreadEnvelopeSource(source)
	called := false
	srv.SetModelControlBindFunc(func(appwire.ModelControlAuthority) (ModelControlBinding, error) {
		called = true
		return ModelControlBinding{}, nil
	})
	_, err := srv.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Ref: "local:workspace", ModelProvider: "openai", Model: "gpt-5.5"})
	if err == nil {
		t.Fatal("identity-free request = nil error, want rejection")
	}
	requireModelControlWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	if called {
		t.Fatal("identity-free request reached the binder")
	}
}

func TestModelControlEnvelopeRejectsOlderSameAuthorityRevision(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
	oldSampled := make(chan struct{})
	releaseOld := make(chan struct{})
	old := &stubThreadEnvelopeSource{modelControl: modelControlSnapshot("instance-1", "7", 2, "gpt-old")}
	old.parkModelControl = func() { close(oldSampled); <-releaseOld }
	srv.SetThreadEnvelopeSource(old)
	done := make(chan struct{})
	go func() {
		srv.refreshFacets(facetModelControl)
		close(done)
	}()
	<-oldSampled
	newer := modelControlSnapshot("instance-1", "7", 3, "gpt-new")
	old.modelControl = newer
	old.parkModelControl = nil
	srv.refreshFacets(facetModelControl)
	close(releaseOld)
	<-done
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.ModelControl == nil || read.Thread.Evener.ModelControl.Revision != 3 {
		t.Fatalf("read snapshot = %+v, want revision 3", read.Thread.Evener.ModelControl)
	}
}

func TestModelControlCapabilityOmittedWithoutCompleteFacet(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "instance-1")
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Capabilities.ModelControlVersion != 0 || read.Thread.Evener.ModelControl != nil {
		t.Fatalf("daemonless model control advertised: %+v", read.Thread.Evener)
	}
}

func TestHandleAppThreadModelSet_RemainsBoundAcrossConcurrentClear(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "old", "7", "local:workspace")
	oldSnapshot := modelControlSnapshot("old", "7", 2, "gpt-old")
	oldSource := &stubThreadEnvelopeSource{modelControl: oldSnapshot}
	srv.SetThreadEnvelopeSource(oldSource)
	started := make(chan struct{})
	release := make(chan struct{})
	srv.SetModelControlBindFunc(func(appwire.ModelControlAuthority) (ModelControlBinding, error) {
		return ModelControlBinding{Envelope: oldSource, Set: func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
			close(started)
			<-release
			return appwire.ThreadModelSetResponse{Disposition: appwire.ModelControlDispositionAccepted, ModelControl: &oldSnapshot}, nil
		}}, nil
	})
	done := make(chan appwire.ThreadModelSetResponse, 1)
	go func() {
		response, _ := srv.handleAppThreadModelSet(context.Background(), modelControlSetParams("local:workspace", "old", "7", "pick-old", "openai", "gpt-old"))
		done <- response
	}()
	<-started
	installModelControlIdentity(t, srv, "new", "8", "local:workspace")
	newSnapshot := modelControlSnapshot("new", "8", 1, "gpt-new")
	srv.SetThreadEnvelopeSource(&stubThreadEnvelopeSource{modelControl: newSnapshot})
	srv.RefreshThreadEnvelope()
	close(release)
	response := <-done
	if response.ModelControl == nil || response.ModelControl.Authority.InstanceID != "old" {
		t.Fatalf("in-flight response lost its bound authority: %+v", response)
	}
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.ModelControl == nil || !reflect.DeepEqual(*read.Thread.Evener.ModelControl, newSnapshot) {
		t.Fatalf("retired request overwrote replacement: %+v", read.Thread.Evener.ModelControl)
	}
}

func TestModelControlNotificationMatchesThreadRead(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
	snapshot := modelControlSnapshot("instance-1", "7", 2, "gpt-5.5")
	source := &stubThreadEnvelopeSource{modelControl: snapshot}
	srv.SetThreadEnvelopeSource(source)
	feedBridge(srv, events.SessionEvent{Kind: events.EventModelControlChanged, SessionID: "instance-1", Data: events.ModelControlChangedData{Snapshot: snapshot}})
	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	var notified *appwire.ModelControlSnapshot
	for _, notification := range srv.AppNotificationsAfter(0, "instance-1") {
		if notification.Notification.Method != appwire.NotifyThreadModelControlChanged {
			continue
		}
		var params appwire.ThreadModelControlChangedParams
		if err := json.Unmarshal(notification.Notification.Params, &params); err != nil {
			t.Fatal(err)
		}
		notified = &params.ModelControl
	}
	if notified == nil || read.Thread.Evener.ModelControl == nil || !reflect.DeepEqual(*notified, *read.Thread.Evener.ModelControl) {
		t.Fatalf("notification/read differ: notification=%+v read=%+v", notified, read.Thread.Evener.ModelControl)
	}
}

// A clear publishes the replacement identity and its resync in ONE commit, and
// only re-seeds the envelope from the replacement session afterwards
// (cmd/evener/serve_wiring.go). A client that finishes its resync read inside
// that window sees the new instance carrying no modelControlVersion, reduces it
// to `unsupported`, and leaves the picker disabled on a healthy replacement
// until some later refresh or reconnect. The replacement's model-control facet
// is therefore installed in the identity's own commit.
//
// The window is modelled exactly: the swap runs, and the read happens BEFORE
// RefreshThreadEnvelope, which is where a second client's resync read lands.
func TestReplaceAppIdentityPublishesTheReplacementModelControl(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "old", "7", "local:workspace")
	// Every other facet stays populated, so the assertion below also proves the
	// commit installs model control ALONE and leaves the post-commit seed to do
	// the rest.
	source := &stubThreadEnvelopeSource{
		modelControl: modelControlSnapshot("new", "8", 1, "gpt-new"),
		queue:        appwire.QueueState{Depth: 3, Revision: 2},
		meta:         schema.SessionMeta{ID: "new", Name: "the replacement session"},
	}
	srv.SetThreadEnvelopeSource(source)

	prepared, err := PrepareAppIdentityForRef("local", "new", "local:workspace", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("8").WithModelControlSnapshot(source.modelControl), nil)

	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.InstanceID != "new" {
		t.Fatalf("instance = %q, want the replacement", read.Thread.Evener.InstanceID)
	}
	if read.Thread.Evener.Capabilities.ModelControlVersion != appwire.ModelControlVersion1 {
		t.Fatalf("modelControlVersion = %d, want 1: the replacement identity was published without its model-control facet",
			read.Thread.Evener.Capabilities.ModelControlVersion)
	}
	if read.Thread.Evener.ModelControl == nil || !reflect.DeepEqual(*read.Thread.Evener.ModelControl, source.modelControl) {
		t.Fatalf("modelControl = %+v, want %+v", read.Thread.Evener.ModelControl, source.modelControl)
	}
	if read.Thread.Evener.Queue.Depth != 0 || read.Thread.Name != "" {
		t.Fatalf("identity commit installs other facets: queue=%+v name=%q", read.Thread.Evener.Queue, read.Thread.Name)
	}
}

// The commit carries a facet only when it speaks for the identity being
// installed. A snapshot sampled from the session being retired would advertise
// an authority the replacement cannot answer for, which is worse than
// advertising none: the client would hold `ready` and fence its pick against a
// daemon that no longer exists.
func TestReplaceAppIdentityDropsAModelControlSnapshotForAnotherAuthority(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "old", "7", "local:workspace")
	retired := modelControlSnapshot("old", "7", 2, "gpt-old")
	prepared, err := PrepareAppIdentityForRef("local", "new", "local:workspace", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared.WithBootGeneration("8").WithModelControlSnapshot(retired), nil)

	read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.ModelControl != nil || read.Thread.Evener.Capabilities.ModelControlVersion != 0 {
		t.Fatalf("retired authority published under the replacement: modelControl=%+v version=%d",
			read.Thread.Evener.ModelControl, read.Thread.Evener.Capabilities.ModelControlVersion)
	}
}
