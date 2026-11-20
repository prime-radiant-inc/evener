package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// thread/list copies a descendant row from its stored projection and then
// samples the live tree. The sample lists only descendants waiting now, so a
// copied wait must leave the row on this refresh, while an ordinary terminal
// failure carries no wait and must survive untouched.
func TestAttachDescendantQuotaWaitsFollowsTheLiveSample(t *testing.T) {
	t.Parallel()
	const id = "quota-wait-child"

	t.Run("the live sample's absence", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			stored    *appwire.ThreadFailure
			wantTitle string // empty: the failure must be cleared
		}{
			{
				name:   "clears a copied wait",
				stored: &appwire.ThreadFailure{Title: "waiting out the usage limit", ResumesAt: 1_900_000_000_000},
			},
			{
				name:      "keeps an ordinary failure",
				stored:    &appwire.ThreadFailure{Title: "the turn failed"},
				wantTitle: "the turn failed",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv := &Server{}
				srv.SetDescendantQuotaWaitsFunc(func([]string) map[string]*appwire.ThreadFailure { return nil })
				rows := []appwire.Thread{{
					ID:     id,
					Evener: appwire.EvenerThread{Profile: "gpt-5.2", Failure: tc.stored},
				}}

				srv.attachDescendantQuotaWaits(rows)

				failure := rows[0].Evener.Failure
				if tc.wantTitle == "" {
					if failure != nil {
						t.Fatalf("the row still carries the copied wait %+v after the live sample reported none", failure)
					}
				} else if failure == nil {
					t.Fatal("the live sample's absence cleared an ordinary terminal failure that carries no wait")
				} else if failure.Title != tc.wantTitle || failure.ResumesAt != tc.stored.ResumesAt {
					t.Fatalf("ordinary failure = %+v, want the stored summary unchanged", failure)
				}
				if got := rows[0].Evener.Profile; got != "gpt-5.2" {
					t.Fatalf("the row's profile = %q after the replacement, want the rest of the row untouched", got)
				}
			})
		}
	})

	t.Run("the live sample replaces the copied wait and is not aliased", func(t *testing.T) {
		live := &appwire.ThreadFailure{
			Title:         "waiting out the usage limit",
			ResumesAt:     1_900_000_000_500,
			WaitStartedAt: 9,
		}
		srv := &Server{}
		srv.SetDescendantQuotaWaitsFunc(func([]string) map[string]*appwire.ThreadFailure {
			return map[string]*appwire.ThreadFailure{id: live}
		})
		rows := []appwire.Thread{{
			ID: id,
			Evener: appwire.EvenerThread{Failure: &appwire.ThreadFailure{
				Title:     "stale copy",
				ResumesAt: 1_900_000_000_000,
			}},
		}}

		srv.attachDescendantQuotaWaits(rows)

		got := rows[0].Evener.Failure
		if got == nil {
			t.Fatal("the live sample's wait was not attached to its row")
		}
		if got.Title != live.Title || got.ResumesAt != live.ResumesAt || got.WaitStartedAt != live.WaitStartedAt {
			t.Fatalf("row failure = %+v, want the live sample %+v", got, live)
		}
		if got == live {
			t.Fatal("the row aliases the live sample's summary: a caller mutating the row would reach the session's state")
		}
		got.Title = "mutated"
		if live.Title != "waiting out the usage limit" {
			t.Fatalf("mutating the row's summary reached the live sample: %+v", live)
		}
	})
}
