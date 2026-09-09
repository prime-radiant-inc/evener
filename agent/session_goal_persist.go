package agent

import (
	"time"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
)

// goalPersistFromStore converts the store-native v2 persisted image to the
// schema wire form (spec §7): waits with full predicate payloads, pendingWake,
// budgets incl. maxParkedTotal, ledger-summary stub (slice-1 seeds migration
// entries; live folding arrives in slice 2), stage, terminalPending,
// lossCause, deadlineFinalDelivered. The persisted stop (terminal status +
// stopReason) doubles as the no-reemit marker — no separate reported flag is
// persisted.
func goalPersistFromStore(p goal.PersistedGoal) *schema.GoalSnapshot {
	out := &schema.GoalSnapshot{
		Objective:              p.Objective,
		Status:                 string(p.Status),
		Iterations:             p.Iterations,
		NoProgressStreak:       p.NoProgressStreak,
		MadeProgressOnce:       p.MadeProgressOnce,
		StopReason:             p.StopReason,
		CreatedAt:              p.CreatedAt,
		UpdatedAt:              p.UpdatedAt,
		AutoReparks:            p.AutoReparks,
		Conditions:             goalConditionsToSchema(p.Conditions),
		TerminalPending:        p.TerminalPending,
		LossCause:              p.LossCause,
		AdvancementSinceLoss:   p.AdvancementSinceLoss,
		DeadlineFinalDelivered: p.DeadlineFinalDelivered,
		Budgets: &schema.GoalBudgetsSnapshot{
			MaxContinuations:    p.Budgets.MaxContinuations,
			UsedContinuations:   p.Budgets.UsedContinuations,
			Deadline:            p.Budgets.Deadline,
			ParkedTotalNanos:    int64(p.Budgets.ParkedTotal),
			MaxParkedTotalNanos: int64(p.Budgets.MaxParkedTotal),
		},
	}
	for _, w := range p.Waits {
		out.Waits = append(out.Waits, schema.GoalWaitSnapshot{
			WaitID:         w.Lease.WaitID,
			Kind:           string(w.Lease.Kind),
			Target:         w.Lease.Predicate.Target,
			TimeoutNanos:   int64(w.Lease.Predicate.Timeout),
			Label:          w.Lease.Label,
			Matcher:        w.Lease.Predicate.Matcher,
			EventSubtype:   string(w.Lease.Predicate.EventSubtype),
			Baseline:       w.Lease.Predicate.Baseline,
			AskGeneration:  w.Lease.Predicate.AskGeneration,
			Deadline:       w.Lease.Deadline,
			RegisteredAt:   w.Lease.RegisteredAt,
			IdempotencyKey: w.Lease.IdempotencyKey,
			FiredEpoch:     w.Lease.FiredEpoch,
		})
	}
	for _, p := range p.PendingWake {
		out.PendingWake = append(out.PendingWake, schema.GoalPendingWakeSnapshot{
			WaitID:     p.WaitID,
			Trigger:    p.Trigger,
			FiredAt:    p.FiredAt,
			Superseded: p.Superseded,
			Kind:       string(p.Kind),
		})
	}
	if len(p.LedgerSummary.Entries) > 0 || p.LedgerSummary.Repetition != 0 || p.LedgerSummary.Tier != 0 || p.LedgerSummary.Stage != "" {
		summary := &schema.GoalLedgerSummarySnapshot{
			Repetition: p.LedgerSummary.Repetition,
			Tier:       p.LedgerSummary.Tier,
			Stage:      string(p.LedgerSummary.Stage),
		}
		for _, e := range p.LedgerSummary.Entries {
			summary.Entries = append(summary.Entries, schema.GoalLedgerEntrySnapshot{
				Fingerprint: e.Fingerprint,
				Class:       e.Class,
				Hash:        e.Hash,
				Digest:      e.Digest,
				Advancement: e.Advancement,
			})
		}
		out.LedgerSummary = summary
	}
	return out
}

// goalRestoreToStore converts a decoded schema snapshot to the store-native
// v2 image (spec §7). A nil Budgets marks a v1 snapshot and selects the
// section-7 backfill: absent budgets → defaults, usedContinuations ← old
// Iterations, deadline ← max(CreatedAt+4h, restore_time+1h), parkedTotal ← 0,
// autoReparks ← 0, pendingWake ← empty, deadlineFinalDelivered ← false,
// terminalPending ← false, stage seeded per the remaining-till-block table
// with K−remaining synthetic "migrated"-fingerprint entries (disclosed
// at-most-K−1 residual).
func goalRestoreToStore(g *schema.GoalSnapshot, restoreTime time.Time) goal.PersistedGoal {
	if g.Budgets == nil {
		return goal.MigrateV1ToPersisted(g.Objective, g.Status, g.StopReason, g.Iterations, g.NoProgressStreak, g.MadeProgressOnce, g.CreatedAt, g.UpdatedAt, restoreTime)
	}
	out := goal.PersistedGoal{
		Objective:        g.Objective,
		Status:           goal.Status(g.Status),
		Iterations:       g.Iterations,
		NoProgressStreak: g.NoProgressStreak,
		MadeProgressOnce: g.MadeProgressOnce,
		StopReason:       g.StopReason,
		CreatedAt:        g.CreatedAt,
		UpdatedAt:        g.UpdatedAt,
		Budgets: goal.Budgets{
			MaxContinuations:  g.Budgets.MaxContinuations,
			UsedContinuations: g.Budgets.UsedContinuations,
			Deadline:          g.Budgets.Deadline,
			ParkedTotal:       time.Duration(g.Budgets.ParkedTotalNanos),
			MaxParkedTotal:    time.Duration(g.Budgets.MaxParkedTotalNanos),
		},
		AutoReparks:            g.AutoReparks,
		Conditions:             goalConditionsFromSchema(g.Conditions),
		TerminalPending:        g.TerminalPending,
		LossCause:              g.LossCause,
		AdvancementSinceLoss:   g.AdvancementSinceLoss,
		DeadlineFinalDelivered: g.DeadlineFinalDelivered,
	}
	for _, w := range g.Waits {
		out.Waits = append(out.Waits, goal.Wait{Lease: goal.Lease{
			WaitID: w.WaitID,
			Kind:   goal.Kind(w.Kind),
			Predicate: goal.WaitKind{
				Kind:          goal.Kind(w.Kind),
				Target:        w.Target,
				Timeout:       time.Duration(w.TimeoutNanos),
				Label:         w.Label,
				Matcher:       w.Matcher,
				EventSubtype:  goal.EventSubtype(w.EventSubtype),
				Baseline:      w.Baseline,
				AskGeneration: w.AskGeneration,
			},
			Label:          w.Label,
			Deadline:       w.Deadline,
			RegisteredAt:   w.RegisteredAt,
			IdempotencyKey: w.IdempotencyKey,
			FiredEpoch:     w.FiredEpoch,
		}})
	}
	for _, p := range g.PendingWake {
		out.PendingWake = append(out.PendingWake, goal.PendingWake{
			WaitID:     p.WaitID,
			Trigger:    p.Trigger,
			FiredAt:    p.FiredAt,
			Superseded: p.Superseded,
			Kind:       goal.Kind(p.Kind),
		})
	}
	if g.LedgerSummary != nil {
		summary := goal.LedgerSummary{
			Repetition: g.LedgerSummary.Repetition,
			Tier:       g.LedgerSummary.Tier,
			Stage:      goal.GraduationStage(g.LedgerSummary.Stage),
		}
		for _, e := range g.LedgerSummary.Entries {
			summary.Entries = append(summary.Entries, goal.LedgerEntry{
				Fingerprint: e.Fingerprint,
				Class:       e.Class,
				Hash:        e.Hash,
				Digest:      e.Digest,
				Advancement: e.Advancement,
			})
		}
		out.LedgerSummary = summary
	}
	return out
}

// goalConditionsToSchema maps store conditions to the persisted wire form
// (full predicate payloads, like waits).
func goalConditionsToSchema(in []goal.Condition) []schema.GoalConditionSnapshot {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.GoalConditionSnapshot, 0, len(in))
	for _, c := range in {
		out = append(out, schema.GoalConditionSnapshot{
			Desc:          c.Desc,
			Kind:          string(c.Predicate.Kind),
			Target:        c.Predicate.Target,
			TimeoutNanos:  int64(c.Predicate.Timeout),
			Matcher:       c.Predicate.Matcher,
			EventSubtype:  string(c.Predicate.EventSubtype),
			AskGeneration: c.Predicate.AskGeneration,
			Baseline:      c.Baseline,
			Satisfied:     c.Satisfied,
			RegisteredAt:  c.RegisteredAt,
		})
	}
	return out
}

// goalConditionsFromSchema maps persisted conditions back to store form.
func goalConditionsFromSchema(in []schema.GoalConditionSnapshot) []goal.Condition {
	if len(in) == 0 {
		return nil
	}
	out := make([]goal.Condition, 0, len(in))
	for _, c := range in {
		out = append(out, goal.Condition{
			Desc: c.Desc,
			Predicate: goal.WaitKind{
				Kind:          goal.Kind(c.Kind),
				Target:        c.Target,
				Timeout:       time.Duration(c.TimeoutNanos),
				Matcher:       c.Matcher,
				EventSubtype:  goal.EventSubtype(c.EventSubtype),
				AskGeneration: c.AskGeneration,
			},
			Baseline:     c.Baseline,
			Satisfied:    c.Satisfied,
			RegisteredAt: c.RegisteredAt,
		})
	}
	return out
}
