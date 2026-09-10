package main

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

func TestNotesCanonicalEnvelopeProjection(t *testing.T) {
	// Expected values are fixtures, independent of the production normalizer.
	for _, tc := range []struct{ name, raw, want string }{
		{"normal", "  opaque\t control\n", "opaque control"},
		{"clamp-at-space", strings.Repeat("x", 999) + " y", strings.Repeat("x", 999) + " "},
		{"unicode", strings.Repeat("界", 999) + "🛰界", strings.Repeat("界", 999) + "🛰"},
		{"clear", " \t\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sess, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.4-mini"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			srv := server.NewServer(server.ServerConfig{})
			srv.SetAppIdentity("local", sess.ID())
			srv.SetThreadEnvelopeSource(liveThreadEnvelopeSource{session: func() agent.EnvelopeSampling { return sess }})
			srv.SetNotesHumanSetFunc(sess.SetHumanNote)
			conn := srv.AppServer().NewConnection("canonical-notes")
			ctx := context.Background()
			initialized := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
			if initialized.Kind() != appwire.MessageResponse {
				t.Fatalf("initialize: %+v", initialized.Error)
			}
			ref := "local:" + sess.ID()
			// Establish actual content before testing clear, rather than clearing an
			// already empty session. Both writes use the real AppWire mutation path.
			seed := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodNotesHumanSet, appwire.NotesHumanSetParams{Ref: ref, ClientMutationID: "seed", ExpectedInstanceID: sess.ID(), Note: "seed"}))
			if seed.Kind() != appwire.MessageResponse {
				t.Fatalf("seed: %+v", seed.Error)
			}
			saved := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodNotesHumanSet, appwire.NotesHumanSetParams{Ref: ref, ClientMutationID: "save", ExpectedInstanceID: sess.ID(), Note: tc.raw}))
			if saved.Kind() != appwire.MessageResponse {
				t.Fatalf("save: %+v", saved.Error)
			}
			response := saved.Response.Result.(appwire.NotesHumanSetResponse)
			durable, present, err := agent.ReadCanonicalHumanNote(dir, sess.ID())
			if err != nil || !present {
				t.Fatalf("canonical filesystem read: present=%v err=%v", present, err)
			}
			srv.RefreshThreadEnvelope()
			read := conn.HandleMessage(ctx, appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref}))
			if read.Kind() != appwire.MessageResponse {
				t.Fatalf("thread/read: %+v", read.Error)
			}
			projected := read.Response.Result.(appwire.ThreadReadResponse).Thread.Evener.HumanNote
			for _, result := range []struct{ boundary, got string }{
				{"response", response.Note},
				{"durable", durable},
				{"thread/read", projected},
			} {
				if result.got != tc.want {
					t.Errorf("%s = %q (%d runes), want %q (%d runes)", result.boundary, result.got, len([]rune(result.got)), tc.want, len([]rune(tc.want)))
				}
			}
		})
	}
}
