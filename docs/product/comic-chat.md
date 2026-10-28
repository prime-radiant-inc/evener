# Comic Chat presentation

Choose **Comic** under **Presentation** in the conversation's **Verbosity** panel.
Comic renders the whole conversation pane, including its composer, questions and
approvals, within the existing workspace. Its units live in
`cmd/evener-hub/frontend/src/comic/`. The reader composes literal source text into
measured panels in one virtual scroll flow and remains usable if the font cannot
load. Comic and its plain reader share source identities and foreground demand.

## Selection, navigation and ownership

Standard and Comic use the same `/s/<encoded-ref>` navigation, pane and connection.
The complete AppWire ref is encoded as one URL component: `local:<session-id>` or
`<host>:<session-id>`. Child navigation uses its authoritative ref, never a display
name. Switching presentation does not navigate or open a second client. The
ordinary workspace owns authentication, navigation, notification leadership and
the `/rpc` connection. Reading a saved or remote conversation does not launch a daemon.

The browser remembers presentation separately for Desktop and Mobile, using the
existing 899px viewport boundary. Same-origin tabs share the choice, and reload
retains it. Both layouts default to Standard. This browser preference is separate
from shared content settings and hub defaults; native clients do not gain a Comic
setting. Standard's content editor remains mounted but inactive in Comic, retaining
custom and advanced values. **Use hub default** clears the content override only.
Storage failure leaves the current choice usable and reports uncertain persistence.

`ConversationBody` selects the renderer within the existing `ConversationSeat`.
The seat retains its composer source and identity-recovery owner across switches.
Each presentation retains its reading position; Comic keeps per-seat semantic
positions and uses session storage for reload. Positions belong to the logical
session, not its routing address. Clear starts the replacement session at its
latest root output, even when Clear happens while Standard is selected. A daemon
instance restart for the same session preserves the position.
The normal session menu remains
available, including a way back to Standard while the conversation is loading or
deleted. Session actions also remains available in both presentations while a
question is pending and message input is hidden. Inline child readers remain
noninteractive.

## Presentation and accessibility

At 760 CSS pixels and below, panels recompose into one column rather than
shrinking a desktop strip. Literal Markdown stays source text. Available thought
content is expanded in source order; missing or redacted content is not invented.
Routine tool text is hidden, while tool-produced images retain source attribution.
The surrounding interface has no desktop title bars, menu bars or beveled chrome.

On both phones and desktop, **Appearance** opens the View, Scene and expression
controls in a popup. History controls and Send/Stop stay outside the popup so
appearance settings do not crowd out the conversation when the keyboard opens.
Transcript scrolling keeps the popup open because its toolbar stays in place.
Escape, outside clicks, scrolling outside the reader and window resizing dismiss it.

Comic and plain views preserve semantic reading positions through reflow. The
root respects reduced motion and safe-area padding, and shared controls retain
their keyboard behavior. Pointer completion and text selection follow the pane's
document when it moves into a popout or docks back, even with Appearance closed.
Comic registers the shared transcript scroll actions for the focused seat,
including shortcuts dispatched outside its scroll port. Explicit movement and
positioning cancel that reader's automatic restoration and older-history demand;
independent readers keep their demand. The shared editable-target and mobile
shortcut rules remain unchanged.
Narrow-screen checks must reach the question choices,
note and final action, not merely capture their container. Browser viewport and
safe-area emulation do not establish native touch inertia or device behavior.

On mobile, the ordinary shell owns the visual-viewport keyboard inset and safe
area. Comic sizes to its conversation pane without adding another viewport-height
root or counting the keyboard inset twice. The composer clears the home indicator
when the keyboard closes, while the reader contracts when it opens.
Closing the keyboard restores reader space without resetting the draft or source
text. Pinch zoom is excluded. Switching presentation does not replace the shell's
viewport owner. Browser regression checks simulate the visual-viewport boundary,
not a physical Safari keyboard.

## Original art and offline checks

The selected 12 character poses, two backgrounds and six emotion icons are in `cmd/evener-hub/frontend/public/webassets/comic/`. The original art is from Microsoft's public [Comic Chat source release](https://github.com/microsoft/comic-chat/tree/48a162249484ab8d116c243e8203b0956d350c09), pinned to `48a162249484ab8d116c243e8203b0956d350c09`. [Art attribution](../../cmd/evener-hub/frontend/public/webassets/comic/ATTRIBUTION.md), the original [MIT notice](../../cmd/evener-hub/frontend/public/webassets/comic/LICENSE), the [selected asset manifest](../../cmd/evener-hub/frontend/public/webassets/comic/manifest.json) and [extraction guide](../../cmd/evener-hub/frontend/scripts/comic-assets/README.md) document origin, chosen AVB face/torso records and native-size PNG conversion. The four original Armando, Bolo, Denise and Tiki `.avb` files, all other referenced input files, source code and the historical archived EULA remain unchanged beneath `scripts/comic-assets/source/`. Their URLs, byte counts, Git SHA-1 blob IDs and SHA-256 values are frozen in `scripts/comic-assets/source-evidence.json` for offline reproduction.

Run `PYTHONDONTWRITEBYTECODE=1 python3 cmd/evener-hub/frontend/scripts/comic-assets/verify.py` from the repository root. Pillow and ImageMagick `convert` are required locally. The verifier checks independently pinned original and selected hashes, verifies the source MIT notice, checks background/icon pixels against an independent decoder, and checks AVB bitmap mask/ink operations on multiple backdrops. Three Bolo fringe pixels are nearest-alpha approximations because a standard RGBA PNG cannot encode the original background-dependent bitwise mask; original white/black output remains exact. The historical EULA is retained for provenance; the included artwork uses Microsoft's root MIT source release.

## Bundled Comic Sans

The client includes unmodified Comic Sans MS Regular 1.20 from Microsoft's
explicit MIT Comic Chat release at `f53e4b6e8e2bce7dbf3ddc7e8f12dd55591f7b17`.
The font and original MIT notice live in
`cmd/evener-hub/frontend/public/webassets/comic-fonts/`.
[Font attribution](../../cmd/evener-hub/frontend/public/webassets/comic-fonts/ATTRIBUTION.md)
records the exact source, hashes, removal history and older archived notices.
Only a regular face was present; the browser synthesizes bold from that face.
No installed or privately licensed font is needed or included.

`comic.module.css` serves `/webassets/comic-fonts/comic.ttf` with `@font-face`,
without `local()` fallback or a build-time download. `ComicConversationBody` waits for normal
and bold text readiness. If loading fails, it reports the failure while the
conversation and navigation remain usable. The asset regression checks the
actual font bytes and unchanged MIT notice independently of browser rendering.

The Comic root supplies its family through the existing `--font-sans` and
`--font-prose` tokens so shared controls and question prose use the same lettering.
AskDock's existing tab owns the narrow `--ask-tab-font` seam, with `revert` outside
Comic to retain the ordinary client's user-agent font. Only Comic root defines
that token; there is no descendant override, new widget or widget prop.
Actual glyph inspection, not computed family names, checks questions, tabs,
approval actions and Send. The browser guard also records regular-client fonts
and geometry to detect theme leakage. The default guard loads the bundled font;
`EVENER_COMIC_BLOCK_FONTS=1` exercises blocked-font fallback and usable controls.

## Shared input and recovery

The conversation pane uses its existing composer source, structured editor, attachments,
queue and `useComposerActions` owner. Send, Queue and Stop keep ordinary-client
admission and durable mutation identities. A transport failure does not mean an
accepted input should be sent again: canonical transcript reconciliation can
settle an unacknowledged mutation. Newer edits remain separately owned through
reload, navigation and switches between Standard and Comic.
Stop remains available beside Send or Queue while a draft is present. Interrupting
the run preserves that draft; the user need not clear it or switch presentation.

Palette insertions, quoted text and composer focus target the addressed pane,
including when two panes show the same conversation. Comic consumes each insertion
once through the shared source, preserving existing skill and command identities
and placing the caret at the insertion boundary. Switching to Standard does not
replay a consumed insertion.

Both presentations use `useBuiltinSubmission` for typed and palette-inserted
session commands. Known built-ins execute instead of sending literal chat;
unknown and plugin commands, and drafts carrying attachments or selected skills
or commands, keep ordinary message routing. Queue and Steer submit their parsed
body through the shared durable input owner. Busy and live recovery fences apply
before execution, and success clears only the unchanged draft. Failure and newer
edits preserve the user's work.

An explicit Send-driven resume owns dispatch for its source and resolved
destination until identity and outbox reconciliation finish. Reconnect reads
cannot release that ownership, even when they permit ordinary folded Send.
Completion wakes eligible pending mutations with their original mutation IDs;
Stop cancellation and failed-resume recovery keep their existing owners.

Pending root questions use the existing AskDock and suppress the ordinary message
surface. Stop remains available while the run is active, including when other
tools continue alongside a pending question. Stopping preserves the hidden draft.
Answering the last question restores focus only to the answering pane, even when
another pane shows the same conversation with its own draft.
The shared question owner retains known pending questions, answers,
notes and the active tab when background history drops cached bodies, including
during responsive pane remounts. Retention requires the same canonical call and
pending state. Resolution, replacement or removal clears the question through
the ordinary reconciliation rules; omitted bodies do not create new questions.
Sandbox requests use the existing approval card; neither arriving at a request
nor reconnecting approves it. A late response belongs to the original root, not
the conversation now on screen. Inline children remain read-only.
Comic does not add upload, voice, session creation, configuration, forks or general
job/watch controls; workspace navigation and existing menus retain those actions.
Existing structured
drafts and recovery remain shared, without new attachment-lifetime guarantees.

## Inline child readers

`ComicConversationBody` owns the root reader. `useComicSubchats` mounts one committed
lifetime per explicitly expanded, qualified child path above the virtual rows.
Scrolling a heading out of view does not release its read demand. Collapsing a
branch releases that branch and its descendants, not an independently opened
reader of the same child. Neither expansion nor paging starts or resumes a
session or writes to the parent's shared composer draft.

Clear can replace the logical session while its workspace address stays the
same. That replacement retires the old session's inline descendants and their
read demand, preserving independent readers. Activity refreshes and a new daemon
instance for the same logical session preserve expansion.

`useComicActivity` observes only session-scoped delegates through the shared
activity owner. `buildEntityView` chooses generation and revision precedence;
`projectComicDelegates` qualifies owner, delegate ID and authoritative child ref.
A canonical loaded delegation event supplies placement, not current status.
Without one, the existing strip stays in a separate group. Loading its event
moves the strip without changing its key or committed child lifetime. Ambiguous
identity remains readable as attributed source, without an invented child route.

Routing keeps the workspace address; delegate membership and personas use the
resolved session's source-qualified identity. Activity and transcript reads can
settle separately during Clear, so Comic combines them only when their logical
session identities agree.

Activity headings use proven `SessionActivityContext.delegateName`. Transcript
panels instead consume the shared SDK's typed `ItemModel.authorship`, never the
containing child's identity or a parent-context guess. Human input stays Anna,
including inherited human input. Agent instructions are source captions, while
known agent answers and thoughts keep their original persona. Delegate identity
uses the complete owner-ref/delegate-ID tuple; other agents use their qualified
session ref. Missing names retain the delegate-ID label. Titles, task descriptions
and thread names are not substitutes. This adds no parent read or identity cache.

Proven copied context carries an `inherited` label in both comic and plain views.
Missing, unknown or invalid authors remain neutral source captions; unknown
reasoning remains labeled Thought rather than becoming the containing agent's
thought. Explicit user steering remains human when no known author supersedes it.
Human whiteboard notes remain source captions even when their human authorship is
known or inherited.
These presentation choices leave literal source text, item IDs, source positions,
ordering, paging and root-only interaction admission unchanged. Durable production,
fork preservation and SDK normalization belong to the shared
[original transcript authors](history-content.md#original-transcript-authors) path.

`composeComicFlow` interleaves each reader's panels, headings and bounded paging
controls. Both presentations use one `VirtualList`; grandchildren have no nested
scrollbox or compounded mobile indentation. Each reader's itemless turn errors
remain separate from chronology and unplaced delegates. Root history controls
remain available above the virtual flow, while child history controls belong to
their reader's rows.

An expanded child shows a bounded loading, repair, disconnected or unavailable
row using the shared connection and thread signals. Cached content remains
readable. Recoverable states keep the shared owner's automatic retry; Comic adds
no timer, retry loop or cache. A deleted child remains explicitly openable, while
its parent and healthy siblings stay usable.

Saved positions contain the complete target session and expansion path, source
entry and character offset, never transcript bodies. Restoration demands only the
saved branch through the existing activity and history pagers. In-flight history
loads preserve the saved source and child path until loading finishes. A newly
loaded source waits for composed layout before any missing-source fallback. User
movement cancels pending restoration. A collapsed branch keeps its small anchor
for reopening; a parent end target does not follow later child panels.
The shared transcript hook captures the older-page scheduling key with its
loading state. The shared history owner coalesces demand and paces retries;
Comic requests continuation while the saved source is missing and that owner is
idle. Hydration may replace a cached window and later return to its cursor, so
an earlier request for that cursor does not prove its page was fetched.
If a saved child never hydrates and the shared owner confirms deletion,
restoration settles at its parent strip. Temporary loading or repair preserves
the saved intent until recovery or user movement. **Jump to latest** releases
every inline reader's pending history demand, not an independent reader's demand
for the same child.

The explicit **Open conversation** action uses the child's ordinary workspace route.
Inline readers contain no composer or approval submission. Send, Stop and approval
controls belong to the interactive conversation pane, not inline expansion. Human
questions and sandbox approvals remain interactive-root-only. Child activity
status is not a human question or approval, and opening a child does not change
those admission rules.
