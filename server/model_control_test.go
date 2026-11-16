package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
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
					ModelControlVersion: appwire.ModelControlVersion1,
					Disposition:         appwire.ModelControlDispositionAccepted,
					ModelControl:        &snapshot,
				}, nil
			},
		}, nil
	})
	params := appwire.ThreadModelSetParams{
		Ref: "local:workspace", ModelProvider: "openrouter", Model: "anthropic/claude-opus",
		ClientMutationID: "pick-1", ExpectedInstanceID: "instance-1", ExpectedBootGeneration: "7",
	}
	response, err := srv.handleAppThreadModelSet(context.Background(), params)
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
		name   string
		params appwire.ThreadModelSetParams
	}{
		{name: "partial", params: appwire.ThreadModelSetParams{Model: "gpt-5.5", ClientMutationID: "pick-1", ExpectedInstanceID: "instance-1"}},
		{name: "stale instance", params: appwire.ThreadModelSetParams{Model: "gpt-5.5", ClientMutationID: "pick-1", ExpectedInstanceID: "old", ExpectedBootGeneration: "7"}},
		{name: "stale generation", params: appwire.ThreadModelSetParams{Model: "gpt-5.5", ClientMutationID: "pick-1", ExpectedInstanceID: "instance-1", ExpectedBootGeneration: "6"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
			called := false
			srv.SetModelControlBindFunc(func(appwire.ModelControlAuthority) (ModelControlBinding, error) {
				called = true
				return ModelControlBinding{}, nil
			})
			tc.params.Ref = "local:workspace"
			if _, err := srv.handleAppThreadModelSet(context.Background(), tc.params); err == nil {
				t.Fatal("expected authority error")
			}
			if called {
				t.Fatal("invalid authority reached the binder")
			}
		})
	}
}

func TestHandleAppThreadModelSet_LegacyRequestStillBindsCurrentAuthority(t *testing.T) {
	srv := NewServer(ServerConfig{})
	installModelControlIdentity(t, srv, "instance-1", "7", "local:workspace")
	snapshot := modelControlSnapshot("instance-1", "7", 2, "gpt-5.5")
	source := &stubThreadEnvelopeSource{modelControl: snapshot}
	srv.SetThreadEnvelopeSource(source)
	srv.SetModelControlBindFunc(func(authority appwire.ModelControlAuthority) (ModelControlBinding, error) {
		if authority != snapshot.Authority {
			t.Fatalf("authority = %+v, want %+v", authority, snapshot.Authority)
		}
		return ModelControlBinding{Envelope: source, Set: func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
			if params.ClientMutationID != "" || params.ExpectedInstanceID != "" || params.ExpectedBootGeneration != "" {
				t.Fatalf("legacy identity changed: %+v", params)
			}
			return appwire.ThreadModelSetResponse{ModelControlVersion: 1, Disposition: appwire.ModelControlDispositionAccepted, ModelControl: &snapshot}, nil
		}}, nil
	})
	if _, err := srv.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Ref: "local:workspace", Model: "gpt-5.5"}); err != nil {
		t.Fatal(err)
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
			return appwire.ThreadModelSetResponse{ModelControlVersion: 1, Disposition: appwire.ModelControlDispositionAccepted, ModelControl: &oldSnapshot}, nil
		}}, nil
	})
	done := make(chan appwire.ThreadModelSetResponse, 1)
	go func() {
		response, _ := srv.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Ref: "local:workspace", Model: "gpt-old"})
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
