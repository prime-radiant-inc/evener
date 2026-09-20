A panic while draining steering input could leave the carrier claim marker set. A later unrelated selection failure could then be misclassified as a carrier failure and resolve a pending question during restore. The marker is now cleared by defer on every exit.

Follows merged #1958, #1962, and #1977. Refreshed head `00849a796e8ec03b76426fca443e132dbbd62dd2` is based on main `810a52f3232586c525580f431e390d7b03be29ba`. Own scope remains five production additions, two deletions, and 54 test additions. The complete owned patch matches the previously reviewed patch after removing only diff blob IDs and hunk coordinates (SHA-256 `0582be763e72889fb4701a2e05e6d5da67f1b2550cae7c322be03ccbe2f491bd`). Duplicate declaration and whitespace checks passed.

Validation: the regression panics through the real append path and verifies marker cleanup. Focused normal/tagged/race carrier and live/restore tests, normal/tagged/Windows vet, agent lint, and formatting passed for the reviewed patch. Independent correctness/simplify review and local RoboRev2613 accepted it; the three raw member bodies at `65933f7cf` were read. The byte-identical refresh carries that review under the handoff rule; all 15 fresh current-head CI checks passed.

Disposition of that panel's three Medium bullets:
- Direct user-input admission: fixed by merged #1977, now in this base.
- Kindless, provenance-less live human-note classification: the suggested construct is outside the supported live admission path, which writes explicit kind/provenance. Legacy restore coverage remains tracked in #1946.
- Interrupt-marker persistence: real inherited bug tracked in #1181. Jesse explicitly directed merging this carrier cleanup first and fixing interrupt persistence in the immediate follow-up. This PR does not claim that fix.

Optional carrier-test coverage and other Lows remain separate in #1946.
