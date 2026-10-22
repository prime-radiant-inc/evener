package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// contextStatusMessageChars is the size of every fixture message text. The
// local estimator charges text-only messages at len(text)/4 tokens, so these
// values make the expected token counts exact char/4 arithmetic.
const contextStatusMessageChars = 1024

func contextStatusText() string { return strings.Repeat("x", contextStatusMessageChars) }

// newContextStatusLargeFixture builds a Session directly, without a recorded
// measurement, so the estimator covers the whole history. That pinned state is
// what makes the sample allocate one []llm.Message; a measurement at the final
// history length would leave an empty tail and hide that allocation.
func newContextStatusLargeFixture(turns int) (*Session, *provider.Profile) {
	profile := NewOpenAIProfile("gpt-5.2")
	text := contextStatusText()
	history := make([]schema.Turn, 0, turns)
	for i := range turns {
		if i%2 == 0 {
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(text)))
		} else {
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(text)))
		}
	}
	return &Session{
		profile:    profile,
		contextMgr: contextmgr.NewManager(profile, nil, nil),
		history:    history,
	}, profile
}

// benchmarkContextStatusSampling is the shared benchmark body for the paired
// baseline/candidate trees. sample is each tree's only differing adapter: the
// pinned baseline has no paired call, so its adapter samples metrics then
// pressure; the candidate adapter samples ContextStatus once.
func benchmarkContextStatusSampling(b *testing.B, sample func(*Session)) {
	b.Helper()
	cases := []struct {
		name     string
		turns    int
		measured bool
	}{
		{name: "10turns", turns: 10},
		{name: "1000turns", turns: 1000},
		{name: "10000turns", turns: 10000},
		{name: "10000turnsMeasured", turns: 10000, measured: true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			s, _ := newContextStatusLargeFixture(tc.turns)
			if tc.measured {
				s.contextMgr.RecordInputTokens(40000, 5000)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sample(s)
			}
		})
	}
}
