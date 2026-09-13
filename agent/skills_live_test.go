//go:build eval

package agent

// Live, explicitly opted-in evaluation of model skill selection and reload
// behavior (Task 15 of the 2026-09-11 skills-lifecycle plan). Unlike the
// scripted suite, no responder is scripted: a real provider resolves through
// the configured registry, and the behavior under test is what the MODEL does
// with skill mentions, structured selections, and compaction reloads.
//
// The oracle discipline follows the approved spec: typed activation,
// selection, delivery, and compaction records are the PRIMARY assertions
// (inventory entries, SkillTurnState outcomes/inputs/reload reminders,
// handoff receipts, EventSkillActivated). Opaque fixture markers in the
// model's natural-language output are supplementary evidence of use, never
// the proof of invocation.
//
// Run (smoke gates the corpus; the flag names a verified registry selector):
//
//	EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' \
//	  -count=1 -timeout 45m -args -skills-eval-model=lunaroute/glm-5.3
//
// Without EVENER_LIVE_TESTS=1 this test skips explicitly and issues no
// provider request. SKILLS_EVAL_MODEL in the plan's command is a shell
// variable feeding -skills-eval-model, not a production environment setting.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/liveeval"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// skillsEvalModel selects the registry model the live corpus runs against.
// Required with the opt-in set: an evaluation that silently picked a model
// would not be reproducible.
var skillsEvalModel = flag.String("skills-eval-model", "", "registry model selector for explicitly opted-in skill lifecycle evaluations")

// skillsLiveRecorder collects the session's typed event stream for the cases
// whose oracle includes events. Snapshot is safe to call while the session is
// still running; the goroutine keeps draining until Close.
type skillsLiveRecorder struct {
	mu   sync.Mutex
	evs  []events.SessionEvent
	done chan struct{}
}

func (r *skillsLiveRecorder) drain(ch <-chan events.SessionEvent) {
	go func() {
		defer close(r.done)
		for ev := range ch {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
	}()
}

func (r *skillsLiveRecorder) snapshot() []events.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.SessionEvent(nil), r.evs...)
}

// skillsLiveOutcome is one typed reload/activation outcome.
type skillsLiveOutcome struct {
	Record schema.SkillActivationOutcome
}

// skillsLiveOutcomesByRoute returns every typed outcome whose activation
// carries the given invocation route, across all recorded turn states.
func skillsLiveOutcomesByRoute(s *Session, route string) []skillsLiveOutcome {
	var out []skillsLiveOutcome
	for _, state := range skillTurnStates(s) {
		for _, outcome := range state.Outcomes {
			if outcome.Activation != nil && outcome.Activation.Route == route {
				out = append(out, skillsLiveOutcome{Record: outcome})
			}
		}
	}
	return out
}

// skillsLiveReloadReminder returns the typed reload reminder a compaction
// handoff recorded, if any.
func skillsLiveReloadReminder(s *Session) *schema.SkillReloadReminder {
	for _, state := range skillTurnStates(s) {
		if state.ReloadReminder != nil {
			return state.ReloadReminder
		}
	}
	return nil
}

// skillsLiveSelectionDiff reports the extra and missing names between the
// model's actual reload selection and the expected set declared before the
// run. Returned in deterministic order for stable failure output.
func skillsLiveSelectionDiff(got, want []string) (extra, missing []string) {
	gotSorted := slices.Clone(got)
	wantSorted := slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	extra = skillNamesOnlyIn(gotSorted, wantSorted)
	missing = skillNamesOnlyIn(wantSorted, gotSorted)
	return extra, missing
}

func skillNamesOnlyIn(a, b []string) []string {
	var out []string
	for _, name := range a {
		if !slices.Contains(b, name) {
			out = append(out, name)
		}
	}
	return out
}

// skillsLiveCompactionReceipts returns the distinct typed compaction receipts
// stamped on the session's compaction turns — the durable record of what a
// real fold claimed and which selection it carried. One publication stamps the
// same receipt on its checkpoint and summary turns, so receipts are
// deduplicated by their publication identity.
func skillsLiveCompactionReceipts(s *Session) []schema.SkillCompactionReceipt {
	var out []schema.SkillCompactionReceipt
	seen := make(map[string]bool)
	for _, state := range skillTurnStates(s) {
		if state.Compaction == nil || seen[state.Compaction.Operation.PublicationID] {
			continue
		}
		seen[state.Compaction.Operation.PublicationID] = true
		out = append(out, *state.Compaction)
	}
	return out
}

// skillsLiveCompactionSummary describes every ContextCompaction event in the
// recorded stream — the evidence that real folds dropped content.
func skillsLiveCompactionSummary(evs []events.SessionEvent) string {
	var parts []string
	for _, ev := range evs {
		if ev.Kind != events.EventContextCompaction {
			continue
		}
		if d, ok := ev.Data.(events.ContextCompactionData); ok {
			parts = append(parts, fmt.Sprintf("layer=%s turns %d→%d est-tokens %d→%d",
				d.Layer, d.TurnsBefore, d.TurnsAfter, d.EstTokensBefore, d.EstTokensAfter))
		}
	}
	return strings.Join(parts, "; ")
}

// skillsLiveDiagnose logs the typed lifecycle state and event stream around a
// compaction turn, before any assertion runs, so a failure names exactly which
// machinery ran: whether the model called compact_context, whether an
// operation was persisted, and what the fold did.
func skillsLiveDiagnose(t *testing.T, s *Session, rec *skillsLiveRecorder, stage string) {
	t.Helper()
	meta := s.Meta().Skills
	var pending *schema.SkillCompactionOperation
	var selection *schema.SkillReloadSelection
	if meta != nil {
		pending = meta.PendingCompaction
		selection = meta.PendingSelection
	}
	var calls []string
	for _, ev := range rec.snapshot() {
		if ev.Kind != events.EventToolCallStart {
			continue
		}
		if d, ok := ev.Data.(events.ToolCallStartData); ok {
			calls = append(calls, d.ToolName)
		}
	}
	t.Logf("%s diagnostics: tool calls=%v pending compaction op=%+v pending selection=%+v outstanding handoffs=%+v",
		stage, calls, pending, selection, pendingHandoffsSnapshot(s))
	t.Logf("%s compaction events: %s", stage, skillsLiveCompactionSummary(rec.snapshot()))
	var reloadNames []string
	for _, outcome := range skillsLiveOutcomesByRoute(s, "compaction_reload") {
		reloadNames = append(reloadNames, fmt.Sprintf("%s(%s)", outcome.Record.Identity.Name, outcome.Record.Status))
	}
	if reminder := skillsLiveReloadReminder(s); reminder != nil {
		t.Logf("%s reload reminder recorded: selection=%+v inventory=%v", stage, reminder.Selection, availabilityNames(reminder.Inventory))
	} else {
		t.Logf("%s reload reminder: none recorded", stage)
	}
	t.Logf("%s compaction_reload outcomes=%v turn-stamped receipts=%d", stage, reloadNames, len(skillsLiveCompactionReceipts(s)))
}

// skillsLiveCredentialSource adapts the credentials store's file layer to
// registry.CredentialSource — exactly what cmdutil.StoreCredentialSource does
// for every production binary.
type skillsLiveCredentialSource struct{ store *credentials.Store }

func (s skillsLiveCredentialSource) Lookup(instance string) (string, bool) {
	if s.store == nil {
		return "", false
	}
	return s.store.Get(instance)
}

// skillsLiveCredentialsPath mirrors cmdutil.CredentialsPath (spec §10):
// EVENER_CREDENTIALS_CONFIG, else the sibling of the providers path, else the
// config root's credentials.toml. Duplicated here for the same reason
// liveeval.Paths duplicates cmdutil.ProvidersConfigPath: this eval harness
// stays free of a dependency on the cmd helper layer. Bare registry.Load()
// reads only the provider layer, so without this a key that lives only in the
// store would never reach the request.
func skillsLiveCredentialsPath(home, providersConfig string) string {
	if v, ok := envvars.EVENERCredentialsConfig.LookupEnv(); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if providersConfig != "" {
		return filepath.Join(filepath.Dir(providersConfig), "credentials.toml")
	}
	configHome := envvars.XDGConfigHome.Trimmed()
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "evener", "credentials.toml")
}

// TestSkillsLive runs a known-good smoke on the selected model first — the
// corpus must not be interpreted when the model or its configuration is
// broken — then the ten-case behavior corpus: seven text-input cases, a
// structured inline selection, a compaction reload selection, and the
// absent-selection fallback reminder.
func TestSkillsLive(t *testing.T) {
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		t.Skip("explicit live-test opt-in required")
	}
	if *skillsEvalModel == "" {
		t.Fatal("-skills-eval-model is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stateHome, providersConfig, noUserLayer := liveeval.Paths(envvars.XDGStateHome.Trimmed(), home)
	if noUserLayer {
		t.Fatal("live evaluation requires the configured provider layer")
	}
	t.Setenv(envvars.XDGStateHome.Name, stateHome)
	t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfig)
	credStore, err := credentials.LoadStore(skillsLiveCredentialsPath(home, providersConfig))
	if err != nil {
		t.Fatalf("credentials store: %v", err)
	}
	r, err := registry.Load(registry.WithCredentials(skillsLiveCredentialSource{store: credStore}))
	if err != nil {
		t.Fatal(err)
	}
	{
		client := llm.NewClient(llm.WithRegistry(r))
		prof, err := provider.Resolve(client.Registry(), *skillsEvalModel)
		if err != nil {
			t.Fatal(err)
		}
		// Identifiers only — credentials stay in the provider's normal
		// resolution path and never appear in test output.
		t.Logf("live eval model: selector=%q instance=%q model=%q surface=%q context-window=%d",
			*skillsEvalModel, prof.ID(), prof.Model(), prof.Surface(), prof.ContextWindowSize())
	}
	newCase := func(t *testing.T) *Session {
		t.Helper()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "probe", "---\nname: pkg:probe\ndescription: fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_a983.\n")
		writeSkillMD(t, root, "second", "---\nname: pkg:second\ndescription: second fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_b742.\n")
		client := llm.NewClient(llm.WithRegistry(r))
		prof, err := provider.Resolve(client.Registry(), *skillsEvalModel)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(root), SessionConfig{
			AgentsDocPath: filepath.Join(root, "absent-personal-instructions"),
		})
		if err != nil {
			t.Fatal(err)
		}
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range sess.Events() {
			}
		}()
		t.Cleanup(func() { sess.Close(); <-drained })
		return sess
	}
	// newRecordingCase mirrors newCase for the cases whose oracle is the
	// typed event stream: the same fresh fixtures and live model, but the
	// events are captured instead of discarded.
	newRecordingCase := func(t *testing.T) (*Session, *skillsLiveRecorder) {
		t.Helper()
		root := t.TempDir()
		markGitRoot(t, root)
		writeSkillMD(t, root, "probe", "---\nname: pkg:probe\ndescription: fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_a983.\n")
		writeSkillMD(t, root, "second", "---\nname: pkg:second\ndescription: second fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_b742.\n")
		client := llm.NewClient(llm.WithRegistry(r))
		prof, err := provider.Resolve(client.Registry(), *skillsEvalModel)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(root), SessionConfig{
			AgentsDocPath: filepath.Join(root, "absent-personal-instructions"),
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := &skillsLiveRecorder{done: make(chan struct{})}
		rec.drain(sess.Events())
		t.Cleanup(func() { sess.Close(); <-rec.done })
		return sess, rec
	}
	if !t.Run("smoke", func(t *testing.T) {
		sess := newCase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // TRIPWIRE: live provider round-trip; measured smoke is ~3s, the bound only absorbs provider latency variance and fires on a genuine hang.
		defer cancel()
		output, err := sess.ProcessInput(ctx, "Return exactly SMOKE_37ab without using skills.", nil)
		if err != nil || !strings.Contains(output, "SMOKE_37ab") {
			t.Fatalf("smoke output=%q error=%v", output, err)
		}
	}) {
		t.Fatal("live smoke failed; behavior corpus was not run")
	}
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{"operative", "Use /pkg:probe to perform the fixture procedure.", []string{"pkg:probe"}},
		{"quotation", "Quote the literal token '/pkg:probe'; do not invoke it.", nil},
		{"fenced", "Explain this code as text only:\n```text\n/pkg:probe\n```", nil},
		{"path", "Describe these literals: /tmp/pkg:probe/file and https://example.invalid/pkg:probe .", nil},
		{"negation", "Do not use /pkg:probe. Return ACK_927.", nil},
		{"multiple", "Use /pkg:probe and /pkg:second to perform both fixture procedures.", []string{"pkg:probe", "pkg:second"}},
		{"near_miss", "Use /pkg:probex if that exact skill exists. Do not substitute another skill.", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newCase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // TRIPWIRE: live provider round-trip; measured text cases run 3–15s, the bound only absorbs provider latency variance and fires on a genuine hang.
			defer cancel()
			out, err := sess.ProcessInput(ctx, tc.input, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if state := sess.Meta().Skills; state != nil {
				for name, entry := range state.Inventory {
					if entry.Ordinary != nil {
						got = append(got, name)
					}
				}
			}
			slices.Sort(got)
			want := append([]string(nil), tc.want...)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("activations=%v want=%v", got, want)
			}
			t.Logf("output=%.160q activations=%v", out, got)
		})
	}

	// Structured inline: an ordinary-prose turn carrying an actual canonical
	// input selection. The prose contains no slash token at all, so any
	// activation must come from the typed selection, independent of text
	// position. The oracle is the typed selection/activation records.
	t.Run("structured_inline", func(t *testing.T) {
		sess, rec := newRecordingCase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // TRIPWIRE: live provider round-trip; measured ~4s, the bound only absorbs provider latency variance and fires on a genuine hang.
		defer cancel()
		prose := "Please perform the fixture procedure for the selected skill and report its result."
		if strings.Contains(prose, "/") {
			t.Fatalf("test bug: prose must not contain a slash invocation cue: %q", prose)
		}
		selection := queuedInput{ID: "live-inline-1", Text: prose, SkillNames: []string{"pkg:probe"}}
		out, err := sess.ProcessInput(sess.contextWithSelectedSkills(ctx, selection), prose, nil)
		if err != nil {
			t.Fatal(err)
		}
		inv := lifecycleInventory(sess)
		probe := inv["pkg:probe"].Ordinary
		if probe == nil {
			t.Fatalf("the selected skill recorded no ordinary activation (inventory=%v)", inv)
		}
		if probe.Route != "user_selection" {
			t.Fatalf("activation route = %q, want user_selection", probe.Route)
		}
		if !probe.UserAuthorized {
			t.Fatal("a user_selection activation must record explicit user authorization")
		}
		if second := inv["pkg:second"].Ordinary; second != nil {
			t.Fatalf("the unselected skill was activated too: %+v", *second)
		}
		var input *schema.SkillInputRecord
		for _, state := range skillTurnStates(sess) {
			if state.Input != nil {
				input = state.Input
				break
			}
		}
		if input == nil {
			t.Fatal("no typed SkillInputRecord recorded for the consumed selection")
		}
		if !slices.Equal(input.Names, []string{"pkg:probe"}) || input.OriginalText != prose || input.AtomicGroupID != "live-inline-1" {
			t.Fatalf("typed input record = %+v, want names [pkg:probe], the original prose, and group live-inline-1", *input)
		}
		if names := skillActivatedEventNames(rec.snapshot()); !slices.Equal(names, []string{"pkg:probe"}) {
			t.Fatalf("activation events = %v, want exactly [pkg:probe]", names)
		}
		t.Logf("typed records: route=user_selection authorized=true group=%s; marker in output (supplementary): %v",
			input.AtomicGroupID, strings.Contains(out, "LIVE_SKILL_a983"))
	})

	// Reload selection: two loaded skills, a real compaction whose reload
	// selection the MODEL makes, and a continuing task that needs only one of
	// them. The expected relevant set is declared before the model runs: the
	// remaining work is the probe procedure only, so the selection must name
	// exactly pkg:probe. Extra or missing names are reported, never silently
	// reinterpreted.
	t.Run("reload_selection", func(t *testing.T) {
		wantSelected := []string{"pkg:probe"}
		sess, rec := newRecordingCase(t)
		// Keep only the most recent turn verbatim so the activation carriers
		// age into the foldable prefix and the reload genuinely restores a
		// folded body (the same knob forced_note_live_test.go uses).
		sess.contextMgr.PreserveRecentTurns = 1
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // TRIPWIRE: multi-round live compaction case; measured 76s at worst, the bound only absorbs provider latency variance and fires on a genuine hang.
		defer cancel()
		out1, err := sess.ProcessInput(ctx, "Use /pkg:probe and /pkg:second to perform both fixture procedures.", nil)
		if err != nil {
			t.Fatal(err)
		}
		inv := lifecycleInventory(sess)
		if inv["pkg:probe"].Ordinary == nil || inv["pkg:second"].Ordinary == nil {
			t.Fatalf("test setup: both fixtures must be loaded before compaction (inventory=%v)", inv)
		}
		t.Logf("turn 1 loaded both fixtures; output=%.160q", out1)

		out2, err := sess.ProcessInput(ctx,
			"The work that used the second fixture procedure is finished and will not continue. "+
				"Call the compact_context tool now with a brief note_to_self. In its reload_skills array, "+
				"list exactly the skills the remaining work still needs.", nil)
		if err != nil {
			t.Fatal(err)
		}
		skillsLiveDiagnose(t, sess, rec, "turn 2")
		if !hasEventKind(rec.snapshot(), events.EventContextCompaction) {
			t.Fatal("no ContextCompaction event — no real compaction ran")
		}
		// A compaction's handoff receipt is transient: the turn's own
		// continuation round can consume it before ProcessInput returns, so
		// the durable oracle is the typed compaction_reload outcome set. When
		// the receipt is still outstanding, validate the model's explicit
		// selection from it before the continuing turn consumes it.
		var selNames []string
		if handoffs := pendingHandoffsSnapshot(sess); len(handoffs) > 0 {
			sel := handoffs[0].Operation.Selection
			if sel.State != "valid" {
				t.Fatalf("model reload selection state = %q (code %q, names %v), want valid", sel.State, sel.ErrorCode, sel.Names)
			}
			extra, missing := skillsLiveSelectionDiff(sel.Names, wantSelected)
			if len(extra) > 0 || len(missing) > 0 {
				t.Fatalf("model reload selection = %v, want exactly %v (extra=%v missing=%v)", sel.Names, wantSelected, extra, missing)
			}
			selNames = sel.Names
			t.Logf("turn 2: real compaction, model selected %v (receipt outstanding, phase %q); output=%.160q",
				sel.Names, handoffs[0].Phase, out2)
		} else {
			t.Logf("turn 2: real compaction; the handoff was already consumed within the turn; output=%.160q", out2)
		}

		out3, err := sess.ProcessInput(ctx, "Continue: perform the probe fixture procedure and report its result.", nil)
		if err != nil {
			t.Fatal(err)
		}
		skillsLiveDiagnose(t, sess, rec, "turn 3")
		reloads := skillsLiveOutcomesByRoute(sess, "compaction_reload")
		var probeReload *skillsLiveOutcome
		for i := range reloads {
			if reloads[i].Record.Identity.Name == "pkg:probe" {
				probeReload = &reloads[i]
			}
			if reloads[i].Record.Identity.Name == "pkg:second" {
				t.Fatalf("the unselected skill was reloaded too: %+v", reloads[i].Record)
			}
		}
		if probeReload == nil {
			t.Fatal("the selected skill recorded no typed compaction_reload outcome")
		}
		if len(selNames) == 0 {
			// The receipt was consumed before it could be read; the reload
			// outcome set is the durable record of the selection. It proves
			// the same contract: exactly the expected names were selected.
			var gotNames []string
			for _, r := range reloads {
				gotNames = append(gotNames, r.Record.Identity.Name)
			}
			extra, missing := skillsLiveSelectionDiff(gotNames, wantSelected)
			if len(extra) > 0 || len(missing) > 0 {
				t.Fatalf("model reload selection (from delivered outcomes) = %v, want exactly %v (extra=%v missing=%v)", gotNames, wantSelected, extra, missing)
			}
		}
		if probeReload.Record.Status != "pending" && probeReload.Record.Status != "already_present" {
			t.Fatalf("reload outcome status = %q, want pending (delivered) or already_present", probeReload.Record.Status)
		}
		if hs := pendingHandoffsSnapshot(sess); len(hs) != 0 {
			t.Fatalf("the reload handoff was not consumed: %+v", hs)
		}
		t.Logf("turn 3: reload outcome status=%q invocation=%s; activation events=%v; marker in output (supplementary): %v",
			probeReload.Record.Status, probeReload.Record.InvocationID,
			skillActivatedEventNames(rec.snapshot()), strings.Contains(out3, "LIVE_SKILL_a983"))
	})

	// Fallback reminder: a real compaction with an absent reload selection
	// records the complete typed inventory notification, and the next task —
	// which needs one previously loaded skill — must see the model invoke the
	// relevant reloadable skill itself (model_tool route).
	t.Run("fallback_reminder", func(t *testing.T) {
		wantReminderNames := []string{"pkg:probe", "pkg:second"}
		sess, rec := newRecordingCase(t)
		sess.contextMgr.PreserveRecentTurns = 1
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // TRIPWIRE: multi-round live compaction case; measured 47s at worst, the bound only absorbs provider latency variance and fires on a genuine hang.
		defer cancel()
		out1, err := sess.ProcessInput(ctx, "Use /pkg:probe and /pkg:second to perform both fixture procedures.", nil)
		if err != nil {
			t.Fatal(err)
		}
		inv := lifecycleInventory(sess)
		if inv["pkg:probe"].Ordinary == nil || inv["pkg:second"].Ordinary == nil {
			t.Fatalf("test setup: both fixtures must be loaded before compaction (inventory=%v)", inv)
		}
		t.Logf("turn 1 loaded both fixtures; output=%.160q", out1)

		out2, err := sess.ProcessInput(ctx,
			"Call the compact_context tool now with a brief note_to_self summarizing progress. "+
				"Do not pass any reload_skills value.", nil)
		if err != nil {
			t.Fatal(err)
		}
		skillsLiveDiagnose(t, sess, rec, "turn 2")
		if !hasEventKind(rec.snapshot(), events.EventContextCompaction) {
			t.Fatal("no ContextCompaction event — no real compaction ran")
		}
		if handoffs := pendingHandoffsSnapshot(sess); len(handoffs) > 0 {
			if state := handoffs[0].Operation.Selection.State; state != "absent" {
				t.Fatalf("selection state = %q (names %v), want absent — the setup asked for no selection", state, handoffs[0].Operation.Selection.Names)
			}
			t.Logf("turn 2: real compaction with absent selection (receipt outstanding, phase %q); output=%.160q", handoffs[0].Phase, out2)
		} else {
			t.Logf("turn 2: real compaction; the reminder handoff was already consumed within the turn; output=%.160q", out2)
		}

		out3, err := sess.ProcessInput(ctx, "New task: perform the probe fixture procedure and report its result.", nil)
		if err != nil {
			t.Fatal(err)
		}
		skillsLiveDiagnose(t, sess, rec, "turn 3")
		reminder := skillsLiveReloadReminder(sess)
		if reminder == nil {
			t.Fatal("the absent-selection compaction recorded no typed reload reminder")
		}
		if state := reminder.Selection.State; state != "absent" {
			t.Fatalf("reminder selection state = %q (names %v), want absent", state, reminder.Selection.Names)
		}
		extra, missing := skillsLiveSelectionDiff(availabilityNames(reminder.Inventory), wantReminderNames)
		if len(extra) > 0 || len(missing) > 0 {
			t.Fatalf("reminder inventory = %v, want exactly %v (extra=%v missing=%v)", availabilityNames(reminder.Inventory), wantReminderNames, extra, missing)
		}
		for _, entry := range reminder.Inventory {
			if entry.Availability != "reloadable" {
				t.Fatalf("reminder entry %q availability = %q, want reloadable", entry.Name, entry.Availability)
			}
		}
		// The model invoked the relevant reloadable skill itself: a typed
		// model_tool outcome for pkg:probe recorded in a turn state AFTER the
		// reminder turn. Turn 1's activations are model_tool outcomes too, so
		// position in the recorded history is what proves the re-invocation
		// happened once the compaction had dropped the body.
		var probeReinvoke *skillsLiveOutcome
		states := skillTurnStates(sess)
		reminderIdx := -1
		for i, state := range states {
			if state.ReloadReminder != nil {
				reminderIdx = i
				break
			}
		}
		if reminderIdx < 0 {
			t.Fatal("the reminder turn state is missing from the recorded history")
		}
		for i := reminderIdx + 1; i < len(states); i++ {
			for _, outcome := range states[i].Outcomes {
				if outcome.Activation != nil && outcome.Activation.Route == "model_tool" && outcome.Identity.Name == "pkg:probe" {
					probeReinvoke = &skillsLiveOutcome{Record: outcome}
				}
			}
		}
		if probeReinvoke == nil {
			t.Fatal("the model did not invoke the relevant reloadable skill (no typed model_tool outcome for pkg:probe)")
		}
		if hs := pendingHandoffsSnapshot(sess); len(hs) != 0 {
			t.Fatalf("the reminder handoff was not consumed: %+v", hs)
		}
		t.Logf("turn 3: reminder inventory=%v; model re-invocation invocation=%s status=%s; marker in output (supplementary): %v",
			availabilityNames(reminder.Inventory), probeReinvoke.Record.InvocationID, probeReinvoke.Record.Status,
			strings.Contains(out3, "LIVE_SKILL_a983"))
	})
}
