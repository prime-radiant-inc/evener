# Mobile Activity usability

Jesse authorized repair iterations from persona testing. Casey's 390×844 browser
journey established three problems: the first overview requires collapsing every
watch and job detail, Back from a child transcript loses the open Activity sheet,
and Activity is hidden in Session actions behind an unexplained count.

This is a bounded refinement of the existing sheet, chrome and row disclosures.
The existing `activityPanel` view store will retain sheet-open and row-detail
intent by session ref while its workspace pane remains retained. Explicit close
will clear the open intent. Shared activity reads, subscription leases and retry
ownership remain unchanged. Mobile details start collapsed; failed entries stay
visible outside the inactive fold through the shared row builder. Narrow chrome
gets a small Activity entry with an explicitly active count, using authoritative
job and delegate summary counts only when both are known.

## Work

1. Add real-component regressions for compact mobile details, visible failure,
   child transcript → Back restoration, explicit close, and a discoverable entry
   whose count remains unknown until authoritative summary evidence arrives.
   Observe the intended failures before implementation.
2. Change existing disclosure defaults and view-state ownership. Put failure
   grouping in `buildActivityRows`, retaining qualified identities, hierarchy and
   successful inactive disclosure behavior. Add no network or recovery owner.
3. Update the owning product guide and subsystem map. Run focused regressions,
   frontend formatting and the complete `make test-web` gate from the root.
4. Commit the verified changes without pushing. Root owns live 390×844 browser
   confirmation and the other persona repair lanes.

## Acceptance

- Opening mobile Activity shows compact rows, visible failed work, and the
  inactive fold. Every detail remains one disclosure away.
- Open a completed delegate from Activity after choosing disclosures, then Back:
  the same session's Activity sheet and choices reappear. Explicitly close it,
  navigate away and back: it remains closed. Another session inherits no state.
- Mobile session chrome opens Activity directly. Any number identifies active
  work, never infers a total from loaded rows or unknown counts, and continues to
  update through the existing shared summary binding.
- Tasks retain their completed/current/remaining behavior. Desktop navigation,
  shared API generation, activity recovery, and AppShell focus are outside this
  change.
