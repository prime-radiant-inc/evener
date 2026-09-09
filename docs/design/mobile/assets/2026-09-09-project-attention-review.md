# Project attention visual review

## Verdict

The supplied light and dark screenshots support the scoped project-attention
change. The signal is legible and secondary, the count matches the catalog
readback, zero-attention rows stay quiet, and the row actions remain visually
separate. This is a screenshot review of this small change only; it does not
qualify the whole app, iPad, accessibility work, or physical-device behavior.

## Observations

`catalog-readback.json` reports three projects with attention `0` and
`workspace` with attention `3`. Both screenshots show `workspace` with exactly
`3 need attention`, while the other rows show no attention text. The source
uses the supplied `rollup_attn` count directly (`ProjectSessionsList.tsx:311-317`)
and retains the existing session counts (`:374`).

In the light screenshot, the attention text is a clear blue accent below the
bold project name; in dark mode it remains readable against the dark surface
with a subdued blue tone. It reads as supporting status rather than competing
with the project name. The text is comfortably inside the row and has ample
space before the session count and trailing details control. No clipping,
overlap, forced expansion, or unexpected extra row is visible.

The project name and attention line are grouped in the existing toggle target;
the trailing three-dot details action remains distinct at the right edge. The
source preserves that separation and the 44pt details minimum
(`ProjectSessionsList.tsx:329-389`). The attention line is rendered inside the
toggle pressable, so its visual placement agrees with the intended tap behavior.

The UX contract calls for 19pt semibold project names, subordinate 13pt
metadata, sparse ordinary rows, and at least 44pt action regions
(`iphone-ux-contract.md:37-40,98-104`). The reviewed source uses 19pt for the
name and 13pt/19pt line-height for the signal (`ProjectSessionsList.tsx:352-371`)
while allowing the text column to grow and wrap (`:341-371`).

## Evidence and limits

Reviewed files:

- `/private/tmp/evener-project-attention-vzgj_qc_/projects-attention-light.png`
- `/private/tmp/evener-project-attention-vzgj_qc_/projects-attention-dark.png`
- `/private/tmp/evener-project-attention-vzgj_qc_/catalog-readback.json`
- `mobile-native/src/ProjectSessionsList.tsx`

No source or simulator files were changed. Screenshot evidence cannot establish
dynamic tap execution, VoiceOver output, extreme text-size layout, or physical
iPhone behavior; coordinator-owned source and runtime checks remain necessary
for those gates.
