# Native project and session organization

Verified 5 September 2026. Project and session rows expose a quiet More action with the item title and hub in the platform alert. Projects support Add to pinned/Remove from pinned and Archive/Unarchive. Top-level sessions support Archive/Unarchive; nested subagent, fork and cluster rows do not expose organization. The synthetic no-project entry is excluded. Session pin sections remain a separate unfinished workflow.

Archive uses the server session_id, while opening uses the canonical ref. Projects send their key and working directory. No history is deleted and no runtime stop is requested. Successful writes require a navigation read satisfying the receipt generation and relevant revision. A read invalidated by a racing notification gets one further read, never a repeated write. Cancellation or a superseding read cannot falsely acknowledge verification. Owner tokens invalidate old native alert callbacks after readiness/focus changes. Errors remain visible without automatic mutation replay.

## Evidence

- Native 87 tests across 16 files, TypeScript and targeted Biome pass. Both final iOS and Android Release builds were installed.
- Boundary tests cover exact project parameters, single pending mutation, disposal/stale confirmation, uncertain failure, receipt revision rejection, superseded verification and a notification racing the receipt read.
- Independent review found and closed superseded-read acknowledgement and reconnect/focus ownership defects.
- Both simulators archived the real isolated-hub project, found it under Archived projects, and restored it. Both archived the owned Session controls verified session, found it in the Archived tier, and restored it. iOS pinned the project; Android removed its pin. The final owned fixture state is unpinned and unarchived.
- The wire observer captured actual hub receipts and navigation pages. Android session archive revision 74 excluded the session from Current and included it in Archived; revision 75 returned an empty Archived tier after restoration. This used the page-size proxy forwarding actual hub data, with no synthetic hierarchy.
- Live testing exposed receipt-before-notification timing: the first verification was invalidated despite a current server response. The added read-only revalidation regression reproduces that order; both-platform manual checks succeeded after correction.
- Final geometry correction gives Action a platform minimum width as well as height. Android More bounds measured 126 by 126 physical pixels at the emulator's 2.625 density, or 48 dp square. Both final screenshots were visually inspected. Mutation checks precede only this minimum-width adjustment; final Android More opening/cancel was repeated afterward.

![iOS organization row](assets/organization-ios.png)
![Android organization row](assets/organization-android.png)

## Remaining acceptance

This does not complete navigation organization: session pin sections, removal, hub-wide pinned browsing and physical-device/accessibility acceptance remain open. Native acknowledgement-loss and stale-alert fault injection still need dedicated E2E beyond the deterministic controller checks. No full repository merge gate or release-readiness claim is made.
