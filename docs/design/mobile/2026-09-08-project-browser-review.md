# iPhone project browser correction

Jesse's first TestFlight feedback identifies three failures in the daily path:
sessions are not organized by project, scrolling does not load more, and opening
sessions feels slow. These are usability defects, not optional finishing work.

## Direction

The main Sessions screen is one scrolling list of expandable projects. Project
names establish the hierarchy; session titles sit underneath without repeating
the working directory on every row. The first project opens initially. Other
projects load their sessions when opened, and returning from a conversation
retains the loaded list and disclosure state. Related-session disclosure stays
separate from opening the conversation. Server omissions remain explicit.

Search remains available across the hub. Submitting a nonempty query enters the
search results; Clear restores the existing project browser. The aggregate search
contract still lacks a valid continuation, so its result limit remains visible.
Project browsing uses the existing paged navigation contract and automatically
loads the next page at a visible page boundary. Failed or invalidated pages stop
automatic requests and retain the visible rows until a deliberate retry or
refresh. This avoids repeated failed requests and rows moving under a finger.

The native header carries New session and a hub menu. Secondary hub settings,
project management, and pin navigation remain available without occupying
permanent rows above the content. Message actions use a quiet trailing control
and a native iOS action sheet; the message retains the available reading width.

Conversation return must reacquire the server subscription because child screens
can replace it. The displayed transcript stays visible during that read, while
mutations remain unavailable until the authoritative projection is accepted.
Previously loaded older history and its cursor must survive a same-instance
refresh. A different connection or replaced conversation cannot inherit that
history.

## Design authority and scope

This correction uses the existing [mobile philosophy](philosophy.md),
[style guide](style-guide.md), React Native components, system navigation, and
light/dark palette. Jesse requested the Impeccable design skill for the review.
It introduces no framework or dependency. iPad and dedicated accessibility work
remain paused. Simulator review does not establish physical-iPhone performance.

## Acceptance checks

- Multiple projects are legible in the main browser, with independent expansion.
- A 25-session project loads 20 rows and automatically appends the final five;
  row order and identities survive repeated scroll-end events.
- Failed continuation retains the loaded prefix; retry appends that page once.
- Opening a conversation and returning retains the list, expansion, and scroll.
- Search and Clear preserve the browser; changing hubs replaces its data owner.
- Opening and dismissing message actions does not mutate a session or draft.
- Returning from a child retains the transcript and older history while a fresh
  read reacquires the subscription; stale callbacks cannot enable mutations.
- Review the actual Release build in light and dark iPhone appearance, retain
  screenshots and source identity, and obtain an independent finish review.

The controller has passed a read-only check against the owned real hub: four
projects, a 20-to-25-row append with a retained prefix, and no duplicate requests.
The first Release simulator pass verified the 20-to-25 automatic append, exact
project-list position on conversation return, and preservation of expansion and
all loaded rows through search and Clear. Light and dark screenshots use the
actual iPhone 17 Pro simulator. The seeded pagination sessions mostly contain
only system records; they qualify browsing, not populated conversation loading.
The existing Ordinary response session supplies the populated reader check.

That reader check found a 183-point shift after returning from Fork preview.
Navigation removed each message action control and changed text wrapping while
the retained transcript stayed mounted. A transient refresh header also changed
scroll content geometry. Message actions now remain in place but disabled while
unfocused or refreshing; refresh feedback sits outside the scroll content.
Hidden-screen scroll events cannot overwrite the saved reading anchor. The
correction preserves semantic restoration for genuine transcript reflow.

The same batch hides Latest when no visible transcript rows exist and uses a
short project label in search, keeping the full directory in its accessible
context. All 703 native tests and TypeScript pass. Confirming native geometry
checks and the independent finish review remain pending at this checkpoint.
The installed TestFlight build has not yet been replaced by this work.
