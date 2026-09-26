package appsource

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

const (
	remoteImageHost     = "host"
	remoteImageSession  = "02wRemoteImageSession00000"
	remoteImageSha      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	remoteImageSha2     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	remoteImageExternal = "https://images.example.test/plot.png"
	remoteImageInline   = "data:image/png;base64,iVBORw0KGgo="

	// wantRemoteImageShaRoute and wantRemoteImageDocRoute are the host-qualified
	// controller routes the remote hub's own stamps must become. The doc form
	// escapes the ":" in the session id exactly as resolveOutputImageFile writes
	// the local form.
	wantRemoteImageShaRoute = "/s/host:" + remoteImageSession + "/images/" + remoteImageSha
	wantRemoteImageDocRoute = "/doc/image?session=host%3A" + remoteImageSession + "&path=shot.png"
)

func remoteImageShaRoute() string { return "/s/" + remoteImageSession + "/images/" + remoteImageSha }
func remoteImageDocRoute() string {
	return "/doc/image?session=" + remoteImageSession + "&path=shot.png"
}

// remoteImageItemFixture is one tool-result item carrying both image fields the
// remote hub stamps: Images[].URL (replayed user-input images, sha-addressed)
// and OutputImages[].URL (tool-result thumbnails, sha-addressed or
// file-backed). External and inline URLs name their own origin and must
// survive; the already host-qualified route pins that the rewrite is
// idempotent.
func remoteImageItemFixture() appwire.ThreadItem {
	return appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "item-image",
		TranscriptKey: "key-image",
		Position:      &appwire.ThreadItemPosition{Entry: 0},
		ToolName:      "shell",
		CallID:        "call-image",
		ArgumentsJSON: `{}`,
		Status:        appwire.TurnStatusCompleted,
		Images: []appwire.InputItem{
			{Metadata: map[string]string{"sha": remoteImageSha}, URL: remoteImageShaRoute()},
			{URL: remoteImageExternal},
			{URL: "/s/host:other/images/" + remoteImageSha2},
		},
		OutputImages: []appwire.OutputImage{
			{Source: "tool-result", SHA: remoteImageSha, URL: remoteImageShaRoute()},
			{Source: "written-file", Path: "shot.png", URL: remoteImageDocRoute()},
			{Source: "inline", URL: remoteImageInline},
		},
	}
}

func remoteImageTurnFixture() appwire.Turn {
	return appwire.Turn{
		ID:     "turn-1",
		Status: appwire.TurnStatusCompleted,
		Items:  []appwire.ThreadItem{remoteImageItemFixture()},
	}
}

func remoteImageThreadFixture() appwire.Thread {
	return appwire.Thread{
		ID:        "t1",
		SessionID: "t1",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:t1"},
		Turns:     []appwire.Turn{remoteImageTurnFixture()},
	}
}

func assertRemoteImageItemRewritten(t *testing.T, label string, item appwire.ThreadItem) {
	t.Helper()
	if len(item.Images) != 3 || len(item.OutputImages) != 3 {
		t.Fatalf("%s: images = %+v outputImages = %+v, want both fields preserved", label, item.Images, item.OutputImages)
	}
	for index, want := range []string{wantRemoteImageShaRoute, remoteImageExternal, "/s/host:other/images/" + remoteImageSha2} {
		if got := item.Images[index].URL; got != want {
			t.Fatalf("%s: Images[%d].URL = %q, want %q", label, index, got, want)
		}
	}
	for index, want := range []string{wantRemoteImageShaRoute, wantRemoteImageDocRoute, remoteImageInline} {
		if got := item.OutputImages[index].URL; got != want {
			t.Fatalf("%s: OutputImages[%d].URL = %q, want %q", label, index, got, want)
		}
	}
}

// Every response carrier that embeds a Thread, Turn, or ThreadItem must run the
// shared image visitor: a remote hub stamps its own origin-relative routes, and
// a URL left unrewritten makes the browser read this hub's own state for a
// session that lives on another machine.
func TestRemoteHubTranslateOutRewritesStampedImageURLs(t *testing.T) {
	source := NewRemoteHubSource(remoteImageHost, nil, nil)

	t.Run("thread/read", func(t *testing.T) {
		out := &appwire.ThreadReadResponse{Thread: remoteImageThreadFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/read", out.Thread.Turns[0].Items[0])
	})

	t.Run("thread/list", func(t *testing.T) {
		out := &appwire.ThreadListResponse{Data: []appwire.Thread{remoteImageThreadFixture()}}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/list", out.Data[0].Turns[0].Items[0])
	})

	t.Run("thread/turns/list", func(t *testing.T) {
		out := &appwire.ThreadTurnsListResponse{Data: []appwire.Turn{remoteImageTurnFixture()}}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/turns/list", out.Data[0].Items[0])
	})

	t.Run("thread/start", func(t *testing.T) {
		out := &appwire.ThreadStartResponse{Thread: remoteImageThreadFixture(), Turn: remoteImageTurnFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/start thread", out.Thread.Turns[0].Items[0])
		assertRemoteImageItemRewritten(t, "thread/start turn", out.Turn.Items[0])
	})

	t.Run("thread/resume", func(t *testing.T) {
		out := &appwire.ThreadResumeResponse{Thread: remoteImageThreadFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/resume", out.Thread.Turns[0].Items[0])
	})

	t.Run("thread/fork", func(t *testing.T) {
		out := &appwire.ThreadForkResponse{Thread: remoteImageThreadFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/fork", out.Thread.Turns[0].Items[0])
	})

	t.Run("thread/clear", func(t *testing.T) {
		out := &appwire.ThreadClearResponse{Thread: remoteImageThreadFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/clear", out.Thread.Turns[0].Items[0])
	})

	t.Run("turn/start", func(t *testing.T) {
		out := &appwire.TurnStartResponse{Turn: remoteImageTurnFixture()}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "turn/start", out.Turn.Items[0])
	})

	t.Run("thread/turns/items/list", func(t *testing.T) {
		out := &appwire.ThreadTurnItemsListResponse{Data: []appwire.ThreadItem{remoteImageItemFixture()}}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "thread/turns/items/list", out.Data[0])
	})

	t.Run("evener/subagent/preview", func(t *testing.T) {
		out := &appwire.EvenerSubagentPreviewResponse{Items: []appwire.ThreadItem{remoteImageItemFixture()}}
		if err := source.translateOut(out); err != nil {
			t.Fatalf("translateOut: %v", err)
		}
		assertRemoteImageItemRewritten(t, "evener/subagent/preview", out.Items[0])
	})
}

// The notification carriers are the other half of the same rule: thread/started
// carries a Thread, turn/started and turn/completed carry a Turn, and
// item/started and item/completed carry a ThreadItem. Each must be rewritten in
// place, and fields the translator does not understand must still survive
// byte-for-byte.
func TestRemoteHubTranslateNotificationRewritesStampedImageURLs(t *testing.T) {
	source := NewRemoteHubSource(remoteImageHost, nil, nil)

	t.Run("thread/started", func(t *testing.T) {
		params, err := json.Marshal(appwire.ThreadStartedParams{
			ThreadID: "t1", Ref: "local:t1", Thread: remoteImageThreadFixture(),
		})
		if err != nil {
			t.Fatal(err)
		}
		translated, threadID, ok := source.translateNotification(appwire.Notification{
			Method: appwire.NotifyThreadStarted, Params: params,
		})
		if !ok || threadID != "t1" {
			t.Fatalf("translateNotification = (%+v, %q, %v), want thread t1 routed", translated, threadID, ok)
		}
		started := decodeNotificationParams[appwire.ThreadStartedParams](t, translated)
		assertRemoteImageItemRewritten(t, "thread/started", started.Thread.Turns[0].Items[0])
	})

	t.Run("turn/started", func(t *testing.T) {
		params, err := json.Marshal(appwire.TurnStartedParams{
			ThreadID: "t1", Ref: "local:t1", Turn: remoteImageTurnFixture(),
		})
		if err != nil {
			t.Fatal(err)
		}
		translated, _, ok := source.translateNotification(appwire.Notification{
			Method: appwire.NotifyTurnStarted, Params: params,
		})
		if !ok {
			t.Fatalf("translateNotification dropped the frame: %+v", translated)
		}
		started := decodeNotificationParams[appwire.TurnStartedParams](t, translated)
		assertRemoteImageItemRewritten(t, "turn/started", started.Turn.Items[0])
	})

	t.Run("turn/completed", func(t *testing.T) {
		params, err := json.Marshal(appwire.TurnCompletedParams{
			ThreadID: "t1", Ref: "local:t1", Turn: remoteImageTurnFixture(),
		})
		if err != nil {
			t.Fatal(err)
		}
		translated, _, ok := source.translateNotification(appwire.Notification{
			Method: appwire.NotifyTurnCompleted, Params: params,
		})
		if !ok {
			t.Fatalf("translateNotification dropped the frame: %+v", translated)
		}
		completed := decodeNotificationParams[appwire.TurnCompletedParams](t, translated)
		assertRemoteImageItemRewritten(t, "turn/completed", completed.Turn.Items[0])
	})

	for _, method := range []string{appwire.NotifyItemStarted, appwire.NotifyItemCompleted} {
		t.Run(method, func(t *testing.T) {
			params, err := json.Marshal(appwire.ItemLifecycleParams{
				ThreadID: "t1", Ref: "local:t1", TurnID: "turn-1", Item: remoteImageItemFixture(),
			})
			if err != nil {
				t.Fatal(err)
			}
			// A field this hub does not understand must reach the client intact.
			params = append(params[:len(params)-1], []byte(`,"future":{"kept":true}}`)...)
			translated, _, ok := source.translateNotification(appwire.Notification{Method: method, Params: params})
			if !ok {
				t.Fatalf("translateNotification dropped the frame: %+v", translated)
			}
			item := decodeNotificationParams[appwire.ItemLifecycleParams](t, translated)
			assertRemoteImageItemRewritten(t, method, item.Item)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(translated.Params, &fields); err != nil {
				t.Fatalf("decode translated params %s: %v", translated.Params, err)
			}
			if _, ok := fields["future"]; !ok {
				t.Fatalf("translated params %s dropped the unknown future field", translated.Params)
			}
		})
	}
}

// An origin-relative image URL a remote hub minted that cannot be qualified as
// one of its stamped forms must not reach the browser as a controller-relative
// path: it is meaningful only against the remote origin. Scheme URLs and
// network-path references name their own origin and stay untouched.
func TestRemoteHubImageVisitorNeutralizesUnqualifiableRoutes(t *testing.T) {
	source := NewRemoteHubSource(remoteImageHost, nil, nil)
	out := &appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: "t1", SessionID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{ID: "turn-1", Items: []appwire.ThreadItem{{
			Type: "commandExecution",
			Images: []appwire.InputItem{
				{URL: "/s/" + remoteImageSession + "/not-an-image-route"},
				{URL: "//cdn.example.test/shot.png"},
			},
			OutputImages: []appwire.OutputImage{
				{Source: "tool-result", URL: "/doc/other?session=" + remoteImageSession},
				{Source: "external", URL: remoteImageExternal},
			},
		}}}},
	}}
	if err := source.translateOut(out); err != nil {
		t.Fatalf("translateOut: %v", err)
	}
	item := out.Thread.Turns[0].Items[0]
	if got := item.Images[0].URL; got != "" {
		t.Fatalf("unqualifiable route = %q, want it neutralized", got)
	}
	if got := item.Images[1].URL; got != "//cdn.example.test/shot.png" {
		t.Fatalf("network-path reference = %q, want it preserved", got)
	}
	if got := item.OutputImages[0].URL; got != "" {
		t.Fatalf("unqualifiable doc route = %q, want it neutralized", got)
	}
	if got := item.OutputImages[1].URL; got != remoteImageExternal {
		t.Fatalf("external URL = %q, want it preserved", got)
	}
}

// The rewrite is idempotent: a route that is already host-qualified is a
// controller route this hub serves, so a second pass must leave it alone. This
// is what lets the hub's own neutralization pass run after the source's
// translation without undoing it.
func TestRemoteHubImageVisitorLeavesHostQualifiedRoutesAlone(t *testing.T) {
	source := NewRemoteHubSource(remoteImageHost, nil, nil)
	thread := remoteImageThreadFixture()
	for pass := range 2 {
		if err := source.translateOut(&appwire.ThreadReadResponse{Thread: thread}); err != nil {
			t.Fatalf("translateOut pass %d: %v", pass, err)
		}
	}
	assertRemoteImageItemRewritten(t, "second pass", thread.Turns[0].Items[0])
}

// A remote payload must never be able to aim the browser at this hub's own
// filesystem (a "local:"-sourced route resolves here) or at another source's
// route: neither is one of the stamped forms, and neither can be qualified for
// this host, so both are blanked rather than left. A URL that names its own
// origin (a scheme, or a network-path reference) is not a route of this hub and
// still passes through.
func TestRemoteHubImageVisitorNeutralizesForeignSourceRoutes(t *testing.T) {
	source := NewRemoteHubSource(remoteImageHost, nil, nil)
	out := &appwire.ThreadReadResponse{Thread: appwire.Thread{
		ID: "t1", SessionID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{ID: "turn-1", Items: []appwire.ThreadItem{{
			Type: "commandExecution",
			Images: []appwire.InputItem{
				{URL: "/s/local:" + remoteImageSession + "/images/" + remoteImageSha},
				{URL: "//cdn.example.test/shot.png"},
			},
			OutputImages: []appwire.OutputImage{
				{Source: "tool-result", URL: "/s/nestedhost:" + remoteImageSession + "/images/" + remoteImageSha},
				{Source: "written-file", Path: "shot.png", URL: "/doc/image?session=local%3A" + remoteImageSession + "&path=shot.png"},
			},
		}}}},
	}}
	if err := source.translateOut(out); err != nil {
		t.Fatalf("translateOut: %v", err)
	}
	item := out.Thread.Turns[0].Items[0]
	if got := item.Images[0].URL; got != "" {
		t.Fatalf("controller-local route = %q, want it neutralized", got)
	}
	if got := item.Images[1].URL; got != "//cdn.example.test/shot.png" {
		t.Fatalf("network-path reference = %q, want it preserved", got)
	}
	if got := item.OutputImages[0].URL; got != "" {
		t.Fatalf("another source's route = %q, want it neutralized", got)
	}
	if got := item.OutputImages[1].URL; got != "" {
		t.Fatalf("controller-local doc route = %q, want it neutralized", got)
	}
}
