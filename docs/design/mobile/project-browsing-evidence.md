# Native project browsing

Verified 5 September 2026. Sessions → Browse projects opens a paged project catalog. A project opens Current, Recent and Archived session tiers. The separate archived-project catalog is accessible too. All routes carry the selected hub identity and use server project keys and session refs.

This uses evener/navigation/read catalog and project_page resources. It does not add aggregate thread/list cursors. The existing navigation service supplies offsets, remaining counts and a revision shared by pages of each logical resource. The client advances by raw response length, deduplicates visible identities, serializes continuation, rejects changed generations/revisions and ignores cancelled requests. Returning from a session preserves loaded pages; pull-to-refresh explicitly replaces them. Selected tabs expose accessibility selection and remain readable at large text sizes.

## Evidence

- Native tests: 72 across 14 files, TypeScript and targeted Biome pass. Both iOS and Android Release builds succeeded and were installed. Shared code was not changed in this increment.
- Network-boundary tests cover raw offsets despite duplicates, duplicate Load more dispatch, revision mismatch and refresh recovery, cancelled completion, and malformed empty pages with positive remaining counts.
- The real isolated hub returned one project with four current sessions. Four wire reads with limit one returned offsets 0–3, remaining 3–0, and project revision 59 throughout.
- iOS manually opened the project and a conversation, returned to the project, and exercised empty Recent/Archived session tiers and the empty archived-project catalog. Live accessibility-large font changes produced readable rows and wrapping tabs; normal text size was restored.
- Android opened the same real project and conversation. A temporary WebSocket proxy reduced only navigation request limits to two, forwarding actual hub responses unchanged. The native Load more action issued offset 2 and displayed all four unique sessions. Returning from the conversation retained the list. Pull-to-refresh then issued offset 0, confirming the reviewed refresh correction through the real network path.
- Review found the explicit refresh incorrectly shared the initial-load guard; the final code permits connected pull-to-refresh after a successful load.

![iOS project sessions](assets/project-ios.png)
![Android after loading the next page](assets/project-android.png)
![iOS large text](assets/project-ios-large.png)

## Open acceptance

This is not full navigation completion. Pins, favorites/archive mutations, nested session disclosure, server truncation disclosure, live invalidation notifications and project-route restoration after process death remain open. Multi-page catalog and archived nonempty datasets, native revision-conflict recovery, screen readers and physical devices still need dedicated acceptance. The top-level thread/list roster retains its documented cap; project paging is the available path to older sessions. No full repository merge gate or release-readiness claim is made.
