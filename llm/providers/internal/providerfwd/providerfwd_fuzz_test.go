package providerfwd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func FuzzProviderForwarders(f *testing.F) {
	f.Add(false, "", "proxy")
	f.Add(true, "instance", "proxy")

	f.Fuzz(func(t *testing.T, enableCounter bool, instanceName, defaultName string) {
		openaiForwarder := NewOpenAICompat(instanceName, defaultName, &openaicompat.Adapter{})
		if openaiForwarder.Name() == "" && (instanceName != "" || defaultName != "") {
			t.Fatal("OpenAI-compatible forwarder lost its name")
		}
		_ = NewOpenAICompat("", defaultName, nil).Name()
		_ = NewAnthropic(instanceName, defaultName, nil).Name()
		_ = NewAnthropic("", defaultName, nil).Name()
		if _, err := NewAnthropicWithInputTokenCounter(instanceName, defaultName, nil).CountInputTokens(context.Background(), llm.Request{}); !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
			t.Fatalf("nil backing error = %v", err)
		}
		backingErr := errors.New("fuzz transport failure")
		backing := &anthropic.Adapter{
			APIKey:  "fuzz-key",
			BaseURL: "https://anthropic.invalid",
			Client:  &http.Client{Transport: failingRoundTripper{err: backingErr}},
		}

		var a *Anthropic
		if enableCounter {
			a = NewAnthropicWithInputTokenCounter(instanceName, defaultName, backing)
		} else {
			a = NewAnthropic(instanceName, defaultName, backing)
		}
		got, err := a.CountInputTokens(context.Background(), llm.Request{Model: "fuzz-model"})
		if !enableCounter {
			if !errors.Is(err, llm.ErrInputTokenCountUnsupported) {
				t.Fatalf("disabled counter error = %v", err)
			}
			return
		}
		if !errors.Is(err, backingErr) {
			t.Fatalf("enabled counter error = %v, want transport failure", err)
		}
		if got.Tokens != 0 || got.Provider != "" || got.Exact || got.Source != "" {
			t.Fatalf("error result = %+v, want zero value", got)
		}

		success := &anthropic.Adapter{
			APIKey:  "fuzz-key",
			BaseURL: "https://anthropic.invalid",
			Client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"input_tokens":7}`)), Header: make(http.Header)}, nil
			})},
		}
		count, err := NewAnthropicWithInputTokenCounter(instanceName, defaultName, success).CountInputTokens(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("x")}})
		if err != nil || count.Tokens != 7 || count.Provider != NewAnthropic(instanceName, defaultName, nil).Name() {
			t.Fatalf("success = (%+v, %v)", count, err)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
