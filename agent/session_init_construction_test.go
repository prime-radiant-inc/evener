package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// constructionFacts is the field state NewSession and
// RestoreSessionFromMetaWithConfig must leave identical for the same spawn
// carrier: delegation allowance, tree counters, activity clock and job manager
// wiring. Live pointers are compared by identity or presence, never by value.
type constructionFacts struct {
	delegationAllowance int
	treeLimit           int64
	driveLimit          int64
	cfgMirrorsTree      bool
	cfgMirrorsDrive     bool
	hasActivityClock    bool
	cfgMirrorsClock     bool
	parentDelegateID    string
	jobHooks            map[string]bool
}

func factsOf(s *Session) constructionFacts {
	jm := s.jobManager
	return constructionFacts{
		delegationAllowance: s.delegationAllowance,
		treeLimit:           s.treeCounter.limit,
		driveLimit:          s.driveCounter.limit,
		cfgMirrorsTree:      s.cfg.spawn.treeCounter == s.treeCounter,
		cfgMirrorsDrive:     s.cfg.spawn.driveCounter == s.driveCounter,
		hasActivityClock:    s.jobActivityClock != nil,
		cfgMirrorsClock:     s.cfg.spawn.jobActivityClock == s.jobActivityClock,
		parentDelegateID:    jm.parentDelegateID,
		jobHooks: map[string]bool{
			"forward":            jm.forward != nil,
			"enqueue":            jm.enqueue != nil,
			"consume":            jm.consume != nil,
			"holdWake":           jm.holdWake != nil,
			"notifySystem":       jm.notifySystem != nil,
			"wake":               jm.wake != nil,
			"emit":               jm.emit != nil,
			"currentProvenance":  jm.currentProvenance != nil,
			"clock":              jm.clock != nil,
			"now":                jm.now != nil,
			"delegateController": jm.delegateController == s.delegateController,
			"retirementOwner":    jm.retirementOwner == s,
		},
	}
}

func equalFacts(a, b constructionFacts) bool { return reflect.DeepEqual(a, b) }

func TestNewSessionAndRestoreAgreeOnSharedConstruction(t *testing.T) {
	t.Parallel()
	hooks := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}
	newClient := func() *llm.Client {
		client := llm.NewClient()
		client.Register(&fakeAdapter{name: "openai"})
		return client
	}
	rootFacts := func(t *testing.T) (constructionFacts, constructionFacts) {
		stateDir := t.TempDir()
		fresh, err := NewSession(newClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: stateDir, testOnly: hooks})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(fresh.Close)
		meta := schema.SessionMeta{ID: "01RESTORECONSTRUCTROOT0001", ProfileID: "openai", Model: "gpt-5.2", Config: (SessionConfig{}).toSnapshot()}
		restored, err := RestoreSessionFromMetaWithConfig(newClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir, testOnly: hooks})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(restored.Close)
		return factsOf(fresh), factsOf(restored)
	}

	t.Run("root session mints the tree counters", func(t *testing.T) {
		t.Parallel()
		fresh, restored := rootFacts(t)
		if !equalFacts(fresh, restored) {
			t.Fatalf("root construction differs:\nnew      %+v\nrestored %+v", fresh, restored)
		}
		if !fresh.hasActivityClock || !fresh.cfgMirrorsTree || !fresh.cfgMirrorsDrive || !fresh.cfgMirrorsClock {
			t.Fatalf("root session did not mint and mirror its counters: %+v", fresh)
		}
	})

	t.Run("child session inherits the carrier", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		tree, drive := newTreeCounter(3), newTreeCounter(2)
		clk := newJobActivityClock("01PARENTCONSTRUCT00000001")
		spawn := spawnConfig{
			parentSessionID:     "01PARENTCONSTRUCT00000001",
			parentDelegateID:    "delegate-1",
			delegationAllowance: 2,
			treeCounter:         tree,
			driveCounter:        drive,
			jobActivityClock:    clk,
			forwardJobEvent:     func(jobstore.Event) error { return nil },
		}
		newSpawn := spawn
		childID, err := identifier.NewSessionID()
		if err != nil {
			t.Fatal(err)
		}
		newSpawn.sessionID = childID
		fresh, err := NewSession(newClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: stateDir, testOnly: hooks, spawn: newSpawn})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(fresh.Close)
		meta := schema.SessionMeta{ID: "01CHILDRESTORE00000000001", ProfileID: "openai", Model: "gpt-5.2", Config: (SessionConfig{}).toSnapshot()}
		restored, err := RestoreSessionFromMetaWithConfig(newClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir, testOnly: hooks, spawn: spawn})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		t.Cleanup(restored.Close)
		for name, s := range map[string]*Session{"new": fresh, "restored": restored} {
			if s.treeCounter != tree || s.driveCounter != drive || s.jobActivityClock != clk || s.delegationAllowance != 2 {
				t.Fatalf("%s child did not inherit the carrier: allowance=%d", name, s.delegationAllowance)
			}
		}
		if !equalFacts(factsOf(fresh), factsOf(restored)) {
			t.Fatalf("child construction differs:\nnew      %+v\nrestored %+v", factsOf(fresh), factsOf(restored))
		}
	})
}
