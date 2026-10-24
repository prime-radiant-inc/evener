import { nativePlatform } from "./conversationPlatform.testkit";
import { appendFileSync, existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { type ComponentProps, Profiler } from "react";
import { act, type ReactTestInstance, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import WebSocket from "ws";
import { installRetryWireFault } from "../../cmd/evener-hub/frontend/scripts/retryguard/wireFault.mjs";
import { ConnectionProvider, useConnection } from "./ConnectionProvider";
import { NativePreferencesProvider } from "./NativePreferencesProvider";
import { ConversationScreen } from "./screens";
import { TimelineItem } from "./TimelineItem";
import { getNativeMutationRuntime, nativeMutationTargetKey } from "./nativeMutationRuntime";
import { MutationOutboxSQLite } from "./mutationOutboxStorage";
import {
	NativeConversationSessionsProvider,
	useNativeConversationSessions,
} from "./session/NativeConversationSessionsProvider";
import type { NativeConversationSessions, NativeConversationSession } from "./session/nativeConversationSessions";
import { drafts, nativeDrafts } from "./nativeDrafts";
import { builtinComposerItems } from "./composerCommand";
import { commandHosts } from "./session/CommandsSheet";
import { sheetKey } from "./sheet/sheetHosts";
import type { SqliteSync } from "./sqliteSync";
import { openSqliteSyncDouble } from "./sqliteSync.testkit";
import { pressable, render, textOf, unmountMountedTrees, until, untilRendered } from "./renderNative.testkit";

const nav = vi.hoisted(() => ({
	state: { index: 0, routes: [{ key: "retry", name: "Conversation" }] },
}));
vi.mock("@react-navigation/elements", () => ({ useHeaderHeight: () => 64 }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => void | (() => void)) => useEffect(effect, []),
		useNavigationState: <T,>(select: (state: typeof nav.state) => T) => select(nav.state),
		usePreventRemove: () => {},
	};
});

interface JourneyScenario {
	ref: string;
	mode: string;
}
interface JourneyFixture {
	origin: string;
	token: string;
	storageDir: string;
	milestonePath: string;
	scenarios: JourneyScenario[];
}

/** The driver's only cross-process handshake: a milestone is this test's own
 * assertion, written after it has observed the named state in the real tree. */
function milestone(path: string, name: string, detail: Record<string, unknown> = {}) {
	appendFileSync(path, `${JSON.stringify({ milestone: name, at: new Date().toISOString(), detail })}\n`);
}

// This file is run by TestRetryContinuationJourney, which owns the real hub,
// daemon and external scripted provider. Default unit runs need no servers.
// The one-owner journeys use the shipped screen, session provider, real
// conversation store/service, durable runtime and SQLite outbox underneath;
// the hub is real, with a scripted external provider and WebSocket faults.
it.skipIf(!process.env.EVENER_RETRY_JOURNEY)(
	"continues a real failed turn through the native error-row action",
	async () => {
		const fixture = JSON.parse(readFileSync(process.env.EVENER_RETRY_JOURNEY ?? "", "utf8")) as JourneyFixture;
		const scenario = fixture.scenarios[0];
		if (!scenario) throw new Error("missing journey scenario");
		// React Native's WebSocket accepts upgrade headers as its third argument.
		class NativeSocket extends WebSocket {
			constructor(url: string, _protocols: unknown, options: WebSocket.ClientOptions) {
				super(url, options);
				this.addEventListener("close", () => {
					// After the real acceptance is dropped, withhold replay replies so
					// a remount can inspect the unresolved row before settlement.
					if (scenario.mode === "remount-unresolved" && wire.dropped > 0) wire.holdResponses("turn/start");
				});
			}
		}
		vi.stubGlobal("WebSocket", NativeSocket);
		// Command selection focuses the field on the next native frame. As in
		// the screen unit fixture, execute that platform callback immediately.
		vi.stubGlobal("requestAnimationFrame", (frame: (time: number) => void) => {
			frame(0);
			return 0;
		});
		const wire = installRetryWireFault();
		let connection: ReturnType<typeof useConnection> | undefined;
		function Probe() {
			connection = useConnection();
			return null;
		}
		const seen = { current: null as NativeConversationSessions | null };
		function RegistryProbe() {
			seen.current = useNativeConversationSessions();
			return null;
		}
		const navigation = {
			isFocused: () => true,
			getState: () => nav.state,
			navigate: vi.fn(),
			push: vi.fn(),
			replace: vi.fn(),
			pop: vi.fn(),
			goBack: vi.fn(),
			setParams: vi.fn(),
			setOptions: vi.fn(),
		} as unknown as ComponentProps<typeof ConversationScreen>["navigation"];
		const routeKeys = ["retry", "retry-b"];
		let auditRetry: (() => void) | undefined;
		const renderedStates: { unresolved: number; unknown: boolean; enabled: number }[] = [];
		function route(index: number): ComponentProps<typeof ConversationScreen>["route"] {
			return {
				key: routeKeys[index] ?? `retry-${index}`,
				name: "Conversation",
				params: { hubId: connection?.activeProfile?.id ?? "", ref: scenario.ref, title: "Session" },
			};
		}
		function Views({ views, front }: { views: number; front: number }) {
			connection = useConnection();
			nav.state = {
				index: front,
				routes: Array.from({ length: views }, (_, index) => ({
					key: routeKeys[index] ?? `retry-${index}`,
					name: "Conversation",
				})),
			};
			return (
				<>
					{Array.from({ length: views }, (_, index) => (
						<ConversationScreen key={routeKeys[index] ?? index} navigation={navigation} route={route(index)} />
					))}
				</>
			);
		}
		function App({ views, front = Math.max(0, views - 1) }: { views: number; front?: number }) {
			return (
				<Profiler id="native-retry" onRender={() => auditRetry?.()}>
					<ConnectionProvider>
						<NativePreferencesProvider>
							<NativeConversationSessionsProvider>
								<Probe />
								<RegistryProbe />
								<Views views={views} front={front} />
							</NativeConversationSessionsProvider>
						</NativePreferencesProvider>
					</ConnectionProvider>
				</Profiler>
			);
		}
		const composers = (tree: ReactTestRenderer) =>
			tree.root.findAll((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Message");
		const composer = (tree: ReactTestRenderer) => composers(tree)[0];
		const retryButtons = (tree: ReactTestRenderer) =>
			tree.root.findAll(
				(node) => String(node.type) === "Pressable" && (node.props.accessibilityLabel ?? textOf(node)) === "Retry",
			);
		// The action's owner is the failure row for the named turn, not
		// whichever Retry Pressable happens to come first in the tree. A
		// transition can leave an older failure's row mounted while a newer
		// turn is canonical; pressing that row would correctly be refused.
		const retryButtonForTurn = (tree: ReactTestRenderer, turnId: string) => {
			for (const row of tree.root.findAllByType(TimelineItem)) {
				const item = row.props.item as { kind?: string; turnId?: string };
				if (item.kind !== "failure" || item.turnId !== turnId) continue;
				const button = row.findAll(
					(node: ReactTestInstance) =>
						String(node.type) === "Pressable" && (node.props.accessibilityLabel ?? textOf(node)) === "Retry",
				)[0];
				if (button) return button;
			}
			return undefined;
		};
		const sendButtons = (tree: ReactTestRenderer) =>
			tree.root.findAll(
				(node) =>
					String(node.type) === "Pressable" &&
					["Send", "Send and resume"].includes((node.props.accessibilityLabel ?? textOf(node)) as string),
			);
		const pressButton = async (button: ReactTestInstance) => {
			expect(button.props.accessibilityState?.disabled).not.toBe(true);
			await act(async () => button.props.onPress());
		};
		const activeEntry = (hubId: string): NativeConversationSession => {
			const entry = seen.current?.entryFor(hubId, scenario.ref);
			if (!entry) throw new Error("the shared conversation entry was not attached");
			return entry;
		};
		// The unresolved submission may already have moved from the outbox to the
		// accepted optimistic projection while its response is held. Both are the
		// same durable identity; assert that identity rather than an unheld outbox
		// row that a fast hub can settle before the first observation.
		const durableRows = async (hubId: string) => {
			const rows = await getNativeMutationRuntime().read(nativeMutationTargetKey(hubId, scenario.ref));
			return [...rows.outbox, ...rows.optimistic];
		};
		const waitFor = async (check: () => Promise<void> | void, timeout = 10000) => {
			await act(async () => {
				await vi.waitFor(check, { timeout });
			});
		};
		const waitDurable = (hubId: string, count: number) =>
			waitFor(async () => expect(await durableRows(hubId)).toHaveLength(count));
		const openProvider = async () => {
			await untilRendered(() => expect(connection?.loading).toBe(false));
			await act(async () => {
				await connection?.saveHub({ name: "retry fixture", origin: fixture.origin, token: fixture.token });
			});
			await untilRendered(() => expect(connection?.state).toBe("ready"));
			const hubId = connection?.activeProfile?.id;
			if (!hubId) throw new Error("hub profile was not selected");
			return hubId;
		};
		const stageDraft = async (hubId: string) => {
			const document = drafts.open({ hubId, sessionRef: scenario.ref });
			await act(async () => {
				document.edit("RETRY_UNSENT_DRAFT");
				document.addImage({ id: "journey-image", marker: 1, mediaType: "image/png", name: "draft.png", data: "AQID" });
				// Native selections are inserted invocations, not browser chips.
				// Use the screen's real sheet host, exactly as CommandsSheet does.
				const host = commandHosts.get(sheetKey(hubId, scenario.ref));
				if (!host) throw new Error("native command host unavailable");
				const selected = builtinComposerItems(host.session).find((item) => item.key === "builtin:tasks");
				if (!selected) throw new Error("native tasks command unavailable");
				host.choose(selected.invocation);
			});
			expect(document.getSnapshot().record.draft).toBe("/tasks RETRY_UNSENT_DRAFT[image 1]");
			expect(document.getSnapshot().record.images).toHaveLength(1);
			return document;
		};
		const expectDraftIntact = (document: ReturnType<typeof drafts.open>) => {
			const record = document.getSnapshot().record;
			expect(record.draft).toBe("/tasks RETRY_UNSENT_DRAFT[image 1]");
			expect(record.images?.map((image) => image.id)).toEqual(["journey-image"]);
			expect(record.unconfirmed).toBeNull();
			const destination = { hubId: connection?.activeProfile?.id ?? "", sessionRef: scenario.ref };
			expect(nativeDrafts().read(destination)).toEqual(record);
			expect(nativeDrafts().imageInputs(destination, record.images ?? [])).toEqual([
				{ marker: 1, mediaType: "image/png", name: "draft.png", data: "AQID" },
			]);
		};
		const expectCanonicalSuccessor = async (entry: NativeConversationSession, failedTurnId: string) => {
			await untilRendered(() => {
				const canonical = entry.store.getState().canonicalSession();
				const latest = canonical?.turns.at(-1);
				expect(latest?.id).toBeDefined();
				expect(latest?.id).not.toBe(failedTurnId);
				expect(latest?.status).toBe("completed");
				expect(canonical?.turns.some((turn) => turn.id === failedTurnId)).toBe(true);
			});
			const canonical = entry.store.getState().canonicalSession();
			if (!canonical) throw new Error("canonical successor unavailable");
			const successor = canonical.turns.at(-1);
			if (!successor) throw new Error("canonical successor missing");
			milestone(fixture.milestonePath, "canonical-settled", { turnIds: canonical.turns.map((turn) => turn.id) });
			return successor.id;
		};
		const expectNoUnsafeRender = () => {
			expect(renderedStates.filter((state) => (state.unresolved > 0 || state.unknown) && state.enabled > 0)).toEqual(
				[],
			);
			if (["two-views", "remount-unresolved"].includes(scenario.mode))
				expect(renderedStates.some((state) => state.unresolved > 0)).toBe(true);
		};
		try {
			let tree!: ReactTestRenderer;
			await act(async () => {
				tree = render(<App views={0} />);
			});
			const hubId = await openProvider();
			const entry = () => activeEntry(hubId);
			const recordRetryState = () => {
				const current = seen.current?.entryFor(hubId, scenario.ref);
				const db = nativePlatform.sqlitePorts.get("evener-mutations.db") as SqliteSync | undefined;
				if (!current || !db) return;
				// Read the stored tables directly, independently of the projection's
				// readiness/pending selectors, and inspect the actual native controls.
				const rows = db.getAllSync(
					"SELECT client_mutation_id FROM mutation_outbox WHERE target_ref = ? UNION ALL SELECT client_mutation_id FROM mutation_optimistic WHERE target_ref = ?",
					current.key,
					current.key,
				);
				const state = current.store.getState();
				renderedStates.push({
					unresolved: rows.length,
					unknown: state.canonicalSession() === null || !state.pendingMutationsReady,
					enabled: retryButtons(tree).filter(
						(button) => !button.props.disabled || button.props.accessibilityState?.disabled !== true,
					).length,
				});
			};
			auditRetry = recordRetryState;
			const failedTurnId = () => {
				const canonical = entry().store.getState().canonicalSession();
				const latest = canonical?.turns.at(-1);
				if (!latest) throw new Error("the canonical failed turn is not ready");
				return latest.id;
			};
			const mount = async (views: number) => {
				await act(async () => {
					tree.update(<App views={views} />);
				});
				await untilRendered(() => expect(retryButtons(tree).length).toBeGreaterThan(0));
				await untilRendered(() => {
					const ready = seen.current?.entryFor(hubId, scenario.ref);
					expect(ready?.store.getState().canonicalSession()).not.toBeNull();
					expect(ready?.store.getState().pendingMutationsReady).toBe(true);
				});
			};
			const expectInactive = () => {
				for (const button of retryButtons(tree)) {
					expect(button.props.disabled).toBe(true);
					expect(button.props.accessibilityState?.disabled).toBe(true);
				}
			};
			// A press must land on the owners' stable answer. Under load a storage
			// notification or a new-turn frame can move the entry through the
			// readiness gap where activation correctly refuses, leaving no durable
			// row. Wait for the shared entry to certify both reads and for the
			// current failure turn's own Pressable to offer the action before every
			// Retry press.
			const waitRetryReady = async (turnId: string) => {
				await waitFor(() => {
					const state = entry().store.getState();
					expect(state.latestReadState).toBe("ready");
					expect(state.pendingMutationsReady).toBe(true);
					expect(entry().service).not.toBeNull();
					expect(state.canonicalSession()?.turns.at(-1)?.id).toBe(turnId);
					const button = retryButtonForTurn(tree, turnId);
					expect(button).toBeDefined();
					expect(button?.props.disabled).toBe(false);
					expect(button?.props.accessibilityState?.disabled).not.toBe(true);
				});
			};
			const expectSettled = async () => {
				await waitFor(async () => {
					const rows = await getNativeMutationRuntime().read(nativeMutationTargetKey(hubId, scenario.ref));
					expect(rows).toEqual({ outbox: [], optimistic: [], recovery: [] });
				});
			};
			const remount = async () => {
				const previous = entry();
				await act(async () => tree.unmount());
				expect(previous.retired).toBe(true);
				auditRetry = undefined;
				await act(async () => {
					tree = render(<App views={0} />);
				});
				auditRetry = recordRetryState;
				await untilRendered(() => expect(connection?.loading).toBe(false));
				// The real app restores a saved location. This test has no navigator
				// writing one, so explicitly choose the persisted profile, not a new hub.
				await act(async () => connection?.selectHub(hubId));
				await untilRendered(() => expect(connection?.state).toBe("ready"));
				await act(async () => tree.update(<App views={1} />));
				expect(entry()).not.toBe(previous);
				return entry();
			};

			if (scenario.mode === "two-views") {
				await mount(2);
				const document = await stageDraft(hubId);
				// Both mounted views borrow the one registry entry, one store and
				// one target registration.
				await untilRendered(() => expect(entry().attachmentCount).toBe(2));
				expect(entry().liveHost).not.toBeNull();
				const attachments = tree.root
					.findAll((node) => node.props.attachment?.entry)
					.map((node) => node.props.attachment);
				expect(attachments).toHaveLength(2);
				expect(attachments[0]).not.toBe(attachments[1]);
				for (const handle of attachments) {
					expect(handle.entry).toBe(entry());
					expect(handle.entry.store).toBe(entry().store);
					expect(handle.entry.service).toBe(entry().service);
					expect(handle.entry.activitySink).toBe(entry().activitySink);
				}
				// Only the in-front view may offer the action; both views read the
				// same store beneath it.
				expect(retryButtons(tree)).toHaveLength(1);
				const failed = failedTurnId();
				const stale = retryButtonForTurn(tree, failed)!.props.onPress;
				await act(async () => tree.update(<App views={2} front={0} />));
				const otherPress = retryButtonForTurn(tree, failed)!.props.onPress;
				wire.mode = "normal";
				wire.armed = true;
				wire.holdResponses("turn/start");
				wire.holdResponses("thread/read");
				wire.holdNotifications("history/updated", "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end");
				await waitRetryReady(failed);
				await pressButton(retryButtonForTurn(tree, failed)!);
				await waitFor(() => expect(wire.requests).toHaveLength(1));
				const heldIdentity = wire.requests[0]?.params.clientMutationId;
				await waitDurable(hubId, 1);
				const [row] = await durableRows(hubId);
				expect(row?.payload.input).toEqual([{ type: "text", text: "continue" }]);
				expect(wire.requests[0]?.params.input).toEqual([{ type: "text", text: "continue" }]);
				expect(row?.clientMutationId).toBe(heldIdentity);
				expectDraftIntact(document);
				// While the acceptance reply is held, both views report the shared
				// unresolved own submission; repeated and background presses add
				// no second identity.
				await untilRendered(() => {
					expect(retryButtons(tree)).toHaveLength(1);
					expectInactive();
				});
				await act(async () => otherPress());
				await act(async () => tree.update(<App views={2} front={1} />));
				expect(retryButtons(tree)).toHaveLength(1);
				expectInactive();
				await act(async () => stale());
				expect(wire.requests).toHaveLength(1);
				await waitDurable(hubId, 1);
				milestone(fixture.milestonePath, "retry-held", { clientMutationId: row?.clientMutationId });
				wire.releaseResponses("turn/start");
				wire.releaseResponses("thread/read");
				wire.releaseNotifications();
				await untilRendered(() =>
					expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
						"RETRY_DONE_two-views",
					),
				);
				// A stale callback captured before settlement re-reads the shared
				// owners and submits nothing; only the one continuation remains.
				act(() => stale());
				await waitFor(() => expect(wire.requests).toHaveLength(1));
				await expectSettled();
				const successor = await expectCanonicalSuccessor(entry(), failed);
				expect(successor).not.toBe(failed);
				expectDraftIntact(document);
				expectNoUnsafeRender();
				return;
			}

			if (scenario.mode === "latest-only") {
				await mount(1);
				const failed = failedTurnId();
				const stale = retryButtonForTurn(tree, failed)!.props.onPress;
				wire.mode = "normal";
				wire.armed = true;
				wire.holdResponses("thread/read");
				wire.holdNotifications("history/updated", "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end");
				// A newer ordinary turn starts, with the provider holding its reply.
				await act(async () => composer(tree).props.onChangeText("RETRY_RUNNING_NEW"));
				await pressButton(sendButtons(tree)[0]!);
				await untilRendered(() => {
					const conversation = entry().store.getState().conversation;
					const running = conversation?.runningTurnId;
					expect(running).toBeTruthy();
					expect(running).not.toBe(failed);
					// Real status/started frames carry the new ID, while transport
					// holds keep its history and overlay items out of the model.
					expect(conversation?.turns.find((turn) => turn.id === running)?.items ?? []).toHaveLength(0);
					expect(entry().store.getState().canonicalSession()).toBeNull();
					expectInactive();
				});
				await act(async () => stale());
				expect(wire.requests).toHaveLength(1);
				milestone(fixture.milestonePath, "running-empty", {});
				wire.releaseResponses("thread/read");
				wire.releaseNotifications();
				await untilRendered(() =>
					expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
						"RETRY_RUNNING_DONE",
					),
				);
				// The successful newer turn leaves the older failure readable but
				// without its action.
				await untilRendered(() => expect(retryButtons(tree)).toHaveLength(0));
				await expectCanonicalSuccessor(entry(), failed);
				await untilRendered(() => expect(sendButtons(tree).length).toBeGreaterThan(0));
				// A newer failure offers its own Retry.
				await act(async () => composer(tree).props.onChangeText("RETRY_NEWER_FAIL"));
				await pressButton(sendButtons(tree)[0]!);
				await untilRendered(() => {
					const latest = entry().store.getState().canonicalSession()?.turns.at(-1);
					expect(latest?.id).toBeDefined();
					expect(latest?.status).toBe("failed");
					const button = latest === undefined ? undefined : retryButtonForTurn(tree, latest.id);
					expect(button).toBeDefined();
					expect(button?.props.disabled).toBe(false);
				});
				const newerFailure = failedTurnId();
				expect(newerFailure).not.toBe(failed);
				act(() => stale());
				await waitFor(() => expect(wire.requests).toHaveLength(2));
				// Stage draft text and an image; Retry must leave both untouched.
				const document = await stageDraft(hubId);
				wire.holdResponses("turn/start");
				await waitRetryReady(newerFailure);
				await pressButton(retryButtonForTurn(tree, newerFailure)!);
				await waitFor(() => expect(wire.requests).toHaveLength(3));
				const heldIdentity = wire.requests[2]?.params.clientMutationId;
				expect(heldIdentity).toEqual(expect.any(String));
				expect(wire.requests[2]?.params.input).toEqual([{ type: "text", text: "continue" }]);
				expectDraftIntact(document);
				milestone(fixture.milestonePath, "newest-retry", { clientMutationId: heldIdentity });
				wire.releaseResponses("turn/start");
				await untilRendered(() =>
					expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
						"RETRY_NEWEST_DONE",
					),
				);
				await expectSettled();
				await expectCanonicalSuccessor(entry(), newerFailure);
				expect(new Set(wire.requests.map((request) => request.params.clientMutationId)).size).toBe(3);
				expectDraftIntact(document);
				expectNoUnsafeRender();
				return;
			}

			if (scenario.mode === "remount-unresolved") {
				await mount(1);
				const document = await stageDraft(hubId);
				const failed = failedTurnId();
				const stale = retryButtonForTurn(tree, failed)!.props.onPress;
				wire.mode = "lost-reply";
				wire.armed = true;
				wire.holdResponses("thread/read");
				wire.holdNotifications("history/updated", "overlay/upserted", "overlay/delta", "overlay/reset", "overlay/end");
				await waitRetryReady(failed);
				await pressButton(retryButtonForTurn(tree, failed)!);
				await untilRendered(() => expect(wire.dropped).toBe(1));
				await waitFor(() => expect(wire.requests).toHaveLength(1));
				const heldIdentity = wire.requests[0]?.params.clientMutationId;
				await waitDurable(hubId, 1);
				const [row] = await durableRows(hubId);
				expect(row?.payload.input).toEqual([{ type: "text", text: "continue" }]);
				expect(row?.clientMutationId).toBe(heldIdentity);
				// Neither a fresh provider nor reconnect is allowed to certify
				// loaded-empty while these real durable rows remain unresolved.
				const remounted = await remount();
				expect(remounted.store.getState().canonicalSession()).toBeNull();
				expectInactive();
				await act(async () => stale());
				const beforeRelease = await durableRows(hubId);
				expect(beforeRelease).toHaveLength(1);
				expect(beforeRelease[0]?.clientMutationId).toBe(row?.clientMutationId);
				expectDraftIntact(document);
				milestone(fixture.milestonePath, "remounted-unresolved", { clientMutationId: row?.clientMutationId });
				wire.releaseResponses("turn/start");
				wire.releaseResponses("thread/read");
				wire.releaseNotifications(
					"history/updated",
					"overlay/upserted",
					"overlay/delta",
					"overlay/reset",
					"overlay/end",
				);
				await untilRendered(() =>
					expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
						"RETRY_RECOVERED_REMOUNT",
					),
				);
				await expectSettled();
				expect(wire.dropped).toBe(1);
				expect(wire.connections).toBeGreaterThanOrEqual(2);
				expect(new Set(wire.requests.map((request) => request.params.clientMutationId)).size).toBe(1);
				const successor = await expectCanonicalSuccessor(remounted, failed);
				expectDraftIntact(document);
				const settledEntry = await remount();
				await expectSettled();
				expect(await expectCanonicalSuccessor(settledEntry, failed)).toBe(successor);
				expect(retryButtons(tree)).toHaveLength(0);
				expectDraftIntact(document);
				expectNoUnsafeRender();
				return;
			}

			// Preserve the composed text and saved attachment through acceptance
			// and explicit refusal, before deliberately editing recovery below.
			await mount(1);
			const document = await stageDraft(hubId);
			let refusedIdentity: string | undefined;
			const failed = failedTurnId();
			wire.mode = scenario.mode;
			wire.armed = true;
			await waitRetryReady(failed);
			await pressButton(retryButtonForTurn(tree, failed)!);
			await untilRendered(() => expect(wire.requests.length).toBeGreaterThanOrEqual(1));
			expect(wire.requests[0]?.params.input).toEqual([{ type: "text", text: "continue" }]);
			expectDraftIntact(document);
			if (scenario.mode === "refused") {
				await untilRendered(() => expect(pressable(tree, "Edit")).toBeDefined());
				expect(wire.requests).toHaveLength(1);
				expectDraftIntact(document);
				const rows = await getNativeMutationRuntime().read(nativeMutationTargetKey(hubId, scenario.ref));
				expect(rows.outbox).toHaveLength(0);
				expect(rows.recovery).toHaveLength(1);
				expect(rows.recovery[0]?.clientMutationId).toBe(wire.requests[0]?.params.clientMutationId);
				refusedIdentity = rows.recovery[0]?.clientMutationId;
				// Edit protects an occupied composer. Remove the image and text
				// deliberately, then restore the refused continuation, not the draft.
				await act(async () => document.removeImage("journey-image"));
				await act(async () => composer(tree).props.onChangeText(""));
				await pressButton(pressable(tree, "Edit")!);
				expect(composer(tree).props.value).toBe("continue");
				await pressButton(pressable(tree, "Send")!);
			}
			if (scenario.mode === "lost-reply") {
				await untilRendered(() => {
					expect(wire.dropped).toBe(1);
					expect(wire.connections).toBeGreaterThanOrEqual(2);
					expect(connection?.state).toBe("ready");
				});
			}
			// The native Markdown host receives its text in a prop, not children.
			await untilRendered(() =>
				expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
					`RETRY_DONE_${scenario.mode}`,
				),
			);
			if (scenario.mode === "refused") {
				expect(document.getSnapshot().record.draft).toBe("");
			} else {
				expectDraftIntact(document);
			}
			for (const request of wire.requests) {
				expect(request.params.input).toEqual([{ type: "text", text: "continue" }]);
				expect(request.params.clientMutationId).toEqual(expect.any(String));
			}
			if (scenario.mode === "lost-reply") {
				expect(new Set(wire.requests.map((request) => request.params.clientMutationId)).size).toBe(1);
				const clientMutationId = wire.requests[0]?.params.clientMutationId;
				if (!clientMutationId) throw new Error("missing Retry mutation identity");
				console.log("retry journey: continued output observed before durable settlement check", { clientMutationId });
				const attempts = wire.requests.length;
				const connections = wire.connections;
				await act(async () => wire.disconnect());
				await untilRendered(() => {
					expect(wire.connections).toBeGreaterThan(connections);
					expect(connection?.state).toBe("ready");
					expect(
						wire.reads.some((read) => read.ref === scenario.ref && read.subscribed && read.connection > connections),
					).toBe(true);
				});
				// This is a separate connection to the file the real runtime wrote,
				// not the runtime's cached pending/recovery projection.
				const path = join(fixture.storageDir, "evener-mutations.db");
				expect(existsSync(path)).toBe(true);
				const reopened = openSqliteSyncDouble(path);
				try {
					const storage = new MutationOutboxSQLite(reopened.port);
					await until(async () => {
						const records = {
							outbox: await storage.getOutbox(clientMutationId),
							optimistic: await storage.getOptimistic(clientMutationId),
							recovery: await storage.getRecovery(clientMutationId),
						};
						expect(records, `unsettled durable Retry ${clientMutationId}`).toEqual({
							outbox: undefined,
							optimistic: undefined,
							recovery: undefined,
						});
					});
				} finally {
					reopened.database.close();
				}
				expect(wire.requests).toHaveLength(attempts);
				expect(composer(tree).props.value).toBe("/tasks RETRY_UNSENT_DRAFT[image 1]");
				expectDraftIntact(document);
				expect(tree.root.findAllByType("EnrichedMarkdownText" as never).map((node) => node.props.markdown)).toContain(
					`RETRY_DONE_${scenario.mode}`,
				);
				console.log("retry journey: durable Retry settled after reconnect", { clientMutationId });
			} else {
				expect(wire.requests).toHaveLength(scenario.mode === "refused" ? 2 : 1);
			}
			if (scenario.mode === "refused") {
				// Edit copies recovery into the composer, it does not discard the
				// refused record. The accepted new submission must settle separately.
				await waitFor(async () => {
					const rows = await getNativeMutationRuntime().read(nativeMutationTargetKey(hubId, scenario.ref));
					expect(rows.outbox).toEqual([]);
					expect(rows.optimistic).toEqual([]);
					expect(rows.recovery.map((row) => row.clientMutationId)).toEqual([refusedIdentity]);
				});
			} else await expectSettled();
			await expectCanonicalSuccessor(entry(), failed);
			expectNoUnsafeRender();
		} finally {
			await unmountMountedTrees();
			await getNativeMutationRuntime().stop();
			for (const database of nativePlatform.sqliteDatabases) database.close();
			vi.unstubAllGlobals();
		}
	},
	60000,
);
