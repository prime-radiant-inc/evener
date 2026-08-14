## Contract validation

At the first evidence that two interpretations could produce different final
artifacts, ensure the three task roles below are active before reading the
evidence that selects among them. If no affected chain exists, create it. If
later evidence may invalidate an existing completed chain, do not append
replacement tasks: in one `task_list` update, set the existing DECIDE task to
`in_progress` and its affected BUILD and VERIFY tasks to `open` before reading
the selecting evidence or doing further artifact work.

When ambiguity can change the final artifact, use exactly three separate tasks
and keep these roles separate:

1. A completed DECIDE task has four true claims in its notes: CANDIDATES names
   the live interpretations; SELECTED INTERPRETATION names one actual
   candidate; PRIMARY EVIDENCE identifies current evidence that distinguishes
   it; REJECTED ALTERNATIVES disposes of every other candidate using that
   evidence. Words such as “unresolved,” “deferred,” “unknown,” or a plan for a
   future implementer are not a selected interpretation. Recording the four
   labels is not completion.
2. BUILD depends on DECIDE. It starts only after DECIDE satisfies all four
   claims.
3. VERIFY depends on BUILD. It independently re-derives the expected result
   from the original request and primary evidence across every materially
   plausible candidate, including candidates DECIDE rejected. If contrary
   evidence appears during verification, apply the in-place reopening rule
   above before further work or finalization.

If current primary evidence does not select a candidate, the task state is
DECIDE `in_progress`, BUILD `open`, and VERIFY `open`. This state is also the
required outcome of a planning-only exercise; do not close or cancel tasks
merely to make the task list terminal.

Evidence must distinguish the candidate scopes, not merely accompany a choice.
A rejected candidate is supported only by evidence whose truth makes that
candidate incompatible with the original request. Separate sibling storage
fields do not by themselves prove that one field is outside a broader
conceptual label, and the request's omission of words such as “plus” or
“combined” does not by itself prove exclusion.

When a conceptual label could denote multiple sibling fields, inspect how the
canonical downstream or consumer-facing representation composes or excludes
those fields before selecting the inclusion boundary. The canonical
construction can support a combined or a narrow scope; do not prefer an answer
because it is broader, larger, or includes more fields.

VERIFY must reconstruct every materially plausible candidate implied by the
original request, including candidates DECIDE marked rejected, and
independently audit the evidence used to reject each one. DECIDE's rejected set
is not a premise VERIFY may inherit. If a rejection rests only on storage
shape, missing inclusion words, or other evidence that does not distinguish
the scopes, reopen DECIDE, BUILD, and VERIFY before further artifact work or
finalization.
