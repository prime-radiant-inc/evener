# Keep narrow Activity entry without compressing session facts

The direct Activity action currently occupies the SessionChrome control row and
compresses real status facts to 0px at 320px and 47px at 390px. Existing browser
red evidence is `ux-web-browser-c645.log` in the integration ledger.

Move this narrow action beside the composer's To: recipient line. Keep the
recipient shrinkable with ellipsis and its full accessible description/title;
the action keeps its intrinsic visible label and existing 44px button target.
Desktop keeps the existing menu/sidebar entry. Remove the narrow chrome button
without hiding any status, model, or verb facts.

A small action component uses the existing shared session summary binding and
activityPanelStore.setSheetOpen action. SessionChrome's one hidden ActivityPanel
remains the sole sheet owner. Unknown agents/jobs counts show Activity; established
counts show their truthful active sum as before. No new fetch, retry or imperative
panel owner. The shared binding coalesces the recipient action's summary demand
with the existing chrome/panel demand.

Add red Composer behavior tests for action placement/opening the existing same
session sheet and unknown/known counts. Extend the actual-component overflow
guard to qualify Activity, recipient, status/model geometry at 320/390 across
themes and font sizes. Preserve the existing clipping guard and all touch-target
requirements. Do not alter the unrelated layoutguard watch fixture.

Run focused composer/chrome tests, overflowguard at 320/390, frontend typecheck
and Biome; commit with normal hooks and record evidence/qualification limits.
