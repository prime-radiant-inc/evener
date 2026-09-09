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
Native visual and interaction acceptance is still pending at this checkpoint.
The installed TestFlight build has not yet been replaced by this work.
