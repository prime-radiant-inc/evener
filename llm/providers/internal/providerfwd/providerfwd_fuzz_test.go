package providerfwd

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
)

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func FuzzProviderForwarders(f *testing.F) {
	f.Add(false, "", "proxy")
	f.Add(true, "instance", "proxy")

	f.Fuzz(func(t *testing.T, enableCounter bool, instanceName, defaultName string) {
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
	})
}
