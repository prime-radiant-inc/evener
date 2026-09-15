package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// A thread LIST page resolves every row's watches in one call, so that call has
// to stay linear in the page: one pass over the page's managers, not one pass
// per row. This measures it over a page of subagent rows, which is the shape a
// delegate- or subagent-heavy session actually presents.
func BenchmarkLiveWatchRowsForSessions(b *testing.B) {
	root := benchWatchTreeSession(b)
	ids := make([]string, 0, benchPageSessions+1)
	ids = append(ids, root.ID())
	for index := range benchPageSessions {
		child := benchWatchTreeSession(b)
		root.subagents.mu.Lock()
		root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
		root.subagents.mu.Unlock()
		child.jobManager.mu.Lock()
		for slot := range benchWatchesPerManager {
			target := fmt.Sprintf("job_page_%02d_%02d", index, slot)
			child.jobManager.watches[watchKey{VisibleSessionID: child.ID(), Target: target}] = &watchConfig{
				id: target, watchID: target, sourcePublic: "self", target: target, createdAt: frozenTestTime,
			}
		}
		child.jobManager.mu.Unlock()
		ids = append(ids, child.ID())
	}

	b.ReportAllocs()
	for b.Loop() {
		if rows := root.LiveWatchRowsForSessions(ids); len(rows) != len(ids) {
			b.Fatalf("page rows = %d, want %d", len(rows), len(ids))
		}
	}
}

const (
	benchPageSessions      = 48
	benchWatchesPerManager = 4
)

func benchWatchTreeSession(b *testing.B) *Session {
	b.Helper()
	dir := b.TempDir()
	sess, err := NewSession(benchClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         dir,
		testOnly:         testConfig{forceRealIO: true},
	})
	if err != nil {
		b.Fatalf("NewSession: %v", err)
	}
	b.Cleanup(sess.Close)
	return sess
}
