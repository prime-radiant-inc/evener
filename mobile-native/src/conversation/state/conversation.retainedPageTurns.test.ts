// ConversationStore (Zustand) tests with a fake ConversationService.
// Covers generation safety, notification routing, draft preservation,
// conflict restore, and no auto-retry.

import type { Turn } from "@evener/appwire-client";
import { hydrateThread, markItemTextOmitted, sessionTokens } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { createConversationStore } from "./conversation";
import {
	FakeConversationService,
	historyUpdated,
	makeConversation,
	makeThread,
	makeTurn,
	turnsPage,
	wireTurnFragment,
} from "./conversationTestUtils";

// The #1919 keep-window bulk: a settled turn of plain user rows, scripted
// FIRST in every read so the retained window is genuinely full when a page
// row has to fall outside it. Its positions sit above every scripted entry,
// so a page fragment weaves in below it and the cap evicts the page's own
// rows — the honest eviction path the bounded-payload fixtures assert.
// Zero usage, and every scripted read re-serves the turn by id, so it never
// claims cursor coverage and never moves a sessionTokens sum.
function fillerTurn(count: number) {
	return {
		id: "filler",
		status: "completed" as const,
		usage: { inputTokens: 0, outputTokens: 0 },
		items: Array.from({ length: count }, (_, j) => ({
			type: "userMessage" as const,
			id: `fill-${j}`,
			turnId: "filler",
			text: `fill-${j}`,
			position: { entry: 100 + j, item: 0 },
			status: "completed" as const,
		})),
	};
}

describe("ConversationStore", () => {
	// D18 B3 round 3: sessionTokens (the shared session-usage derivation) reads
	// conversation.turns and conversation.olderCursor, but loadOlder only ever
	// updated conversation.items and the store's OWN olderCursor field. A
	// session with no thread-level cumulative usage therefore kept summing
	// just the first page forever, even after older turns loaded.
	// Task 17 (v6 read path): this block used to carry a second family of
	// tests, alongside the retained-cap ones below, pinning the pre-v6 array
	// merge's own cross-turn coalescing — a compact (memory-only) turn
	// "bridging" a later re-issue of its content under a BRAND NEW turn id, an
	// "alias" chain of bare ids the merge stitched back into one item through
	// remembered call-ids, a "skeleton" injected into the merge's input so its
	// fragment-matching folded correctly. That matching existed because the
	// OLD merge (mergeTurnHistoryWithContext) coalesced fragments across
	// DIFFERENT turn ids by scanning item/call-id overlap — the only way it
	// could recognize that a turn the client trimmed for memory had reappeared
	// under a wire-assigned id it could not predict.
	//
	// The versioned merge (mergeHistory, mergeOlderItemPage, applyReadModel)
	// has no such scan: turns key strictly by id, items within a turn key by
	// transcriptKey (falling back to id only when a side carries none) — a
	// real v6 item's transcriptKey is stable for the item's whole life (it
	// names the item's position in the transcript), so it never needs
	// rediscovering under a new turn id the way a pre-v6 bare id did. A
	// compacted turn's later reappearance is just its OWN turn id showing up
	// again with real items where boundRetainedTurns had trimmed them to
	// none — mergeHistory's ordinary "index === -1, splice it in" path, no
	// memory of the trim required. The one thing the versioned merge does not
	// do on its own — an item's transcriptKey re-served under a DIFFERENT
	// turn id than before, e.g. a page and a fresh read splitting the same
	// turn differently at their boundary — reconcileCrossTurnReissues folds,
	// the same helper the live-frame path (applyThreadNotification) uses; see
	// reducer.history.test.ts's "an older page merges by version and never
	// replaces newer held items" and this file's loadOlder/rehydrate describe
	// blocks for that coverage.
	//
	// The removed tests' literal scenarios (turns coalescing across ids by
	// remembered alias, skeleton placeholders surviving or not surviving a
	// strip pass, tool-fold call-id precedence) named internal shapes of code
	// this migration deletes (injectCompactedSkeletons, stripInjectedSkeletons,
	// compactedTurnsCollidingWith, mergeTurnHistoryWithContext's call-id
	// folding) and cannot be re-expressed against the new merge without
	// restating a mechanism the spec no longer has. The BEHAVIOR they protected
	// that still applies under v6 — a turn's payload is bounded to the display
	// keep-window and restores correctly, a page-owned failure's error and a
	// turn's usage survive a covering refresh under ordinary version
	// supersession, RETAINED_ITEM_CAP eviction and restoration stay correct —
	// is exactly what the surviving tests in this block (and
	// reducer.history.test.ts's merge-by-version suite) already cover.
	describe("retained page-turn payloads are bounded to the display keep-window", () => {
		it("repeated loadOlder + rehydrate with display-row eviction keeps retained turn payloads bounded while every loaded turn's usage still counts", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();

			const ROW_PAGES = 12;
			const ROWS_PER_PAGE = 8;
			const PAYLOAD_ITEMS_PER_TURN = 20;
			// 28 cycles, not 40: the cap evicts every page row by rehydrate 24 (see
			// the loop note below), so the extra 12 cycles only re-projected an
			// ever-larger fresh window without exercising a new state, and pushed
			// this test's wall time near vitest's 5s budget under load (#2753). 28
			// keeps several post-eviction cycles of margin while proving the bound.
			const PAGES = 28;

			// A live conversation's fresh read window: `rows` display rows plus one
			// live turn's usage. The fresh window GROWS across the loop the way a
			// live conversation does, so the 500-row cap evicts the oldest retained
			// rows — the early pages' — exactly as live churn does on device. The
			// rows ride the live turn's model items (unpositioned, the fresh window's
			// own order): under the cutover rows re-project from the model, so a
			// synthetic items array no turn carries would vanish on the first
			// rehydrate and the window would never grow.
			const freshConversation = (rows: number) =>
				makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						{
							id: "ft",
							status: "completed",
							items: Array.from({ length: rows }, (_, j) => ({
								type: "userMessage" as const,
								id: `f-${j}`,
								turnId: "ft",
								text: `f-${j}`,
							})),
							usage: { inputTokens: 5, outputTokens: 1 },
						},
					],
					olderCursor: "wire",
				});
			const freshRead = (rows: number) => ({
				conversation: freshConversation(rows),
				olderCursor: "wire",
			});

			service.readProjectionResult = freshRead(40);
			await store.getState().openProjected(service, "ref-1");

			// Each page turn carries a usage stamp plus a heavy item payload: items
			// that project onto the page's display rows (while those rows are
			// retained) and payload-only items the projector filters — blank
			// warnings project no row, the pinned filtered-page shape. Pages past
			// ROW_PAGES carry no display rows at all: their turns are the
			// evicted/filtered class, and on current main they are exactly the
			// payloads that accumulate without bound.
			const pageTurn = (i: number): Turn =>
				wireTurnFragment(
					`pt-${i}`,
					[
						...(i <= ROW_PAGES
							? Array.from({ length: ROWS_PER_PAGE }, (_, j) => ({
									id: `w-${i}-${j}`,
									transcriptKey: `w-${i}-${j}`,
									turnId: `pt-${i}`,
									type: "agentMessage",
									text: `page ${i} row item ${j}`,
									position: { entry: 1000 - i * 16 - j, item: 0 },
									status: "completed",
								}))
							: []),
						...Array.from({ length: PAYLOAD_ITEMS_PER_TURN - (i <= ROW_PAGES ? ROWS_PER_PAGE : 0) }, (_, j) => ({
							id: `x-${i}-${j}`,
							transcriptKey: `x-${i}-${j}`,
							turnId: `pt-${i}`,
							type: "warning",
							position: { entry: 2000 - i * 16 - j, item: 0 },
							status: "completed",
						})),
					],
					{ inputTokens: 100, outputTokens: 10 },
				);

			for (let i = 1; i <= PAGES; i++) {
				service.olderItems = {
					turnsPage: turnsPage([pageTurn(i)], `cursor-${i}`),
					nextCursor: `cursor-${i}`,
				};
				const result = await store.getState().loadOlder(service);
				expect(result.status).toBe("loaded");

				// The fresh window grows 40 rows per refresh once the row-carrying
				// pages are done, so the cap evicts the oldest-positioned pages'
				// rows one refresh at a time (by rehydrate 24 every page row is
				// gone) while the row-less pages keep paging: a partially-evicted
				// in-window turn re-projects rows the cap had already discarded, but
				// those re-discards lose nothing the page brought, and the loop
				// keeps loading.
				const freshRows = i <= ROW_PAGES ? 40 : 40 + 40 * (i - ROW_PAGES);
				service.readProjectionResult = freshRead(freshRows);
				await store.getState().rehydrate(service);
			}

			const conv = store.getState().conversation!;
			const retainedPayloadItems = conv.turns.reduce(
				// The fresh read's own turn carries the read's window in the model
				// (the daemon bounds it by itemLimit on device) — the #1919 bound
				// governs retained PAGE payloads, so the count reads page history
				// only, the turns loadOlder added.
				(total, turn) => total + (turn.id === "ft" ? 0 : turn.items.length),
				0,
			);
			// The bound: only turns whose items intersect the retained display
			// window may keep payloads. By the final iteration every page row has
			// been evicted and every row-less page turn is outside the window, so
			// the retained payload set must be well under the worst in-window
			// shape (ROW_PAGES turns x 20 items) — on the unbounded main it is the
			// whole loaded transcript (PAGES turns x 20 items = 560).
			expect(retainedPayloadItems).toBeLessThanOrEqual(ROW_PAGES * PAYLOAD_ITEMS_PER_TURN);
			// Accounting completeness is the bound's other half: EVERY loaded turn
			// keeps identity... (order is not asserted; the merge places freely)
			expect(conv.turns.some((turn) => turn.id === `pt-${PAGES}`)).toBe(true);
			expect(new Set(conv.turns.map((turn) => turn.id)).size).toBe(PAGES + 1);
			// ...and usage, so the turn-summed fallback still covers everything
			// actually loaded: PAGES page turns plus the live turn.
			expect(sessionTokens(conv)).toEqual({
				inputTokens: 5 + 100 * PAGES,
				outputTokens: 1 + 10 * PAGES,
				scope: "loaded",
			});
			// The first page's rows were evicted by the cap, so its turn is the
			// trimmed shape: identity + usage survive, display-fallback payloads
			// do not.
			const evictedPageTurn = conv.turns.find((turn) => turn.id === "pt-1")!;
			expect(evictedPageTurn.items).toEqual([]);
			expect(evictedPageTurn.usage).toEqual({ inputTokens: 100, outputTokens: 10 });
			// The display window itself stayed capped through the churn.
			expect(conv.items.length).toBeLessThanOrEqual(500);
		});

		// Review round 1, finding 2: live notification paths cap display rows
		// too, so page rows can be evicted by live growth alone. The bound must
		// hold there as well — not only at page/rehydrate publishes — or a page
		// turn's full payloads linger until some later refresh happens to run.
		it("live growth that evicts a page's rows bounds its turn payloads without a rehydrate or page load", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// 492 fresh rows + an 8-row page = exactly the 500-row cap, so the
			// page loads without eviction and its turn keeps full payloads.
			service.readProjectionResult = {
				conversation: makeConversation(
					{
						threadId: "thread-1",
						instanceId: "instance-1",
						usage: null,
						items: Array.from({ length: 492 }, (_, j) => ({
							kind: "user" as const,
							id: `f-${j}`,
							text: `f-${j}`,
						})),
						// The fresh window's rows are model-backed: under the cutover the
						// rows are reprojected from the model's turns on every frame, so a
						// synthetic items array no turn carries would vanish on the first
						// live publish. ft carries the items f-0..f-491 project from.
						turns: [
							{
								id: "ft",
								status: "completed",
								items: Array.from({ length: 492 }, (_, j) => ({
									type: "userMessage" as const,
									id: `f-${j}`,
									turnId: "ft",
									text: `f-${j}`,
								})),
								usage: { inputTokens: 1, outputTokens: 1 },
							},
						],
						olderCursor: "c0",
					},
					true,
				),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							Array.from({ length: 8 }, (_, j) => ({
								id: `w-${j}`,
								transcriptKey: `w-${j}`,
								turnId: "pt",
								type: "agentMessage",
								text: `page item ${j}`,
								position: { entry: 100 - j, item: 0 },
								status: "completed",
							})),
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			const loaded = store.getState().conversation?.turns.find((t) => t.id === "pt");
			expect(loaded?.items).toHaveLength(8);

			// Live traffic alone (no rehydrate, no further page) appends 8 rows,
			// and each append's cap evicts one page row from the oldest end.
			for (let j = 0; j < 8; j++) {
				store.getState().applyNotification(
					historyUpdated({
						items: [{ ...{ type: "userMessage", id: `l-${j}`, text: `live ${j}` }, turnId: "ft" }],
					}),
				);
			}

			const conv = store.getState().conversation!;
			// The page's rows are all gone...
			expect(conv.items.some((row) => row.id.startsWith("w-"))).toBe(false);
			// ...so its turn is the compact shape: identity + usage survive, and
			// the payloads are bounded by the LIVE path's own bound — no
			// rehydrate or page load needed to trim them.
			const pageTurn = conv.turns.find((t) => t.id === "pt");
			expect(pageTurn?.items).toEqual([]);
			expect(pageTurn?.usage).toEqual({ inputTokens: 500, outputTokens: 20 });
			expect(conv.items.length).toBeLessThanOrEqual(500);
			expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
		});

		// RoboRev round 30, coverage aliases: the cursor gate's skeleton-free
		// re-merge lost the alias relationships the real merge saw through the
		// injected skeleton. A compact turn's item returns keyed under a NEW
		// bare id (the hub reissues under a new wire id while the transcript
		// key stands), then a complete reread re-serves the same content
		// KEYLESS under the ORIGINAL id. The real merge folds the restored
		// item into the reread through the remembered alias, but the re-merge
		// matched nothing — the restored turn read as uncovered history and,
		// an unchanged turn supplying overlap, the stale retained cursor
		// overrode the complete reread's absent one and sessionTokens reported
		// "loaded" for a complete read.

		// RoboRev round 31, omitted usage: alias consumption is not field
		// consumption. The restored turn's ITEMS folded into the reread
		// through the remembered alias, but the reread's turn omitted the usage
		// stamp — the retained stamp survives the merge (the fresh side's
		// nullish fallback), so it is still history the fresh read lacks and
		// the cursor gate must keep claiming it. A membership gate that read
		// item consumption alone dropped the claim and let the complete
		// reread's absent cursor discard a stamp the merged model still sums.

		// #2152 corner (a): alias consumption is not field consumption for the
		// ITEM payloads either. The complete reread re-serves the remembered
		// identity through the alias bridge but omits the payload itself (the
		// sparse fragment shape), so the retained item's text survives the
		// merge and is still history the fresh read lacks. The gate used to
		// accept the turn on output membership alone (the fresh side reached the
		// identity, so the output "carries fresh") and consumed it, and the
		// re-merge never judged the item: the complete reread's absent cursor
		// discarded the retained one and sessionTokens mislabeled a read that
		// never carried the payload as a session total. The package's own
		// per-item verdict must gate the consumption now.

		// #2152 corner (b): the per-turn consumption check bailed on the first
		// direct identity match, so a mixed direct+alias turn kept its whole
		// membership in coverageOlderTurns and the ALIAS-ONLY item claimed
		// against a re-merge that cannot see its bridge — the stale retained
		// cursor survived a COMPLETE reread and sessionTokens offered "loaded"
		// forever. The landed shape reaches the mixed turn only through the
		// full compact chain: the page's own turn sheds to memory at a
		// refresh whose window omits it, a sparse
		// restore lands the content under the reissue identity with no text, a
		// later page brings the item's real text (folding through the
		// remembered alias, which is what makes the restored item page-owned
		// and able to survive a covering reread's twin filter beside a
		// directly-matched sibling), and the complete reread then re-serves the
		// remembered identity keyless under the original id — one item of the
		// turn alias-only, one unchanged. Both fully carried: the package's
		// per-item verdict must consume the turn and let the complete read's
		// absent cursor stand.

		// RoboRev review round 2: the alias-consumption exclusion must not
		// take the turn's DIRECTLY MATCHED items out of the window-overlap
		// evidence. The re-merge that judges coverage claims runs without the
		// consumed turn, and reading ITS overlap made a refresh whose only
		// continuity was the consumed turn's direct matches read as disjoint,
		// resetting the store's own paging cursor to the read's even though
		// the windows share history. The overlap reads the REAL merge's
		// signal, which the exclusion never touched.

		// RoboRev round 31, unmatched empty turns: an empty retained turn with
		// a usage stamp the fresh read does not re-serve is retained history
		// even though it has no items to match — the reducer's coverage
		// semantics claim its accounting data. A membership gate keyed on fold
		// ids passed it (its unmatched group still lands in olderTurnFolds) and
		// skipped it on the empty-items check, silently dropping the claim.

		// RoboRev round 31, warning-only claims: a retained warning the fresh
		// read omits is display content, not accounting data — the reducer's
		// coverage semantics never let a warning-only contribution claim older
		// coverage. A membership gate that counted any un-folded item as a
		// claim let the warning restore the stale pagination cursor and report
		// "loaded" for a complete snapshot.
		it("an unmatched warning-only turn does not claim cursor coverage", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						fillerTurn(500),
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "rk",
									transcriptKey: "PK",
									turnId: "rt",
									type: "agentMessage",
									text: "real text",
									position: { entry: 98, item: 0 },
									status: "completed",
								},
							],
							usage: { inputTokens: 9, outputTokens: 9 },
						},
					],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// After pagination the store retains a warning alongside the ordinary
			// message: a row-less page turn that compacts at its own load (its
			// ownership moves to the compact set — the preserve gate that keeps
			// retained turns across the reread reads exactly those sets), and a
			// warning turn whose failure row the page owns, seated above the
			// filler bulk so the cap never evicts it — the reread keeps the row
			// and the warning turn keeps its window seat.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"ct",
							[
								{
									id: "x-0",
									transcriptKey: "px",
									turnId: "ct",
									type: "agentMessage",
									text: "payload",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
						wireTurnFragment("wt", [
							{
								id: "w-0",
								turnId: "wt",
								type: "warning",
								text: "careful",
								position: { entry: 200, item: 0 },
								status: "completed",
							},
						]),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "ct")?.items).toEqual([]);

			// A complete fresh read that repeats the unchanged real turn and its
			// message but omits the warning entirely.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						fillerTurn(500),
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "rk",
									transcriptKey: "PK",
									turnId: "rt",
									type: "agentMessage",
									text: "real text",
									position: { entry: 98, item: 0 },
									status: "completed",
								},
							],
							usage: { inputTokens: 9, outputTokens: 9 },
						},
					],
				}),
				olderCursor: null,
			};
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			// The warning's turn survived with its warning item (the retained
			// failure row still backs it)...
			expect(conv.turns.find((turn) => turn.id === "wt")?.items.map((item) => item.type)).toEqual(["warning"]);
			// ...but a warning is not accounting data: with nothing else claiming,
			// the complete read's absent cursor stands.
			expect(conv.olderCursor).toBeUndefined();
			expect(sessionTokens(conv)).toEqual({
				inputTokens: 509,
				outputTokens: 29,
				scope: "session",
			});
		});

		// Review round 1, finding 1: the package's merges match fragments by
		// ITEM identity when turn ids differ. A compact survivor has no items to
		// match with, so when the wire re-issues its content under another turn
		// id the two would otherwise both survive and sessionTokens would
		// double-count their usage. The remembered-identity fold below keeps
		// exactly one turn with exactly one usage stamp.

		// Review round 1, finding 1 (page side): the same fold through a later
		// PAGE that re-issues a compact turn's content under another turn id.
		// mergeOlderItemPage makes the retained copy the newer merge input, so
		// the compact side's usage wins there. The compact turn here is the
		// pinned evicted/filtered shape — a page turn whose payload items back
		// no retained row — so it compacts at its own load while the window
		// keeps spare capacity for the re-issuing page's rows.

		// Review round 2, finding 2.1: a compact turn that is only PARTIALLY
		// restored (a same-id page fragment brings back one of its items) must
		// keep folding later re-issues of its remaining identities — the
		// remembered set may not be replaced by the restored subset.

		// Review round 2, finding 2.2: a compact fragment whose remembered
		// identities connect TWO incoming turns must coalesce all of them —
		// the package's own grouping is transitive, and stopping at the first
		// match would leave the second turn counted separately.

		// Review round 2, finding 2.3: itemIdentityMatches compares IDS when
		// either side lacks a transcript key, so a remembered identity must keep
		// both fields — a later id-only fragment of the same item has to fold.

		// Review round 3, skeleton dedupe: repeated restore-and-trim cycles of
		// unchanged history must keep the remembered set constant (the dedupe)
		// and the cycle's outcomes stable — the turn keeps folding, its usage
		// keeps counting, and nothing duplicates.

		// Review round 3, text adoption guard: a skeleton whose shed item carried
		// a transcript key, re-issued by a page item with the same BARE ID (no
		// transcript key), must still get the page's text back after the fold —
		// the guard may not return early before the id fallback runs.

		// Review round 3, ownership transfer: an id-only remembered identity
		// whose re-issue GAINED a transcript key folds by id; the transfer must
		// still find the surviving turn under the bare id so the compact turn's
		// OTHER remembered identities stay foldable.

		// Review round 3, cursor gate: a partial reissue must not let the
		// unmatched skeleton's coverage claim keep the accumulated wire cursor
		// over the fresh read's own — skeletons are memory, not retained
		// transcript evidence.

		// Review round 4, sparse reissue: a reread wire item that omits text (the
		// sparse fragment shape thread/turns/list sends for stripped entries)
		// folding against an injected skeleton must settle to a valid empty
		// string. mergePageItem's textSource selection can pick the skeleton, and
		// a skeleton leaking an undefined text would surface as "undefined"
		// prefixes during streaming and throw in reasoningText's item.text.length.
		// The wire-text repair cannot rescue it either: a wire item that omitted
		// text has nothing to adopt.
		it("a sparse reread reissue of a compact identity settles to empty text, never undefined", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// Row-less page turn: compacts at its own load and remembers px.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "x-0",
									transcriptKey: "px",
									turnId: "pt",
									type: "warning",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

			// The reread re-issues px with NO text field — the sparse wire shape —
			// under the same turn id, so the fold adopts the fresh item's own row
			// and pt stays in-window. With nothing to adopt, the fold's settle must
			// be the valid empty string the wire's own hydration would produce.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						{
							id: "pt",
							status: "completed",
							items: [
								// The wire's own sparse shape: text "" plus the omitted
								// marker, exactly what hydrating a text-less wire item
								// produces — a bare model literal would leak undefined.
								markItemTextOmitted({
									id: "rx",
									transcriptKey: "px",
									turnId: "pt",
									type: "agentMessage",
									text: "",
									position: { entry: 99, item: 0 },
									status: "completed",
								}),
							],
							usage: { inputTokens: 500, outputTokens: 20 },
						},
						{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().rehydrate(service);
			const folded = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
			expect(folded).toHaveLength(1);
			expect(folded[0]?.transcriptKey).toBe("px");
			expect(folded[0]?.text).toBe("");
			// The string invariant reasoningText and the streaming prefix logic
			// both lean on.
			expect(folded[0]?.text.length).toBe(0);
			expect(sessionTokens(store.getState().conversation!)).toEqual({
				inputTokens: 501,
				outputTokens: 21,
				scope: "loaded",
			});
		});

		// Review round 4, restored-identity presence: an id-only compaction
		// restored carrying a transcript key is the SAME item as its remembered
		// id-only skeleton (the package's identity rule matches them by id), so
		// a later overlapping page must not inject the skeleton beside the real
		// item — the injection's fold would erase the retained copy's text and
		// the wire-text repair would then re-adopt the OLDER page's text.
		it("an id-only compaction restored with a key keeps the retained text through an overlapping page", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// Row-less page turn carrying an ID-ONLY item: compacts at its own
			// load, remembering the bare id.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "a-0",
									turnId: "pt",
									type: "warning",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

			// Restoration: the fresh read re-issues the item with a gained
			// transcript key (same id); the item's own projection is the row that
			// keeps it in the window.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						{
							id: "pt",
							status: "completed",
							items: [
								{
									id: "a-0",
									transcriptKey: "kk",
									turnId: "pt",
									type: "agentMessage",
									text: "retained new",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							usage: { inputTokens: 500, outputTokens: 20 },
						},
						{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().rehydrate(service);
			const restored = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
			expect(restored).toHaveLength(1);
			expect(restored[0]?.id).toBe("a-0");
			expect(restored[0]?.transcriptKey).toBe("kk");
			expect(restored[0]?.text).toBe("retained new");

			// The overlapping older page re-issues the same item with DIFFERENT
			// text. The retained copy is the newer merge input and must win: the
			// remembered id-only skeleton must not be injected beside the real
			// item, so nothing erases the retained text and re-adopts the page's.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "a-0",
									transcriptKey: "kk",
									turnId: "pt",
									type: "agentMessage",
									text: "stale old",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
			expect(after).toHaveLength(1);
			expect(after[0]?.transcriptKey).toBe("kk");
			expect(after[0]?.text).toBe("retained new");
			expect(sessionTokens(store.getState().conversation!)).toEqual({
				inputTokens: 501,
				outputTokens: 21,
				scope: "loaded",
			});
		});

		// Review round 5, sparse settle is not authoritative: after a sparse
		// reissue folds to the empty settle, a later overlapping page that brings
		// the item's REAL text must still win it. The folded item carries the
		// skeleton's omitted-text semantics, so mergePageItem treats the empty
		// settle as "nothing to say" — never as an authoritative empty that
		// blocks the item's actual text from loading.

		// Review round 5, ownership transfer: a remembered KEYED item must not
		// transfer to a surviving turn whose item merely shares its BARE id under
		// a conflicting transcript key. The wrong transfer would let a later page
		// fragment coalesce that unrelated turn (dropping its separate usage
		// stamp and polluting its items) instead of folding into the turn that
		// actually carries the remembered content.

		// Review round 6, alias folds: an item restored under a different id with
		// the same transcript key and compacted again leaves BOTH id aliases
		// remembered. A later page restoring a DIFFERENT item of the same turn
		// injects both aliases, and the package folds them into each other — a
		// placeholder no real side contributed to. The strip pass must remove
		// it: an unrestored placeholder would claim transcript coverage a later
		// rehydrate reads as retained evidence.

		// Review round 6, explicit empty text: overlapping page fragments that
		// provide text and then an EXPLICIT "" for the same identity settle to
		// the empty string (the reducer keeps the later fragment's provided
		// value). Nothing may reinstate the earlier fragment's stale text over
		// that legitimate empty — not the reconcile of the two fragments within
		// the page, and not the fold into the turn's remembered skeleton.
		it("a later fragment's explicit empty text is not overwritten by an earlier fragment's text", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page brings px as a real row item: under D23d the model keeps
			// the page turn's own row while it stays in the window.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "x-0",
									transcriptKey: "px",
									turnId: "pt",
									type: "agentMessage",
									text: "payload",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "px", text: "payload" }),
			]);

			// Window churn: the fresh read fills the window, the cap evicts pt's
			// row — the oldest-positioned one — and the bound compacts the turn,
			// remembering px through the item's real row shape.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						fillerTurn(500),
						{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } },
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().rehydrate(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "pt")?.items).toEqual([]);

			// The next read shows a smaller window (the snapshot is authoritative
			// for the fresh window), so the overlapping page's folded row survives
			// the cap and paging keeps flowing.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().rehydrate(service);

			// Overlapping page carrying the SAME turn twice: the first fragment
			// provides text, the second explicitly clears it. The later
			// fragment's empty is the legitimate settle.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "r1",
									transcriptKey: "px",
									turnId: "pt",
									type: "agentMessage",
									text: "hello",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
						wireTurnFragment(
							"pt",
							[
								{
									id: "r2",
									transcriptKey: "px",
									turnId: "pt",
									type: "agentMessage",
									text: "",
									position: { entry: 99, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const folded = store.getState().conversation?.turns.find((t) => t.id === "pt")?.items ?? [];
			expect(folded).toHaveLength(1);
			expect(folded[0]?.transcriptKey).toBe("px");
			expect(folded[0]?.text).toBe("");
		});

		// Review round 8, tool-result folds: the incoming page can restore a
		// compact turn's OTHER identity while carrying the RESULT of the tool
		// call the same compact turn remembers as a skeleton. The injection
		// puts the call skeleton into the merge, the package's tool fold
		// removes the page's result item and carries its fields onto that
		// skeleton by callId, and the rewritten host is a new object whose
		// identity matches no real source — so the strip pass deleted it, and
		// BOTH representations of the result (the folded host and the removed
		// result item) vanished from conversation.turns. A skeleton host that
		// received a real result's fields is content, not remembered memory:
		// it must survive the strip.
		// D23d staging: the page turn keeps its rows while it is in the window,
		// so the compact the fold depends on is staged by window churn — a
		// fresh read that fills the window evicts pt's rows and the bound
		// compacts the turn, remembering the call skeleton and ka.

		// Review round 8, transitive alias folds: the fresh read re-issues a
		// remembered keyless item under its bare id now carrying a transcript
		// key, and a second fresh fragment shares that key under a different id —
		// the package coalesces the chain, and the merged item's FINAL identity
		// (the second fragment's) matches neither remembered skeleton. The
		// ownership transfer matched only final identities, found no carrier, and
		// deleted the vanished compact turn's memory — including the
		// still-unrestored identities it also remembered. A later reissue of one
		// of those then survived beside the carrier and double-counted usage.
		// The transfer must follow the merge's own fragment membership instead.
		// D23d staging: the page turn keeps its rows while it is in the window,
		// so the compact the alias fold depends on is staged by window churn —
		// a fresh read that fills the window evicts ta's rows and the bound
		// compacts the turn, remembering fa and fz. The fixture's own tf1/tf2
		// read is small, so it doubles as the window shrink.

		// Review round 8, cursor coverage without injection: compact-only turns
		// were excluded from the coverage merge only when a collision had
		// actually injected skeletons. With NO collision the unmatched compact
		// turn itself still entered the merge and claimed olderCoverage (usage
		// metadata counts as coverage for an empty turn), and an unchanged real
		// turn supplied transcriptOverlap — so the stale wire cursor overrode a
		// complete fresh read. A compact-only turn holds no transcript content by
		// construction; it must never supply coverage, injected or not.
		// D23d staging: the window starts full (the #1919 keep-window bulk
		// beside the real turn), so the page's own row — the oldest — is
		// cap-discarded at its own load and the turn compacts immediately. The
		// complete read re-serves the whole window, so nothing is left behind
		// as an empty turn that could claim coverage for a reason the pin is
		// not about.
		it("a noncolliding compact turn does not claim cursor coverage over a complete fresh read", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "rk",
									transcriptKey: "PK",
									turnId: "rt",
									type: "agentMessage",
									text: "real text",
									position: { entry: 98, item: 0 },
									status: "completed",
								},
							],
							usage: { inputTokens: 9, outputTokens: 9 },
						},
						fillerTurn(499),
					],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page's position says older history — below the snapshot's own
			// oldest row — so its row is the oldest in the merged projection and
			// the full window's cap discards it at the turn's own load: ct
			// compacts immediately and remembers px.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"ct",
							[
								{
									id: "x-0",
									transcriptKey: "px",
									turnId: "ct",
									type: "agentMessage",
									text: "payload",
									position: { entry: 50, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "ct")?.items).toEqual([]);

			// A COMPLETE fresh read repeats the unchanged real turn and re-issues
			// nothing the compact turn remembers — no collision, no injection —
			// and re-serves the window bulk it still holds. The fresh read's own
			// cursor is null: nothing older exists.
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "rk",
									transcriptKey: "PK",
									turnId: "rt",
									type: "agentMessage",
									text: "real text",
									position: { entry: 98, item: 0 },
									status: "completed",
								},
							],
							usage: { inputTokens: 9, outputTokens: 9 },
						},
						fillerTurn(499),
					],
				}),
				olderCursor: null,
			};
			await store.getState().rehydrate(service);
			const conv = store.getState().conversation!;
			// The complete read's null cursor stands: the compact turn survives
			// with its usage, but usage metadata is not transcript coverage.
			expect(conv.olderCursor).toBeUndefined();
			expect(conv.turns.find((turn) => turn.id === "ct")?.usage).toEqual({
				inputTokens: 500,
				outputTokens: 20,
			});
			expect(sessionTokens(conv)).toEqual({
				inputTokens: 509,
				outputTokens: 29,
				scope: "session",
			});
		});

		// Review round 7, cursor coverage: a compact turn that the injected
		// merge folds into a fresh carrier under a different id must not claim
		// older coverage in the cursor gate's uninjected merge — an unmatched
		// empty turn with usage counts as coverage there. With an unchanged real
		// turn supplying transcriptOverlap, the stale cursor would override a
		// fresh read that actually covers everything, and sessionTokens would
		// report "loaded" for a complete read.
		// D23d staging: the window starts full (the #1919 keep-window bulk),
		// so the page's own row — the oldest — is cap-discarded at its own
		// load and the turn compacts immediately. The complete read re-serves
		// the bulk it still holds, so nothing is left behind as an empty turn
		// that could claim coverage for a reason the pin is not about, and the
		// renamed reissue rides last so the cap's one-row trim takes PK's row
		// — rt's usage survives its compact — not the reissued px.

		// Review round 9, alias chains through the strip: a compact turn can
		// remember TWO id aliases of one persisted item (restored under a
		// different id, compacted again). When a later page supplies the item
		// KEYLESS under the first alias's bare id, the merge folds the real page
		// item through BOTH remembered skeletons and the merged item settles on
		// the SECOND alias's identity — carried by no real source under the
		// strip's exact rule. The restored text used to die with it: the page
		// item itself was consumed by the fold. The strip must track real-source
		// participation through the item merges, not reconstruct it from final
		// identities.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compact the alias chain depends on is staged by window churn —
		// a fresh read that fills the window evicts pt's row and the bound
		// compacts the turn before the restoring read folds its memory into
		// the carrier.

		// Review round 16, notification replacements: the package's folds
		// build each live-updated item as a NEW object off the model item they
		// found — and the fold-source ancestry is keyed by object. A streaming
		// delta after an alias fold must inherit the ancestry: the resync it
		// schedules is a retention pass whose rows still name only the
		// folded-from alias, so the turn stays in the window through the
		// ancestry the fold recorded — or the resync sheds the payload the
		// visible row displays.

		// Review round 9, pagination bridges: a loadOlder page can bridge a
		// compact turn into ANOTHER retained turn through a shared transcript
		// key — the page item identity-matches the compact turn's remembered
		// keyless id AND the retained turn's keyed item — and the merged turn
		// then carries the retained turn's id. The compact turn's own id does
		// not always survive at loadOlder the way the transfer's comment
		// assumed: matched only by final identities, the bridge left no carrier
		// for the vanished turn's memory, and deleting it forgot the turn's
		// still-unrestored identities. A later page reissuing one of those
		// survived separately and double-counted usage. The transfer must read
		// the retained side's fold membership from the page merge.
		// D23d staging: the page turn keeps its rows while they are in the
		// window, so the compact the bridge depends on is staged by window
		// churn — a fresh read that fills the window evicts ta's rows (the
		// oldest) and the bound compacts the turn. tb's own row sits above the
		// bulk so the same read keeps it live for the bridge.

		// Review round 10, superseded aliases: the injection skipped a
		// remembered skeleton whenever the turn already carried a real item
		// matching it — but a restored item supersedes its skeleton under ONE
		// alias only. Compaction remembered {id ra, transcriptKey kr}; the
		// fresh read restored kr under a DIFFERENT id; a later page then
		// re-issued the item KEYLESS under the remembered bare id ra. The
		// keyless reissue matches neither the restored item (keyed kr, another
		// id) nor the turn id, so with the alias skeleton skipped the reissue
		// survived as its own turn and its usage double-counted. The remembered
		// alias must stay injectable for turn matching while the restored
		// payload stays authoritative.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compact the alias pin depends on is staged by window churn —
		// a fresh read that fills the window evicts pt's row and the bound
		// compacts the turn before the restoring read folds its memory into
		// the carrier (which doubles as the window shrink).

		// Review round 10, folded call chains: a real KEYLESS call reissue can
		// fold through remembered call aliases into a final identity no real
		// source carries, and the tool-result fold then rewrites that call with
		// the page's real result — removing the result item. The strip's
		// tool-host guard rejected the rewritten call because the PAGE carried
		// a real call with that callId — but that call was consumed by the
		// alias chain, so nothing else held the result's content. Both
		// representations of the result vanished. The guard must ask whether a
		// real call with that callId SURVIVES in the merged output, not whether
		// one existed among the inputs.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compacts the alias chain depends on are staged by window churn
		// — a fresh read that fills the window evicts the page turn's row, and
		// a second one evicts the carrier's restored row — with a small read
		// after each so the later pages' folded rows survive the cap.

		// Review round 16, result-row backing: the tool fold moves a result's
		// fields onto its call and removes the result item, so the call can be
		// the ONLY model payload behind a visible result row. When the rows
		// retain the RESULT — the call never had a row of its own — the call's
		// own identity names no retained row, and the identity-fold ancestry
		// does not either (the callId fold is a different mechanism with its
		// own participation rule): the window must read the result identities
		// the call absorbed, or the bound deletes the payload the visible row
		// displays. D23d: the rows re-project from the merged model, and the
		// callId fold removes the result item before any projection runs, so
		// the folded pair projects ONE row — the host's own. The literal
		// result-row vehicle is gone with the row-side window; what remains to
		// pin is that the pair folds into a single host whose own row keeps
		// the full folded payload in the window. The bound's absorbed-identity
		// read stays as defense for shapes a future projector could still mint.
		it("a folded call keeps its payload when only the result row is retained", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A page turn carrying a call and its result: the pair folds into a
			// single host item, so the host's own row is the only row either
			// side ever projects.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "item_tool_1",
									type: "commandExecution",
									toolName: "shell",
									callId: "call-1",
									argumentsJson: '{"command":"make"}',
									status: "inProgress",
									position: { entry: 40, item: 0 },
								},
								{
									id: "item_tool_result_1",
									type: "commandExecution",
									toolName: "shell",
									callId: "call-1",
									output: "ok",
									exitCode: 0,
									status: "completed",
									position: { entry: 41, item: 0 },
								},
							],
							{ inputTokens: 700, outputTokens: 70 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			// The folded host's own row is the retained row, and it carries the
			// result's content.
			expect(after.items.some((row) => row.id === "item_tool_1")).toBe(true);
			expect(after.turns.find((turn) => turn.id === "pq")?.items ?? []).toEqual([
				expect.objectContaining({
					id: "item_tool_1",
					callId: "call-1",
					output: "ok",
					exitCode: 0,
					status: "completed",
				}),
			]);
		});

		// RoboRev panel follow-up (#2152): the round-8/16 pins cover a result
		// arriving beside an identity the compact turn also remembers. A
		// result-ONLY fragment shares none of that. The callId fold collapses a
		// call/result pair into one item before the turn ever compacts, so the
		// compact memory holds the call skeleton alone and the result's own
		// identity is remembered nowhere. The identity-only collision scan then
		// found no collision, injected no skeleton, and the fragment survived as
		// its own turn carrying the result — a second turn for content the
		// compact host already accounts, with its usage stamp counting beside
		// the host's in sessionTokens' turn-summed fallback. callId is a
		// collision dimension of its own between remembered tool skeletons and
		// incoming tool call/result items: the skeleton injects, and the result
		// merges into the host through the package's existing callId fold
		// exactly like the shared-identity paths.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compact the callId pin depends on is staged by window churn —
		// a fresh read that fills the window evicts the call's row and the
		// bound compacts the turn, with a small read after it so the result
		// page's folded host keeps a window seat.

		// RoboRev round 29: the window's row side carries bare ids too. A
		// KEYLESS backing item matches a keyed row by bare id under the
		// package's own identity rule (itemIdentityMatches falls to the id when
		// one side carries no key), so a keyed row that contributed only its
		// transcript key left its keyless backing item outside the window and
		// the bound shed the payload behind a row still on screen.
		it("a keyed retained row keeps its keyless backing item's turn in the window", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A page whose display row is keyed while its backing fragment item
			// arrives keyless — the exact pair itemIdentityMatches still matches
			// by bare id.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pt",
							[
								{
									id: "ra",
									turnId: "pt",
									type: "agentMessage",
									text: "alpha",
									position: { entry: 90, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 500, outputTokens: 20 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			const conv = store.getState().conversation!;
			// The keyed row backs the keyless item by bare id, so the turn keeps
			// its full payload inside the window.
			expect(conv.turns.find((t) => t.id === "pt")?.items).toEqual([
				expect.objectContaining({ id: "ra", text: "alpha" }),
			]);
			// Keeping the window seat never changes the accounting: the turn's
			// stamp counts once either way.
			expect(sessionTokens(conv)).toEqual({ inputTokens: 501, outputTokens: 21, scope: "loaded" });
		});

		// Review round 11, alias reconciliation at rehydrate: the same
		// duplicate-identity hole the superseded-alias fold opens at loadOlder
		// opens at rehydrate too — the turn carries its restored item under one
		// alias, the injected remembered alias hosts the fresh read's keyless
		// reissue, and the merge leaves BOTH items keyed kr: the fresh text on
		// one, the superseded retained text on the other. One identity must
		// reconcile to one item, with the fresh side winning at rehydrate.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compact the alias pin depends on is staged by window churn —
		// a fresh read that fills the window evicts pt's row and the bound
		// compacts the turn before the restoring read folds its memory into
		// the carrier (which doubles as the window shrink).

		// Review round 12, reconciliation precedence: the duplicate
		// reconciliation initially treated display order as freshness order. A
		// page can carry a keyless alias of an item beside a keyed stale
		// sibling; the retained turn holds the item's fresh payload. The merge
		// folds the retained item into the keyless alias (first identity match)
		// while the stale sibling sits untouched — and the reconciliation then
		// let that PURE OLDER sibling fold over the fresh result, publishing
		// the stale text. Source precedence must decide the fold, not list
		// order: content from the newer side of the merge wins independently
		// of where the aliases sort.
		it("a page's untouched stale alias does not overwrite the retained fold", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A retained turn holds the item's fresh payload under
			// {id pa, transcriptKey pk}, in window.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"rt",
							[
								{
									id: "pa",
									transcriptKey: "pk",
									turnId: "rt",
									type: "agentMessage",
									text: "fresh",
									position: { entry: 30, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 50, outputTokens: 5 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "pk", text: "fresh" }),
			]);

			// The next page carries a KEYLESS alias of the same item (the retained
			// item's bare id) followed by a keyed sibling holding the item's
			// STALE text. The merge folds the retained item into the keyless
			// alias — the first identity match — and the untouched stale sibling
			// must not then fold over the fresh result.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "pa",
									turnId: "pq",
									type: "agentMessage",
									text: "older keyless",
									position: { entry: 30, item: 0 },
									status: "completed",
								},
								{
									id: "sb",
									transcriptKey: "pk",
									turnId: "pq",
									type: "agentMessage",
									text: "stale",
									position: { entry: 31, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "pk", text: "fresh" }),
			]);
		});

		// Review round 13, skeleton participation: an injected skeleton's
		// participation is identity-only — it carries the omitted-text marker
		// and no payload — but the reconciliation's source-precedence check
		// counted it as fresh-side participation anyway. A POSITIONED restored
		// item beside its UNPOSITIONED remembered alias then both read as
		// fresh, the tiebreak fell to display order, and because the
		// unpositioned skeleton fold sorts last, an older keyless reissue's
		// STALE text — folded through the alias — overwrote the restored text.
		// Precedence must come from the inputs that actually supplied payload,
		// and an identity-only skeleton supplies none.
		// D23d staging: the page turn keeps its row while it is in the window,
		// so the compact the alias pin depends on is staged by window churn —
		// a fresh read that fills the window evicts pt's row (the turn weaves
		// ahead of the bulk either way) and the bound compacts the turn before
		// the restoring read folds its memory into the carrier (which doubles
		// as the window shrink).

		// Review round 14, non-text payload: omitted text does not mean an item
		// lacks payload. A hydrated tool item can carry a current output,
		// arguments, and status while the wire omitted its text, and the
		// freshness check read exactly that — text omitted — so a retained tool
		// item folding into a page's keyless alias classified its fold as
		// payload-less. A later stale keyed sibling then tied with it and let
		// display order overwrite the retained fields. Identity-only skeletons
		// are the items to exclude — the ones carrying no field a fold keeps —
		// never real items merely because their text is omitted.
		it("a retained tool item with omitted text keeps its fields through the fold", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A retained tool item whose wire omitted the text field — hydrated
			// with the omitted-text marker — carrying its CURRENT output.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"rt",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									toolName: "shell",
									callId: "call-9",
									argumentsJson: '{"command":"make"}',
									output: "current output",
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 50, item: 0 },
								},
							],
							{ inputTokens: 60, outputTokens: 6 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kt", output: "current output" }),
			]);

			// The next page carries the item's KEYLESS alias (the retained item's
			// bare id, text omitted too) followed by a keyed sibling holding the
			// item's STALE output. The retained item folds into the keyless alias
			// (the first identity match) — and its fields, not the stale
			// sibling's, must come out of the reconciliation.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									toolName: "shell",
									callId: "call-9",
									argumentsJson: '{"command":"make"}',
									status: "inProgress",
									position: { entry: 50, item: 0 },
								},
								{
									id: "item_tool_8",
									type: "commandExecution",
									toolName: "shell",
									callId: "call-8",
									argumentsJson: '{"command":"make"}',
									output: "stale output",
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kt", output: "current output" }),
			]);
		});

		// Review round 16, hydration shape: the structural identity-only check
		// read key PRESENCE, but hydration creates every field as an enumerable
		// property — undefined-valued when the wire omitted it — so the wire's
		// own sparse identity-only fragment (a bare call reissue: identity and
		// ordering fields only) never passed the check. A payload-free retained
		// fragment folding into a page's keyless alias then counted as FRESH
		// payload participation, and the stale content the alias carried won
		// duplicate reconciliation by source precedence over the keyed sibling
		// holding the restored fields.
		it("a sparse wire fragment folds without claiming freshness over a restored sibling", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A retained turn holding the SPARSE fragment the wire reissued:
			// identity and ordering fields only — no text, no arguments, no
			// output, no status. The projector rows it under its transcript key.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"rt",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									callId: "call-9",
									transcriptKey: "kt",
									position: { entry: 50, item: 0 },
								},
							],
							{ inputTokens: 60, outputTokens: 6 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
				expect.objectContaining({ id: "item_tool_7", transcriptKey: "kt" }),
			]);

			// The next page carries the fragment's KEYLESS alias — holding the
			// STALE output — followed by a keyed sibling holding the restored
			// one. The sparse retained fragment folds into the keyless alias
			// (the first identity match); the fold supplies no payload, so the
			// merged item must not claim freshness, and the reconciliation must
			// let the later keyed sibling's restored fields stand.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									callId: "call-9",
									output: "stale output",
									exitCode: 1,
									status: "failed",
									position: { entry: 50, item: 0 },
								},
								{
									id: "item_tool_8",
									type: "commandExecution",
									callId: "call-8",
									output: "restored output",
									exitCode: 0,
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kt", output: "restored output" }),
			]);
		});

		// Review round 17, field-scoped freshness: a fresh fragment carrying
		// ONLY a status does supply payload — the merged item is fresh — but
		// its freshness covers only the fields the fresh side supplied. The
		// output the merged item inherited from the older alias is older
		// content, and its conflict with the later keyed sibling's restored
		// output must resolve in list order, not ride the item's status-borne
		// freshness.
		it("a status-only fresh fragment does not promote inherited stale output", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A retained turn holding a STATUS-ONLY fragment: identity and
			// ordering fields plus a completed status — no text, no arguments,
			// no output. The status is real payload, so the fragment is not
			// identity-only; the projector rows it under its transcript key.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"rt",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									callId: "call-9",
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 50, item: 0 },
								},
							],
							{ inputTokens: 60, outputTokens: 6 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
				expect.objectContaining({ id: "item_tool_7", transcriptKey: "kt", status: "completed" }),
			]);

			// The next page carries the fragment's KEYLESS alias — holding the
			// STALE output — followed by a keyed sibling holding the restored
			// one. The status-only fragment folds into the keyless alias: the
			// merged item is fresh through the status, but the output it carries
			// came from the older alias, so the restored sibling's output must
			// still win the reconciliation.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "item_tool_7",
									type: "commandExecution",
									callId: "call-9",
									output: "stale output",
									exitCode: 1,
									status: "failed",
									position: { entry: 50, item: 0 },
								},
								{
									id: "item_tool_8",
									type: "commandExecution",
									callId: "call-8",
									output: "restored output",
									exitCode: 0,
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kt", output: "restored output" }),
			]);
		});

		// Review round 18, omitted text is not supplied text: hydration gives
		// every item an enumerable text property — "" under the omitted
		// marker when the wire carried none — so the fresh-supplied field set
		// counted text as supplied for every leaf, presence check or not. A
		// status-only fresh fragment folding an older alias therefore promoted
		// the alias's inherited stale TEXT over a later sibling's restored
		// text: text counts as supplied only when a fresh leaf provided it.
		it("a status-only fresh fragment does not promote inherited stale text", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: Array.from({ length: 480 }, (_, j) => ({
						kind: "user" as const,
						id: `f-${j}`,
						text: `f-${j}`,
					})),
					turns: [{ id: "ft", status: "completed", items: [], usage: { inputTokens: 1, outputTokens: 1 } }],
					olderCursor: "c0",
				}),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// A retained turn holding a STATUS-ONLY agentMessage fragment:
			// identity and ordering fields plus a completed status — no text of
			// its own. The projector rows it under its transcript key.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"rt",
							[
								{
									id: "msg_7",
									type: "agentMessage",
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 60, item: 0 },
								},
							],
							{ inputTokens: 60, outputTokens: 6 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([
				expect.objectContaining({ id: "msg_7", transcriptKey: "kt", status: "completed" }),
			]);

			// The next page carries the fragment's KEYLESS alias — holding the
			// STALE text — followed by a keyed sibling holding the restored
			// text. The status-only fragment folds into the keyless alias: the
			// merged item is fresh through the status, but the text it carries
			// came from the older alias, so the restored sibling's text must
			// still win the reconciliation.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "msg_7",
									type: "agentMessage",
									text: "stale text",
									position: { entry: 60, item: 0 },
								},
								{
									id: "msg_8",
									type: "agentMessage",
									text: "restored text",
									status: "completed",
									transcriptKey: "kt",
									position: { entry: 61, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kt", text: "restored text" }),
			]);
		});

		// Review round 19, per-field freshness across a two-fresh tie: two
		// aliases of one identity can each fold a fresh input — one carrying
		// real text, one only a status. Both duplicates then carry fresh
		// participation, and the item-granular tiebreak let the later
		// duplicate's OLDER-inherited text overwrite the earlier duplicate's
		// fresh text. Freshness resolves per field: the text only one side's
		// fresh input supplied survives, the status the other side supplied
		// survives, and everything else falls to the later-wins tiebreak.
		it("two fresh-folded duplicates keep each side's fresh-supplied fields", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "pa",
									turnId: "rt",
									type: "agentMessage",
									text: "fresh",
									transcriptKey: "kk",
									position: { entry: 70, item: 0 },
									status: "completed",
								},
								// The second retained alias carries ONLY a status — its
								// text is the omitted marker, so it supplies no text.
								markItemTextOmitted({
									id: "pb",
									turnId: "rt",
									type: "agentMessage",
									text: "",
									status: "completed",
									position: { entry: 71, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues both retained aliases keyless — each holding
			// its own STALE text — and the second one already keyed: both
			// folds land, leaving two duplicates of the kk identity, the
			// fresh-text one ordered first.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "pa",
									turnId: "pq",
									type: "agentMessage",
									text: "old A",
									position: { entry: 70, item: 0 },
								},
								{
									id: "pb",
									turnId: "pq",
									type: "agentMessage",
									text: "old B",
									transcriptKey: "kk",
									position: { entry: 71, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kk", text: "fresh", status: "completed" }),
			]);
		});

		// Review round 20, null is not supplied: the raw field merges with the
		// nullish fallback — a fresh leaf carrying raw: null contributes
		// NOTHING (the merged item inherits the older side's raw) — but the
		// fresh-supplied field set counted any non-undefined value, so the
		// inherited STALE metadata received fresh precedence and overwrote
		// the later duplicate's own raw. Field participation must follow the
		// merge's own presence rules: null is absent for the nullish-fallback
		// fields.
		it("a fresh null raw field does not promote inherited stale metadata", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								// A status-bearing retained fragment whose raw is NULL —
								// it supplies a status but no raw of its own.
								markItemTextOmitted({
									id: "s1",
									turnId: "rt",
									type: "commandExecution",
									callId: "c9",
									text: "",
									status: "completed",
									raw: null,
									transcriptKey: "kk",
									position: { entry: 50, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues the fragment's keyless alias — carrying STALE
			// raw metadata the null-raw fragment cannot displace — followed by
			// a keyed sibling carrying the RESTORED metadata. The alias fold
			// inherits the stale raw; the reconciliation must let the later
			// sibling's own raw stand.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "s1",
									turnId: "pq",
									type: "commandExecution",
									callId: "c9",
									raw: { stale: "yes" },
									position: { entry: 50, item: 0 },
								},
								{
									id: "t2",
									turnId: "pq",
									type: "commandExecution",
									callId: "c8",
									raw: { restored: "yes" },
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kk", raw: { restored: "yes" } }),
			]);
		});

		// Review round 20, the recheck: a fold can GROW its item's identity —
		// a keyless older alias takes the transcript key of the reissue it
		// folded — so the merged result can identity-match an accumulated
		// candidate the original did not. Ordered {id b}, {id a, tk k},
		// {id b, tk k}: the third folds into the first, the merged pair gains
		// the key, and the second — already accumulated — is left beside it
		// as a second item of one identity. The reconciliation must keep
		// folding the merged result until no accumulated candidate matches.
		it("a three-item alias bridge reconciles to one item", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "k", text: "k row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								{
									id: "b",
									turnId: "rt",
									type: "agentMessage",
									text: "retained b",
									position: { entry: 20, item: 0 },
									status: "completed",
								},
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page carries the bridge: a bare {id b} alias, a keyed {id a,
			// tk k} item, and the {id b, tk k} reissue. The reissue folds into
			// the bare alias (the first id match) and the fold GAINS the key —
			// which the keyed item ahead of it also carries.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "b",
									turnId: "pq",
									type: "agentMessage",
									text: "stale b",
									position: { entry: 40, item: 0 },
									status: "completed",
								},
								{
									id: "a",
									turnId: "pq",
									type: "agentMessage",
									text: "alpha a",
									transcriptKey: "k",
									position: { entry: 41, item: 0 },
									status: "completed",
								},
								{
									id: "b",
									turnId: "pq",
									type: "agentMessage",
									text: "stale b2",
									transcriptKey: "k",
									position: { entry: 42, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "k", text: "retained b" }),
			]);
		});

		// Review round 21, provenance order: the per-field reconciliation
		// keeps the later duplicate's corrected output, but the provenance
		// recorded the fold in item-level freshness order — the stale alias
		// ahead of the duplicate that won the field — so the tool-result
		// fold's reversed candidate walk read the stale source first, and a
		// result fragment omitting output folded the STALE output back in.
		// The provenance must record in list order: a side's leaves read, in
		// the fold's walk, in the same precedence the resolution used.
		it("a corrected call output survives the result fold after reconciliation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								// A status-only retained CALL fragment: it supplies a
								// status but no output of its own.
								markItemTextOmitted({
									id: "item_tool_9",
									turnId: "rt",
									type: "commandExecution",
									callId: "c9",
									text: "",
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 50, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues the fragment's keyless alias — carrying the
			// STALE output — followed by a keyed sibling carrying the
			// corrected one, and a result fragment for the call that OMITS
			// output. The reconciliation keeps the corrected output; the
			// result fold must not read the stale alias first and restore it.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "item_tool_9",
									turnId: "pq",
									type: "commandExecution",
									callId: "c9",
									output: "stale output",
									exitCode: 1,
									status: "failed",
									position: { entry: 50, item: 0 },
								},
								{
									id: "item_tool_8",
									turnId: "pq",
									type: "commandExecution",
									callId: "c9",
									output: "corrected output",
									exitCode: 0,
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 51, item: 0 },
								},
								{
									id: "item_tool_result_1",
									turnId: "pq",
									type: "commandExecution",
									callId: "c9",
									status: "completed",
									position: { entry: 52, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ callId: "c9", output: "corrected output", exitCode: 0 }),
			]);
		});

		// Review round 22, explicit clears: the spread-merged fields follow
		// property PRESENCE — a fresh leaf's own undefined CLEARS the field,
		// the spread keeping the clear — but the fresh-supplied field set
		// treated explicit undefined as omission for every field, so a fresh
		// fragment explicitly clearing its transcript entry index lost the
		// clear at reconciliation: the later stale duplicate's index came
		// back. Presence, not value, decides the spread-merged fields.
		it("a fresh fragment's explicit clear survives alias reconciliation", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								// A status-bearing retained fragment whose EXPLICIT
								// undefined transcript entry index clears the field.
								markItemTextOmitted({
									id: "f9",
									turnId: "rt",
									type: "agentMessage",
									text: "",
									status: "completed",
									transcriptKey: "kk",
									transcriptEntryIndex: undefined,
									position: { entry: 50, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues the fragment's keyless alias — carrying a STALE
			// entry index the explicit clear must displace — followed by a
			// keyed sibling carrying the restored text and ANOTHER index. The
			// alias fold keeps the clear; the reconciliation must keep it too,
			// not let the sibling's index back in.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "f9",
									turnId: "pq",
									type: "agentMessage",
									text: "stale text",
									transcriptEntryIndex: 5,
									position: { entry: 50, item: 0 },
									status: "completed",
								},
								{
									id: "g2",
									turnId: "pq",
									type: "agentMessage",
									text: "restored text",
									transcriptEntryIndex: 7,
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			const merged = after.turns.find((turn) => turn.id === "rt")?.items ?? [];
			expect(merged).toEqual([
				expect.objectContaining({ transcriptKey: "kk", text: "restored text", status: "completed" }),
			]);
			expect(merged[0]?.transcriptEntryIndex).toBeUndefined();
		});

		// Review round 23, clears are content: the identity-only check read
		// every undefined-valued property as absence, but the spread-merged
		// fields follow property PRESENCE — a fragment's own undefined is an
		// explicit CLEAR the merge keeps, so it is content. With no status or
		// other payload, the clearing fragment was classified identity-only,
		// the fresh-supplied walk skipped it entirely, and the reconciliation
		// restored the later duplicate's stale index. (The round-22 test
		// misses this: its fragment carries a status.)
		it("a clear-only fragment is not identity-only", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								// A retained fragment carrying ONLY an explicit clear —
								// its text omitted, no status, no payload field.
								markItemTextOmitted({
									id: "f9",
									turnId: "rt",
									type: "agentMessage",
									text: "",
									transcriptKey: "kk",
									transcriptEntryIndex: undefined,
									position: { entry: 50, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues the fragment's keyless alias — carrying a STALE
			// entry index — followed by a keyed sibling carrying the restored
			// text and ANOTHER index. The alias fold keeps the clear; the
			// reconciliation must keep it too.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "f9",
									turnId: "pq",
									type: "agentMessage",
									text: "stale text",
									transcriptEntryIndex: 5,
									position: { entry: 50, item: 0 },
									status: "completed",
								},
								{
									id: "g2",
									turnId: "pq",
									type: "agentMessage",
									text: "restored text",
									transcriptEntryIndex: 7,
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const after = store.getState().conversation!;
			const merged = after.turns.find((turn) => turn.id === "rt")?.items ?? [];
			expect(merged).toEqual([expect.objectContaining({ transcriptKey: "kk", text: "restored text" })]);
			expect(merged[0]?.transcriptEntryIndex).toBeUndefined();
		});

		// Review round 23, the unchanged reread: when the retained side
		// contributes nothing, the merge returns the freshly hydrated items
		// directly — new objects with no recorded fold — so an identical
		// reread right after an alias fold replaced the carrier that held
		// the recorded ancestry. The bound then trimmed the turn whose only
		// visible row named the folded-from alias. The retained ancestry
		// transfers to the identity-matching fresh items on that path. Both
		// reads hydrate through the wire: the fresh items match the folded
		// carrier field for field, which is exactly what makes the retained
		// side contribute nothing.
		it("an identical reread keeps the retained alias ancestry", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			const read = () => ({
				conversation: makeConversation({
					items: [{ kind: "user" as const, id: "fa", text: "fa row" }],
					turns: hydrateThread(
						{
							thread: makeThread({
								turns: [
									makeTurn({
										id: "rt",
										items: [
											{
												id: "fb",
												turnId: "rt",
												type: "agentMessage",
												text: "alias restored",
												status: "completed",
												transcriptKey: "kk",
												position: { entry: 70, item: 0 },
											},
										],
									}),
								],
							}),
						},
						"ref-1",
						0,
					).turns,
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			});
			service.readProjectionResult = read();
			await store.getState().openProjected(service, "ref-1");

			// A row-less page reissues the alias under fa's bare id; the fold
			// leaves the turn backed by the fa row through the recorded
			// ancestry — the item's own identity names no row.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "fa",
									transcriptKey: "kk",
									turnId: "pq",
									type: "agentMessage",
									text: "page fa",
									position: { entry: 70, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 700, outputTokens: 70 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const folded = store.getState().conversation?.turns.find((t) => t.id === "rt")?.items ?? [];
			expect(folded).toEqual([expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "alias restored" })]);

			// An IDENTICAL reread — no live delta between, nothing for the
			// retained side to contribute — must keep the turn through the
			// ancestry the fold recorded, not lose it to the fresh objects.
			service.readProjectionResult = read();
			await store.getState().rehydrate(service);
			const after = store.getState().conversation!;
			expect(after.turns.find((turn) => turn.id === "rt")?.items ?? []).toEqual([
				expect.objectContaining({ id: "fb", transcriptKey: "kk", text: "alias restored" }),
			]);
		});

		// Review round 24, the rank rule: a fresh leaf's status only reaches
		// the merge when it WINS the rank chain — an inProgress fragment
		// folding into a failed alias leaves the alias's failure in place —
		// but the fresh-supplied field set counted any defined status, so the
		// INHERITED failure rode fresh precedence over a later duplicate's
		// completed status. Status counts as supplied only when a fresh
		// leaf's own status is what the merge kept.
		it("an inProgress fragment does not promote an inherited failed status", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			service.readProjectionResult = {
				conversation: makeConversation({
					threadId: "thread-1",
					instanceId: "instance-1",
					usage: null,
					items: [{ kind: "user" as const, id: "kk", text: "kk row" }],
					turns: [
						{
							id: "rt",
							status: "completed",
							items: [
								// An in-progress retained fragment: its rank cannot beat
								// the failed alias it folds into, so the merge keeps the
								// alias's failure.
								markItemTextOmitted({
									id: "f9",
									turnId: "rt",
									type: "agentMessage",
									text: "",
									status: "inProgress",
									transcriptKey: "kk",
									position: { entry: 50, item: 0 },
								}),
							],
						},
					],
					olderCursor: "c1",
				}),
				olderCursor: "c1",
			};
			await store.getState().openProjected(service, "ref-1");

			// The page reissues the fragment's keyless alias — carrying the
			// FAILED status the in-progress fragment cannot displace —
			// followed by a keyed sibling carrying the restored text and a
			// COMPLETED status. The alias fold keeps the failure; the
			// reconciliation must let the sibling's completed status stand.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"pq",
							[
								{
									id: "f9",
									turnId: "pq",
									type: "agentMessage",
									text: "stale text",
									status: "failed",
									position: { entry: 50, item: 0 },
								},
								{
									id: "g2",
									turnId: "pq",
									type: "agentMessage",
									text: "restored text",
									status: "completed",
									transcriptKey: "kk",
									position: { entry: 51, item: 0 },
								},
							],
							{ inputTokens: 70, outputTokens: 7 },
						),
					],
					"c2",
				),
				nextCursor: "c2",
			};
			await store.getState().loadOlder(service);
			const afterRound24 = store.getState().conversation!;
			expect(afterRound24.turns.find((turn) => turn.id === "rt")?.items).toEqual([
				expect.objectContaining({ transcriptKey: "kk", text: "restored text", status: "completed" }),
			]);
		});

		// Review round 25, the model-only publish: a completion's full view
		// repopulates a compacted turn's entire payload through the reducer,
		// and the row-applier paths that bail publish the model half WITHOUT
		// the retention bound — no display row backs the payloads, and on the
		// open() compatibility path no reread is scheduled to clean up, so
		// they stay resident indefinitely. A model-only publish bounds against
		// the rows it keeps: the payloads trim again while the turn's usage
		// survives.
		it("a completion repopulating a compacted turn stays bounded", async () => {
			const service = new FakeConversationService();
			const store = createConversationStore();
			// The fresh window is exactly full (500 model-backed rows in ft) and
			// rt sits before it, so the row a full-view completion repopulates
			// lands at the oldest end — out of the keep-window the moment the
			// cap runs. Under the cutover the rows reproject from the model on
			// the completion, so the payload is backed only while its row is
			// retained; the bound must trim what the cap evicted.
			service.readProjectionResult = {
				conversation: makeConversation(
					{
						threadId: "thread-1",
						instanceId: "instance-1",
						usage: null,
						items: Array.from({ length: 500 }, (_, j) => ({
							kind: "user" as const,
							id: `f-${j}`,
							text: `f-${j}`,
						})),
						turns: [
							{
								id: "rt",
								status: "completed",
								items: [
									{
										id: "rb",
										transcriptKey: "kr",
										turnId: "rt",
										type: "agentMessage",
										text: "original",
										position: { entry: 90, item: 0 },
										status: "completed",
									},
								],
								usage: { inputTokens: 10, outputTokens: 1 },
							},
							{
								id: "ft",
								status: "completed",
								items: Array.from({ length: 500 }, (_, j) => ({
									type: "userMessage" as const,
									id: `f-${j}`,
									turnId: "ft",
									text: `f-${j}`,
								})),
								usage: { inputTokens: 1, outputTokens: 1 },
							},
						],
						olderCursor: "c0",
					},
					true,
				),
				olderCursor: "c0",
			};
			await store.getState().openProjected(service, "ref-1");

			// An unrelated page load bounds the turns: no row backs rt's item,
			// so rt compacts to identity + usage.
			service.olderItems = {
				turnsPage: turnsPage(
					[
						wireTurnFragment(
							"un",
							[
								{
									id: "un-0",
									transcriptKey: "un",
									turnId: "un",
									type: "agentMessage",
									text: "unrelated",
									position: { entry: 91, item: 0 },
									status: "completed",
								},
							],
							{ inputTokens: 9, outputTokens: 9 },
						),
					],
					"c1",
				),
				nextCursor: "c1",
			};
			await store.getState().loadOlder(service);
			expect(store.getState().conversation?.turns.find((t) => t.id === "rt")?.items).toEqual([]);

			// The turn runs and completes with a FULL view: the reducer
			// repopulates rt's entire payload. The store has no row applier for
			// turn/completed — the model-only publish is the only path — and it
			// must bound what it publishes.
			store.getState().applyNotification(
				historyUpdated({
					turns: [{ id: "rt", itemsView: "default", status: "running" }],
				}),
			);
			store.getState().applyNotification(
				historyUpdated({
					turns: [
						{
							id: "rt",
							itemsView: "full",
							status: "completed",
							usage: { inputTokens: 42, outputTokens: 4 },
						},
					],
					items: [
						{
							id: "rb",
							turnId: "rt",
							type: "agentMessage",
							text: "the full payload",
							transcriptKey: "kr",
							position: { entry: 90, item: 0 },
							status: "completed",
						},
					].map((it) => ({ ...it, turnId: it.turnId ?? "rt" })),
				}),
			);
			const after = store.getState().conversation!;
			const settled = after.turns.find((turn) => turn.id === "rt");
			expect(settled?.items).toEqual([]);
			expect(settled?.usage).toEqual({ inputTokens: 42, outputTokens: 4 });
		});

		// Review round 26: the active-turn exemption cannot live only in
		// publishModel — pagination, rehydration and the live capping paths all
		// run the same bound, and an in-flight page can land right after a live
		// steering injection whose item has no display row (or row applier) yet.
		// The page response must not clear the active turn's live items.
	});
});
