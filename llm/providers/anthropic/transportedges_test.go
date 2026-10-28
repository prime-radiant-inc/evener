package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// streamingRequiredAuth is a stub authenticator whose transport answers every
// request as a stream: it is what routes Complete through CompleteViaStream
// (protocolhttp.RequiresStreamingComplete). Registered under a test-only scheme
// so the anthropic transport can be driven through the streaming-complete path
// without a real Codex backend.
type streamingRequiredAuth struct{}

func (streamingRequiredAuth) Apply(context.Context, *http.Request, registry.Resolved) error {
	return nil
}
func (streamingRequiredAuth) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (streamingRequiredAuth) RequiresStreamingComplete() bool { return true }

const streamingRequiredScheme = "anthropic-test-streaming-required"

var registerStreamingRequiredOnce sync.Once

func registerStreamingRequiredAuth(t *testing.T) {
	t.Helper()
	registerStreamingRequiredOnce.Do(func() {
		llm.RegisterAuthenticator(streamingRequiredScheme, streamingRequiredAuth{})
	})
}

// A transport whose auth scheme requires streaming completion must serve
// Complete from the SSE stream and still return the accumulated response.
func TestCompleteRoutesThroughStreamWhenPreparerRequiresIt(t *testing.T) {
	registerStreamingRequiredAuth(t)
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, messagesSSE })
	res := protoLive(srv)
	res.Transport.Auth = streamingRequiredScheme
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text() != "hello" || resp.Provider != "anthropic-prod" {
		t.Fatalf("streamed Complete = %+v", resp)
	}
}

// A request the builder rejects (an audio content part) must fail Complete,
// Stream, and CountTokens before any HTTP traffic, as a configuration error.
func TestBuildBodyErrorsPropagateThroughCompleteStreamCountTokens(t *testing.T) {
	req := protoReq("")
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte{1}}},
	}})
	res := protoRes(nil)
	for name, run := range map[string]func() error{
		"complete": func() error {
			_, err := (&Protocol{}).Complete(context.Background(), req, res)
			return err
		},
		"stream": func() error {
			_, err := (&Protocol{}).Stream(context.Background(), req, res)
			return err
		},
		"count_tokens": func() error {
			_, err := (&Protocol{}).CountTokens(context.Background(), req, res)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
				t.Fatalf("BuildBody failure must surface as a configuration error, got %T %v", err, err)
			}
		})
	}
}

// A base URL that cannot parse must fail models.list before any request goes
// out, through withQuery's own parse error.
func TestListModelsUnparseableURL(t *testing.T) {
	res := protoRes(nil)
	res.Transport.BaseURL = "http://h/%zz"
	res.Transport.ModelsEndpoint = "/models"
	_, err := (&Protocol{}).ListModels(context.Background(), res)
	if err == nil {
		t.Fatal("an unparseable base URL must fail models.list")
	}
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		t.Fatalf("err = %T %v, want the url parse error", err, err)
	}
}
