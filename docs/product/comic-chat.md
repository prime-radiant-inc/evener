# Comic Chat personal client

`/comic` is an additional Evener web client; it does not replace the existing hub. Its comic-panel units live in `cmd/evener-hub/frontend/src/comic/`. The reader composes literal source text into measured panels in one virtual scroll flow. The picker and reader remain usable without the Comic Sans files. Comic and plain presentation share the same source identities and foreground reader.

## Original art and offline checks

The selected 12 character poses, two backgrounds and six emotion icons are in `cmd/evener-hub/frontend/public/webassets/comic/`. The original art is from Microsoft's public [Comic Chat source release](https://github.com/microsoft/comic-chat/tree/48a162249484ab8d116c243e8203b0956d350c09), pinned to `48a162249484ab8d116c243e8203b0956d350c09`. [Art attribution](../../cmd/evener-hub/frontend/public/webassets/comic/ATTRIBUTION.md), the original [MIT notice](../../cmd/evener-hub/frontend/public/webassets/comic/LICENSE), the [selected asset manifest](../../cmd/evener-hub/frontend/public/webassets/comic/manifest.json) and [extraction guide](../../cmd/evener-hub/frontend/scripts/comic-assets/README.md) document origin, chosen AVB face/torso records and native-size PNG conversion. The four original Armando, Bolo, Denise and Tiki `.avb` files, all other referenced input files, source code and the historical archived EULA remain unchanged beneath `scripts/comic-assets/source/`. Their URLs, byte counts, Git SHA-1 blob IDs and SHA-256 values are frozen in `scripts/comic-assets/source-evidence.json` for offline reproduction.

Run `PYTHONDONTWRITEBYTECODE=1 python3 cmd/evener-hub/frontend/scripts/comic-assets/verify.py` from the repository root. Pillow and ImageMagick `convert` are required locally. The verifier checks independently pinned original and selected hashes, verifies the source MIT notice, checks background/icon pixels against an independent decoder, and checks AVB bitmap mask/ink operations on multiple backdrops. Three Bolo fringe pixels are nearest-alpha approximations because a standard RGBA PNG cannot encode the original background-dependent bitwise mask; original white/black output remains exact. The historical EULA differs from Microsoft's current root MIT release; approval for future distribution has not been determined.

## Personal-only Comic Sans setup

The personal preview may use **two already-authorized local** font files, genuine Comic Sans MS Regular/Bold 2.10. They are not part of the official Comic Chat art and **must not be committed, packaged for release, or deployed in font-bearing builds**. The existing personal font source lives outside the public web tree at `.superpowers/brainstorm/3112257-1792887793/font-source/`; see its README and EULA before local use. The regular file `Comic.TTF` has SHA-256 `b82c53776058f291382ff7e008d4675839d2dc21eb295c66391f6fb0655d8fc0`; bold `Comicbd.TTF` has `873361465d994994762d0b9845c99fc7baa2a600442ea8db713a7dd19f8b0172`.

For an authorized **local personal build only**, copy the two files manually to `cmd/evener-hub/frontend/public/webassets/comic-local/Comic.TTF` and `Comicbd.TTF`. The entire directory is gitignored. `comic.module.css` defines only `/webassets/comic-local/Comic.TTF` at weight 400 and `/webassets/comic-local/Comicbd.TTF` at weight 700, without `local()` fallback or download logic. `ComicApp` asks `document.fonts` to load both faces and announces an accessible setup status when either fails while navigation controls continue working. CI deliberately runs this usable missing-font state. Never publish the font-bearing `dist` or ship a build with these private files. Do not treat the unrelated Comic Neue preview font as a licensed substitute or commit it.

## Inline child readers

`ComicSession` owns the root reader. `useComicSubchats` mounts one committed
lifetime per explicitly expanded, qualified child path above the virtual rows.
Scrolling a heading out of view does not release its read demand. Collapsing a
branch releases that branch and its descendants, not an independently opened
reader of the same child. Neither expansion nor paging starts or resumes a
session or writes to the parent's shared composer draft.

`useComicActivity` observes only session-scoped delegates through the shared
activity owner. `buildEntityView` chooses generation and revision precedence;
`projectComicDelegates` qualifies owner, delegate ID and authoritative child ref.
A canonical loaded delegation event supplies placement, not current status.
Without one, the existing strip stays in a separate group. Loading its event
moves the strip without changing its key or committed child lifetime. Ambiguous
identity remains readable as attributed source, without an invented child route.

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
saved branch through the existing activity and history pagers. A newly loaded
source waits for composed layout before any missing-source fallback. User
movement cancels pending restoration. A collapsed branch keeps its small anchor
for reopening; a parent end target does not follow later child panels.
If a saved child never hydrates and the shared owner confirms deletion,
restoration settles at its parent strip. Temporary loading or repair preserves
the saved intent until recovery or user movement. **Jump to latest** releases
every inline reader's pending history demand, not an independent reader's demand
for the same child.

The explicit **Open conversation** action selects the child's full-page route.
Inline readers contain no composer or approval submission. Send, Stop and approval
controls are separate work, not features delivered by inline expansion. Human
questions and sandbox approvals remain interactive-root-only. Child activity
status is not a human question or approval, and opening a child does not change
those admission rules.
