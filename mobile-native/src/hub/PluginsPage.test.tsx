// Screen-level tests for the marketplace removal outcomes the browser cannot
// hold on its own: the PluginsPage-owned no-repeat guard fences a
// marketplace the hub says it already removed across browser remounts, the
// cleanup warning survives the browser's own revision fence and tab switches,
// a clean applied removal retires an obsolete cleanup warning, and a late
// outcome from a replaced client changes nothing on the new one. The fence
// covers only the window between an applied outcome and the first trusted
// read after it - presence retires it too (the fallback ruling), so a
// re-registration the wire cannot tell from a stale row can never stay
// fenced forever. The reconnect-recovery suite main added beside it
// (#1915 round-1 gap: a transport flap keeps the same client, so the
// screen's own mount effect never re-runs) shares this file's harness
// and mocks.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import {
	type AnyNotification,
	ErrorMarketplaceRemoveApplied,
	WireError,
	type MarketplaceEntry,
	type PluginEntry,
} from "@evener/appwire-client";
import {
	HUB_WRITE_BUSY,
	HubWriteBusyError,
	MARKETPLACE_REFETCH_DEBOUNCE_MS,
} from "@evener/appwire-client/state/extensions";
import { deferRequest, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { AddMarketplace } from "../MarketplaceBrowser";
import { PluginsPage } from "./PluginsPage";
import { PluginsStack } from "./pluginsStackTestUtils";
import { alertRequests, pressable, render, renderedText, screenConnection } from "../renderNative.testkit";
import { Button, Group, GroupFooter, Row } from "../sheet/Grouped";
import { SearchField } from "../sheet/SearchField";

const harness = vi.hoisted(() => ({
	connection: {} as Record<string, unknown>,
}));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
	SafeAreaView: "SafeAreaView",
}));
vi.mock("../ConnectionProvider", () => ({
	useConnection: () => harness.connection,
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
// The page's focus: each registered effect runs once, as on first showing,
// and refocus() runs them again, as coming back to the page does.
const focus = vi.hoisted(() => ({ effects: new Set<() => void>() }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) =>
			useEffect(() => {
				focus.effects.add(effect);
				effect();
				return () => {
					focus.effects.delete(effect);
				};
			}, [effect]),
	};
});
function refocus() {
	for (const effect of [...focus.effects]) effect();
}

const marketplace: MarketplaceEntry = {
	name: "acme",
	source: { kind: "github", repo: "acme/plugins" },
	lastUpdated: 1,
};

function cloneLitterError(applied: unknown, includeApplied = true): WireError {
	return new WireError("clone cleanup failed", -32603, {
		evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
		...(includeApplied ? { applied } : {}),
	});
}

/** A marketplace hub: every method call is recorded in `methods`, the
 * marketplaces list answers per `options.list`, the Browse segment's catalog is
 * empty, and `evener/marketplace/remove` answers per `options.remove`.
 * `notify` sends a notification to every listener. */
function marketplaceClient(options: {
	list?: () => Promise<{ marketplaces: MarketplaceEntry[] }>;
	remove?: () => Promise<unknown>;
	add?: () => Promise<{ marketplaces: MarketplaceEntry[] }>;
}) {
	const methods: string[] = [];
	const listeners = new Set<(notification: AnyNotification) => void>();
	const client = Object.assign(new FakeClient("ready"), {
		request: async (method: string) => {
			methods.push(method);
			if (method === "evener/marketplace/list") return options.list?.() ?? { marketplaces: [marketplace] };
			if (method === "evener/marketplace/browse") return { name: marketplace.name, plugins: [] };
			if (method === "evener/marketplace/remove") return options.remove?.();
			if (method === "evener/marketplace/add") return options.add?.() ?? { marketplaces: [marketplace] };
			if (method === "evener/plugin/list") return { plugins: [] };
			return { marketplaces: [marketplace] };
		},
		onNotification: (listener: (notification: AnyNotification) => void) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	const notify = (method: string) => {
		for (const listener of [...listeners]) listener({ method, params: {} } as AnyNotification);
	};
	return { client, methods, notify };
}

/** The hub says its marketplaces changed, as it does to every client after
 * any change: the page's store reads the list again once its debounce
 * passes. */
async function marketplacesChanged(hub: { notify(method: string): void }) {
	await act(async () => {
		hub.notify("evener/marketplace/updated");
		await new Promise((resolve) => setTimeout(resolve, MARKETPLACE_REFETCH_DEBOUNCE_MS));
	});
}

function readyConnection(client: ConversationClientLike) {
	return screenConnection(client, "ready");
}

/** The Marketplaces segment's rows for `name`: each reads as the name, then
 * its source. */
function marketplaceRows(tree: ReturnType<typeof render>, name: string) {
	return tree.root.findAll(
		(node) =>
			node.props.accessibilityRole === "button" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${name}, `),
	);
}

function marketplaceRow(tree: ReturnType<typeof render>, name: string) {
	const rows = marketplaceRows(tree, name);
	if (rows.length !== 1) throw new Error(`expected one ${name} row, found ${rows.length}`);
	return rows[0];
}

async function browseMarketplace(tree: ReturnType<typeof render>) {
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
}

/** Presses Retry marketplaces on the top page: a marketplace's page, when
 * one is pushed, leads with its own over the list's underneath. */
function retryMarketplaces(tree: ReturnType<typeof render>) {
	tree.root.findAllByProps({ accessibilityLabel: "Retry marketplaces" }).at(-1)?.props.onPress();
}

async function confirmMarketplaceRemoval(tree: ReturnType<typeof render>) {
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => {
		remove.onPress?.();
		await Promise.resolve();
	});
}

/** The props the mounted screen hands its Plugins child, for driving the
 * screen-level recorders directly: `onAppliedRemoval` is the seam whose
 * return the client-switch tests pin, and `appliedRemovalNames` is the
 * fence as the browser sees it. */
function pluginsProps(tree: ReturnType<typeof render>) {
	const found = tree.root.findAll((node) => typeof node.props?.onAppliedRemoval === "function");
	const props = found.at(-1)?.props as
		| {
				onAppliedRemoval: (
					name: string,
					notice: string | null,
					owner: ConversationClientLike,
					marketplaces: readonly MarketplaceEntry[] | null,
					publicationVersion: number,
				) => boolean;
				onAuthoritativeMarketplaces: (
					marketplaces: readonly MarketplaceEntry[],
					owner: ConversationClientLike,
					publicationVersion: number,
				) => void;
				appliedRemovalNames: ReadonlySet<string>;
		  }
		| undefined;
	if (!props) throw new Error("Plugins child was not rendered");
	return props;
}

beforeEach(() => {
	alertRequests.length = 0;
});

it("re-fences a name a stale presence read cleared after a browser remount", async () => {
	let listCalls = 0;
	let removals = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; the remount's own read
			// answers with the stale row again - the wire's whole-second stamp
			// cannot tell it from a re-registration, and the fallback ruling
			// trusts it as one - and the read after the second removal's outcome
			// fails too, so the re-fence holds to the assertion.
			if (listCalls === 2 || listCalls === 4) throw new Error("list unavailable");
			return { marketplaces: [marketplace] };
		},
		remove: () => {
			removals += 1;
			// The hub answers BOTH removals with the idempotent applied outcome:
			// the removal already stood, so a repeat press never reads as a
			// failed write.
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Leave the Marketplaces segment and come back: the guard lives at the screen, so
	// it survives the browser's death, but the remount's own read answers
	// with the stale row - the first authoritative read after the outcome -
	// and the fallback retires the fence for whatever the trusted read
	// vouches for, so Remove re-enables.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});

	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	// Pressing Remove on the stale row reaches a hub that already applied the
	// removal: the same applied outcome answers, which re-fences the name and
	// re-raises the warning - idempotent, never a failed write.
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");
	const fenced = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(fenced.props.disabled).toBe(true);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("fences the name when a re-registration lands while the confirm dialog is open", async () => {
	let listCalls = 0;
	let landRefresh!: (value: { marketplaces: MarketplaceEntry[] }) => void;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets. A
			// change-notification read stays in flight while the confirm dialog is
			// open and answers with another client's re-registration of acme -
			// a fresh lastUpdated - and the post-removal reconciliation read
			// fails.
			if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
			if (listCalls === 2)
				return new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
					landRefresh = resolve;
				});
			return Promise.reject(new Error("list unavailable"));
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	// The hub says the list changed: the read whose answer another client's refresh has
	// since re-registered, kept in flight while the dialog opens.
	await marketplacesChanged(hub);
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	// The read lands while the confirm dialog is open: acme's registration now
	// carries a fresh identity, not the one the dialog captured.
	await act(async () => {
		landRefresh({ marketplaces: [{ ...marketplace, lastUpdated: 2 }] });
		await Promise.resolve();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => {
		confirm.onPress?.();
		await Promise.resolve();
	});
	await act(async () => {});

	// The removal applied and the post-removal reconciliation read failed, so
	// no fresh list has established what the hub now carries: the guard has to
	// fence acme - Remove stays disabled on the already-removed row - and the
	// cleanup warning still raises.
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");
	expect(hub.methods.filter((method) => method === "evener/marketplace/list")).toHaveLength(3);
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(true);
});

it("does not confirm a removal a fresh read retired while the dialog was open", async () => {
	let listCalls = 0;
	let removals = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read carries the registration the dialog targets; the
			// reconnect recovery read answers with what another client's removal
			// left behind, so every trusted read from there on omits the name.
			if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
			return Promise.resolve({ marketplaces: [] });
		},
		remove: () => {
			removals += 1;
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});

	// The connection flaps while the confirmation is open, and the recovery
	// read that lands once it is ready again no longer carries the name: the
	// marketplace another client removed is gone from the trusted list.
	harness.connection = screenConnection(hub.client, "reconnecting");
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	harness.connection = readyConnection(hub.client);
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	await act(async () => {});
	expect(listCalls).toBe(2);
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => {
		confirm.onPress?.();
		await Promise.resolve();
	});
	await act(async () => {});

	// The confirmation holds the state it opened on, but the store it must
	// answer to no longer carries the name: the removal already stood on the
	// hub, and confirming must not issue the duplicate removal the guard
	// exists to prevent.
	expect(removals).toBe(0);
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
	expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
});

it("keeps the fence when a pre-removal read lands inside the outcome's window", async () => {
	let releaseStaleRead!: (value: { marketplaces: MarketplaceEntry[] }) => void;
	const staleRead = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		// The read's answer still describes the hub before the removal: acme
		// present under its original registration.
		releaseStaleRead = () => resolve({ marketplaces: [marketplace] });
	});
	let releaseRemoval!: () => void;
	const pendingRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; a
			// change-notification read stays on the wire while the removal runs; every
			// later read fails, so nothing fresh ever lands.
			if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
			if (listCalls === 2) return staleRead;
			return Promise.reject(new Error("list unavailable"));
		},
		remove: () => pendingRemoval,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await marketplacesChanged(hub);
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => confirm.onPress?.());

	// The stale read answers while the removal is still in flight, so the
	// revision fence holds it behind the newer write. The write then rejects
	// with the applied outcome - and a rejection that publishes nothing hands
	// ownership back down, publishing the held stale answer a beat BEFORE the
	// outcome's continuation records the fence. That answer predates the fence
	// and must never retire it: only reads issued after the outcome count.
	await act(async () => {
		releaseStaleRead({ marketplaces: [marketplace] });
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {
		releaseRemoval();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(true);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("clears the guard when a fresh read shows the removed name absent", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read establishes its absence; a later
			// change-notification read answers with another client's same-second re-add.
			if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
			return Promise.resolve({ marketplaces: [marketplace] });
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// The reconciliation read established acme's absence, so the guard forgot
	// it: the hub says the list changed, and the read finds another client's
	// re-registration - one the wire cannot even tell from the removed
	// registration, the same lastUpdated - and it has to stay removable.
	await marketplacesChanged(hub);
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears an obsolete cleanup warning when a later applied removal is clean", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read establishes its absence; a later
			// change-notification read answers with another client's re-registration; the
			// read after the second removal's outcome fails, so the clean
			// outcome's own re-fence holds to the assertion.
			if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
			if (listCalls >= 4) return Promise.reject(new Error("list unavailable"));
			return Promise.resolve({ marketplaces: [marketplace] });
		},
		remove: () => {
			removals += 1;
			return removals === 1
				? Promise.reject(cloneLitterError(null, false))
				: Promise.reject(
						new WireError("marketplace removed, but the updated list was unavailable", -32603, {
							evenerErrorInfo: ErrorMarketplaceRemoveApplied,
							appliedUnavailable: true,
						}),
					);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// The reconciliation read established acme's absence, so the guard forgot
	// it: the hub says the list changed, the read finds another client's
	// re-registration, and opening it shows the fence no longer covers the name.
	await marketplacesChanged(hub);
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);

	// Remove it again, and this removal applies CLEANLY: the hub's marker says
	// the unregister and its clone cleanup both landed, so the outcome carries
	// no notice. The earlier clone-cleanup warning is now about a removal this
	// hub fully handled - an obsolete leftover the screen has to retire, or a
	// warning about long-gone litter outlives every later successful removal.
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
	// The clean outcome is still an applied removal - never a failed write.
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
	// And it still fences the name: the applied outcome recorded with the
	// screen's guard whatever its notice said.
	const fenced = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(fenced.props.disabled).toBe(true);
});

it("clears the cleanup warning when a later removal succeeds", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read establishes its absence; a later
			// change-notification read answers with another client's re-registration.
			if (listCalls === 2) return Promise.resolve({ marketplaces: [] });
			return Promise.resolve({ marketplaces: [marketplace] });
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// The reconciliation read established acme's absence, so the guard forgot
	// it: the hub says the list changed, the read finds another client's
	// re-registration, and removing it this time the hub confirms the change
	// outright, the write resolving with nothing left to warn about.
	await marketplacesChanged(hub);
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	// The screen-level warning reports the latest removal outcome, so the
	// successful one retires the obsolete cleanup warning - never a failed
	// write either.
	expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps a marketplace's warning when an unrelated removal succeeds", async () => {
	const beta: MarketplaceEntry = {
		name: "beta",
		source: { kind: "github", repo: "beta/plugins" },
		lastUpdated: 2,
	};
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// Every read carries both marketplaces: the hub's truth still lists
			// acme beside beta, so the rows the flow below presses stay visible.
			return Promise.resolve({ marketplaces: [marketplace, beta] });
		},
		remove: () => {
			removals += 1;
			// acme's removal (first) applies with clone litter; beta's removal
			// (second) the hub confirms outright.
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// A different marketplace's clean removal is a fresh outcome for a
	// different name: acme's litter warning is about clone files the hub
	// could not clean, and beta's success says nothing about them, so the
	// warning has to stay up for its own marketplace.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "beta").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("records an applied removal after selection changes while the request is pending", async () => {
	let releaseRemoval!: () => void;
	const pendingRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; every
			// later read fails, so no authoritative read lands after the outcome.
			if (listCalls === 1) return { marketplaces: [marketplace] };
			throw new Error("list unavailable");
		},
		remove: () => pendingRemoval,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => remove.onPress?.());
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	// Re-open the detail before the outcome settles: the reconciliation read
	// its settlement issues fails, and a failed read hides the rows it cannot
	// vouch for, but the detail already open survives on the list the store
	// retains - its Remove is the fence's own view.
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	releaseRemoval();
	await act(async () => {
		await Promise.resolve();
		await Promise.resolve();
	});

	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	const removeButton = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(removeButton.props.disabled).toBe(true);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("keeps a valid applied list after the old browser is disposed", async () => {
	let listCalls = 0;
	let releaseRemoval!: () => void;
	const pendingRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError({ marketplaces: [] }));
	});
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			if (listCalls === 2) throw new Error("browser B initial list failed");
			return { marketplaces: [marketplace] };
		},
		remove: () => pendingRemoval,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => remove.onPress?.());

	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	releaseRemoval();
	await act(async () => {
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});

	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	expect(listCalls).toBe(2);
	expect(renderedText(tree)).toContain("No marketplaces on this hub.");
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
});

it("shows the failed read's error alone when the retained list is not empty", async () => {
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read answers a non-empty list the store keeps; the
			// change-notification read fails, so the retained rows hide behind
			// the error copy without a fresh list ever replacing them.
			if (listCalls === 1) return { marketplaces: [marketplace] };
			throw new Error("list unavailable");
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("acme");
	await marketplacesChanged(hub);
	await act(async () => {});
	// A failed read keeps the last list in the store: the rows it cannot
	// vouch for hide behind the error and Retry, but the retained list is not
	// empty, so the empty-state copy must not claim the hub has no
	// marketplaces beside the error that says the load failed.
	expect(renderedText(tree)).toContain("Could not load marketplaces.");
	expect(renderedText(tree)).not.toContain("No marketplaces on this hub.");
});

it("reconciles through the remounted browser after the old browser is disposed", async () => {
	let listCalls = 0;
	let releaseRemoval!: () => void;
	const pendingRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			if (listCalls === 2) throw new Error("browser B initial list failed");
			return { marketplaces: [marketplace] };
		},
		remove: () => pendingRemoval,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => remove.onPress?.());

	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	expect(listCalls).toBe(2);
	expect(renderedText(tree)).not.toContain("acme github: acme/plugins");

	releaseRemoval();
	await act(async () => {
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	expect(listCalls).toBe(3);
	expect(renderedText(tree)).toContain("acme");
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const removeButton = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	// The outcome's own reconciliation read is the first authoritative read
	// after it, and it still carries the row the hub re-lists - the fallback
	// ruling trusts it as a re-registration and retires the fence, a press
	// only ever drawing the hub's same idempotent applied outcome.
	expect(removeButton.props.disabled).toBe(false);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("keeps a remounted browser from retiring the fence on the pre-outcome snapshot", async () => {
	let listCalls = 0;
	let releaseRead!: () => void;
	const pendingRead = new Promise<void>((resolve) => {
		releaseRead = () => resolve();
	});
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// outcome's own reconciliation read fails; the remount's first
			// read stays on the wire until the test releases it.
			if (listCalls === 2) throw new Error("list unavailable");
			if (listCalls >= 3) await pendingRead;
			return { marketplaces: [marketplace] };
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// The applied outcome's reconciliation read failed, so the store still
	// carries the pre-removal list. Leave and come back: the remounted
	// browser must not report that retained snapshot as an authoritative
	// read - the fence stays up while the remount's own first read is still
	// on the wire, and the stale row it exposes stays unremovable.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const fenced = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(fenced.props.disabled).toBe(true);

	// The remount's read is the first publication newer than the outcome:
	// the fallback ruling trusts it whatever it carries, and Remove
	// re-enables - a press would only draw the hub's same idempotent
	// applied outcome.
	releaseRead();
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("retires an earlier fence on the read a later outcome's fence outran", async () => {
	// The round-4 M1 shape from #2137's review: a browser-wide watermark
	// advances on ANY outcome's recording, so a read that publishes inside
	// the same window - before any effect can report it - is mistaken for
	// one the earlier fence already predates once the later outcome records
	// against it. Per-name baselines keep the earlier fence's retirement
	// its own, the way the web guard prunes per name.
	const beta: MarketplaceEntry = {
		name: "beta",
		source: { kind: "github", repo: "beta/plugins" },
		lastUpdated: 1,
	};
	let listCalls = 0;
	let removals = 0;
	let releaseAcmeRead!: () => void;
	const acmeRead = new Promise<void>((resolve) => {
		releaseAcmeRead = () => resolve();
	});
	const laterReads = new Promise<void>(() => {});
	let releaseAcmeRemoval!: () => void;
	const acmeRemoval = new Promise<never>((_resolve, reject) => {
		releaseAcmeRemoval = () => reject(cloneLitterError(null, false));
	});
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read carries both rows; acme's reconciliation read is
			// held on the wire until the window below opens it; every later
			// read stays on the wire, so nothing else publishes.
			if (listCalls >= 3) await laterReads;
			if (listCalls === 2) await acmeRead;
			return { marketplaces: [marketplace, beta] };
		},
		remove: () => {
			removals += 1;
			// Both removals answer with the idempotent applied outcome and an
			// unavailable applied list, so each records a fence and asks the
			// store to reconcile.
			return removals === 1 ? acmeRemoval : Promise.reject(cloneLitterError(null, false));
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	releaseAcmeRemoval();
	await act(async () => {
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	const fencedAcme = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(fencedAcme.props.disabled).toBe(true);
	expect(listCalls).toBe(2);

	// Open beta's confirmation while acme's reconciliation read is still on
	// the wire, so the window below can settle beta's outcome before any
	// effect reports the read.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		marketplaceRow(tree, "beta").props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const betaConfirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!betaConfirm?.onPress) throw new Error("Remove confirmation was not shown");

	// The window: acme's reconciliation read publishes - still carrying acme,
	// a row only a re-registration can be once the removal stood - and
	// beta's outcome records against that publication before any effect can
	// report it. A browser-wide watermark would rise to the read's version
	// with beta's recording and swallow acme's retirement read whole;
	// per-name baselines hand the read to acme's fence alone.
	await act(async () => {
		releaseAcmeRead();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		betaConfirm.onPress?.();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// Acme's fence retired on the read beta's recording outran; beta's own
	// fence stands until a read newer than ITS baseline, and its
	// reconciliation read never comes off the wire.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const acmeRemove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(acmeRemove.props.disabled).toBe(false);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		marketplaceRow(tree, "beta").props.onPress();
	});
	await act(async () => {});
	const betaRemove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(betaRemove.props.disabled).toBe(true);
	expect(removals).toBe(2);
});

it("ignores an applied removal result from a replaced client", async () => {
	let releaseOld!: () => void;
	const oldRemoval = new Promise<never>((_resolve, reject) => {
		releaseOld = () => reject(cloneLitterError({ marketplaces: [] }));
	});
	const oldHub = marketplaceClient({ remove: () => oldRemoval });
	const newHub = marketplaceClient({
		list: async () => ({ marketplaces: [] }),
	});
	harness.connection = readyConnection(oldHub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((button) => button.text === "Remove");
	if (!remove?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => remove.onPress?.());

	harness.connection = readyConnection(newHub.client);
	await act(async () => tree.update(<PluginsStack {...props} />));
	await act(async () => {});
	releaseOld();
	await act(async () => {
		await Promise.resolve();
	});

	expect(renderedText(tree)).not.toContain("Marketplace removed; clone cleanup failed");
	expect(newHub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(0);
});

it("leaves ordinary marketplace removal failures retryable", async () => {
	const hub = marketplaceClient({
		remove: async () => {
			throw new Error("ordinary failure");
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);

	expect(renderedText(tree)).toContain("Could not confirm the change. Check its status before trying again.");
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps the guard and warning across a same-client connection flap", async () => {
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; every
			// later read - the post-removal refetch and the store's own reconnect
			// re-read - fails, so no authoritative read lands after the outcome.
			if (listCalls === 1) return { marketplaces: [marketplace] };
			throw new Error("list unavailable");
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// A passive flap moves only `state`: the screen stays mounted behind the
	// banner (connectionDisplay.ts) with the SAME client throughout, and the
	// browser it carries stays on the detail it was showing - the guard and
	// warning have to outlive the store's own reconnect re-read.
	harness.connection = { ...readyConnection(hub.client), state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	harness.connection = readyConnection(hub.client);
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	await act(async () => {});

	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	// The banner kept this browser mounted through the flap (connectionDisplay:
	// everReady already true, the SAME client throughout), so the detail it had
	// open survives with it - the retention #1952 bought - while the store's
	// own reconnect re-read fails, so no authoritative read has landed and
	// the fence still guards the stale row in the retained detail.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Back to Plugins" })).toHaveLength(1);
	expect(renderedText(tree)).toContain("Update source");
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(true);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("records a pending removal settling after a remount whose list read failed", async () => {
	let releaseRemoval!: () => void;
	const pendingRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The remount's own read fails, and so does the reconciliation read
			// the settled outcome issues through the store that survived the
			// unmounted browser: the error copy stays up until the manual retry
			// below answers with the stale row.
			if (listCalls === 2 || listCalls === 3) throw new Error("list unavailable");
			return { marketplaces: [marketplace] };
		},
		remove: () => pendingRemoval,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => confirm.onPress?.());

	// Leave the Marketplaces segment and come back while the removal is still pending:
	// the remounted browser's own list read fails, so no authoritative list
	// has been seen when the outcome settles.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	releaseRemoval();
	await act(async () => {});

	// The guard and warning still land - the screen owns them, not the
	// browser that asked - and the screen owns the marketplaces store too,
	// so the flow that started on the unmounted browser refetches through
	// the store that survived it: the third list call is that reconciliation
	// read, and its failure keeps the error copy up until a fresh read
	// reconciles.
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");
	expect(hub.methods.filter((method) => method === "evener/marketplace/list")).toHaveLength(3);

	await act(async () => {
		retryMarketplaces(tree);
	});
	await act(async () => {});
	// The retry read answers with the stale row - the first authoritative read
	// after the outcome, which the fallback trusts - so the fence retires and
	// Remove re-enables; a press on it would only draw the hub's same
	// idempotent applied outcome.
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("leaves a re-added marketplace removable after an applied removal reconciles the list", async () => {
	let releaseRemoval!: () => void;
	const firstRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The hub answers the mount read with acme; the browser remount's
			// read finds the removal the hub already applied - its list omits
			// acme.
			return { marketplaces: listCalls === 1 ? [marketplace] : [] };
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => confirm.onPress?.());

	// Leave the Marketplaces segment and come back while the removal is still pending:
	// the remounted browser reads the reconciled list the hub now publishes -
	// acme is gone from it - BEFORE the outcome records with the screen's
	// guard, so the record lands over an absence the screen has already seen
	// and the fence covers the name again. The re-add below submits a BLANK
	// name - the hub assigns one - and lands within the same wire second, so
	// neither the add's own published list (a same-timestamp row reads as the
	// stale registration the fence guards) nor any later read can tell the
	// fresh registration from the removed one: only the add itself reporting
	// the name it registered can clear the fence, or the re-added acme could
	// never be removed.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	releaseRemoval();
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// Re-add acme through the browser's own modal.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root
			.findByProps({ accessibilityLabel: "Marketplace source" })
			.props.onChangeText("https://example.test/plugins.git");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence when a blank-name re-add lands in the wire-indistinguishable second", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails, so the stale row - with its
			// original whole-second stamp - is all the screen ever sees.
			if (listCalls === 2) throw new Error("list unavailable");
			return { marketplaces: [marketplace] };
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Re-add acme with a BLANK name and the source the stale row still shows,
	// landing within the same whole second as the original registration: the
	// add's own answer is a list indistinguishable from the stale one, so only
	// the add knowing what it re-registered can clear the fence - or the
	// fresh registration could never be removed.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence for a blank-name re-add that resolves after the browser unmounts", async () => {
	let releaseAdd!: () => void;
	const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseAdd = () =>
			resolve({
				marketplaces: [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			});
	});
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; every later read carries
			// the re-registration the add made - a different source, stamped
			// within the same whole second as the removed one.
			if (listCalls === 2) throw new Error("list unavailable");
			return {
				marketplaces:
					listCalls === 1 ? [marketplace] : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			};
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
		add: () => pendingAdd,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Start a BLANK-name add from a different source, then leave the Marketplaces
	// segment before it resolves: the store the screen owns outlives the browser,
	// but a newer list read can still hold the add's publication - the
	// registration it made is only NAMED off the captured answer, which is
	// what clears the fence here.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/other");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		releaseAdd();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {});

	// The fence the stale row kept has to clear for the registration the add
	// put back, or the re-added marketplace could never be removed.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence for a wire-indistinguishable re-add made beside another change", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; every later read carries
			// the re-registration the add made alongside a marketplace another
			// client registered since the list this screen last carried.
			if (listCalls === 2) throw new Error("list unavailable");
			return {
				marketplaces:
					listCalls === 1
						? [marketplace]
						: [
								marketplace,
								{
									name: "gamma",
									source: { kind: "github", repo: "gamma/plugins" },
									lastUpdated: 2,
								},
							],
			};
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
		add: async () => ({
			marketplaces: [
				marketplace,
				{
					name: "gamma",
					source: { kind: "github", repo: "gamma/plugins" },
					lastUpdated: 2,
				},
			],
		}),
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Re-add acme with a BLANK name and its original source, within the same
	// whole second, while another client registers gamma: the add's answer
	// newly carries gamma, and the fence still needs its own re-registration
	// named beside it - or acme could never be removed.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence for a blank-name re-add held behind a newer list read", async () => {
	let releaseAdd!: () => void;
	const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseAdd = () =>
			resolve({
				marketplaces: [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			});
	});
	let releaseRefreshRead!: () => void;
	const heldRead = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseRefreshRead = () =>
			resolve({
				marketplaces: [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			});
	});
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; a change-notification read -
			// issued while the add is still in flight - stays pending, holding
			// the add's own publication behind it; every later read carries the
			// re-registration the add made.
			if (listCalls === 2) throw new Error("list unavailable");
			if (listCalls === 3) return heldRead;
			return Promise.resolve({
				marketplaces:
					listCalls === 1 ? [marketplace] : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			});
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
		add: () => pendingAdd,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Start a BLANK-name add from a different source, then the hub says the list
	// changed before it resolves: the pending read outranks the add, so the store
	// holds the add's publication behind it.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/other");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await marketplacesChanged(hub);
	await act(async () => {
		releaseAdd();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {});

	// The pending read settles with the same whole-second stamp, so it
	// cannot reconcile the fence either: the add's own registration is the
	// only thing that can name it.
	await act(async () => {
		releaseRefreshRead();
		await Promise.resolve();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("keeps the fence for a fenced row a blank add's answer carries unchanged", async () => {
	let releaseAdd!: () => void;
	const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseAdd = () =>
			resolve({
				marketplaces: [
					// acme's row exactly as the pre-add list carried it: the hub's
					// answer read failed and served the stale cache, so this row is
					// NOT one the add created - the add registered delta, named off
					// the same source's own catalog.
					marketplace,
					{ name: "delta", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 2 },
				],
			});
	});
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; a change-notification read -
			// issued while the add is still in flight - stays pending, holding the
			// add's own publication behind it so only the add's naming can touch
			// the fence.
			if (listCalls === 1) return Promise.resolve({ marketplaces: [marketplace] });
			if (listCalls === 2) return Promise.reject(new Error("list unavailable"));
			return new Promise<{ marketplaces: MarketplaceEntry[] }>(() => {});
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
		add: () => pendingAdd,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Submit a BLANK-name add of the fenced marketplace's own source: only a
	// row the answer newly carries - one the pre-add list did not have, in the
	// wire's own form - may be the registration this add made, so the stale
	// acme row the answer also carries must leave the fence alone.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await marketplacesChanged(hub);
	await act(async () => {
		releaseAdd();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {});
	expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(true);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(1);
});

it("clears the fence for a wire-indistinguishable blank re-add when no list read succeeds", async () => {
	let listCalls = 0;
	let removals = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; every
			// later list read fails, so no read can ever name the re-registration
			// the add makes - only the add's own answer can.
			if (listCalls === 1) return { marketplaces: [marketplace] };
			throw new Error("list unavailable");
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Re-add acme with a BLANK name and its original source, within the same
	// whole second as the removed registration, on a hub whose list reads
	// keep failing: the add's own answer is the only thing that can name the
	// re-registration, or the fresh one could never be removed.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("retires the fence when the read holding a wire-indistinguishable blank re-add lands", async () => {
	let releaseAdd!: () => void;
	const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseAdd = () => resolve({ marketplaces: [marketplace] });
	});
	let releaseRefreshRead!: () => void;
	const heldRead = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		// The read resolves with the re-registration under the removed
		// registration's own identity: the same name, the same source, the
		// same whole-second stamp - the wire cannot tell it from the stale
		// row, so neither can any diff.
		releaseRefreshRead = () => resolve({ marketplaces: [marketplace] });
	});
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; a change-notification read -
			// issued while the add is still in flight - stays pending, holding
			// the add's own publication behind it, and every later read carries
			// the re-registration the add made under the indistinguishable
			// identity.
			if (listCalls === 2) return Promise.reject(new Error("list unavailable"));
			if (listCalls === 3) return heldRead;
			return Promise.resolve({ marketplaces: [marketplace] });
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
		add: () => pendingAdd,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// Re-add acme with a BLANK name and its original source - landing within
	// the same whole second, so the add's own answer is a list the wire cannot
	// tell from the stale one and no naming can pick the registration out -
	// while a change-notification read stays on the wire ahead of the answer, so
	// the store holds the answer's publication behind it.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "GitHub" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await marketplacesChanged(hub);
	await act(async () => {
		releaseAdd();
		await Promise.resolve();
		await Promise.resolve();
		await Promise.resolve();
	});
	await act(async () => {});
	expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);

	// The read that held the answer's publication lands with the
	// re-registration the wire cannot tell from the removed one: it is the
	// first publication after the outcome either way, so the fallback ruling
	// retires the fence on its arrival - the exact same-name same-source
	// same-second case the source fallback used to be documented for, covered
	// by the read side instead of any naming.
	await act(async () => {
		releaseRefreshRead();
		await Promise.resolve();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("answers false for an applied outcome a client switch outran, and records for the client that replaced it", async () => {
	const oldHub = marketplaceClient({});
	const newHub = marketplaceClient({ list: async () => ({ marketplaces: [] }) });
	harness.connection = readyConnection(oldHub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";

	// The outcome's recording path, captured through the props the screen
	// hands the browser: the return says whether the fence was actually
	// stored, and the browser drops a false outcome whole.
	const record = pluginsProps(tree).onAppliedRemoval;

	// Replace the client; the switch's effect has run, so the screen now
	// belongs to the new client's guard.
	harness.connection = readyConnection(newHub.client);
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	await act(async () => {});

	// A late applied outcome from the replaced client must answer false -
	// not store the name and answer true, which would send the browser off to
	// set its watermark and refetch for a fence that never existed.
	// The snapshot and version the recording reads: a list unavailable to the
	// outcome, so the fence is the answer's own, and the store's baseline.
	expect(record("acme", notice, oldHub.client, null, 0)).toBe(false);
	expect(pluginsProps(tree).appliedRemovalNames.size).toBe(0);

	// The store is alive for the client that replaced it: an outcome of its
	// own records and fences, so the false above was the switch's, not a dead
	// store's.
	let stored = false;
	await act(async () => {
		stored = pluginsProps(tree).onAppliedRemoval("acme", notice, newHub.client, null, 0);
	});
	expect(stored).toBe(true);
	expect(pluginsProps(tree).appliedRemovalNames.has("acme")).toBe(true);
});

it("stores an applied outcome that recorded before the switch, then drops it with the replaced client", async () => {
	const oldHub = marketplaceClient({});
	const newHub = marketplaceClient({ list: async () => ({ marketplaces: [] }) });
	harness.connection = readyConnection(oldHub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";
	const record = pluginsProps(tree).onAppliedRemoval;

	// The outcome records while the screen still belongs to the old client -
	// the flank of the client-switch window on the other side from the test
	// above: there the switch outran the outcome's check and the answer had
	// to be false; here the check and the store both outran the switch, and a
	// true answer has to mean the entry was actually stored, because the
	// browser sets its watermark and refetches off that true.
	let answer: boolean | undefined;
	act(() => {
		answer = record("acme", notice, oldHub.client, null, 0);
	});
	expect(answer).toBe(true);
	// The store the true answered for, as the fence the browser sees: the
	// name is in the guard, committed.
	expect(pluginsProps(tree).appliedRemovalNames.has("acme")).toBe(true);

	// The client switch then replaces the guard wholesale: the fence the
	// replaced client recorded is not the new client's, and nothing of the
	// old outcome may survive it.
	harness.connection = readyConnection(newHub.client);
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	expect(pluginsProps(tree).appliedRemovalNames.size).toBe(0);
	expect(renderedText(tree)).not.toContain("clone cleanup failed");
	// And the new client's own outcome still records.
	let fresh = false;
	await act(async () => {
		fresh = pluginsProps(tree).onAppliedRemoval("beta", notice, newHub.client, null, 0);
	});
	expect(fresh).toBe(true);
	expect(pluginsProps(tree).appliedRemovalNames.has("beta")).toBe(true);
});

it("prunes each fence against its own baseline, not the latest outcome's", async () => {
	const beta: MarketplaceEntry = {
		name: "beta",
		source: { kind: "github", repo: "beta/plugins" },
		lastUpdated: 1,
	};
	const hub = marketplaceClient({});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";
	const record = pluginsProps(tree).onAppliedRemoval;
	const report = pluginsProps(tree).onAuthoritativeMarketplaces;

	// Two fences with different baselines: acme's predates the read reported
	// below, beta's was recorded against a later publication. One browser-wide
	// watermark would rise to beta's baseline with its recording and the read
	// acme's fence was waiting for would never be reported (the round-4 M1
	// shape from #2137's review); each name's own baseline hands that read to
	// acme's fence alone.
	let storedAcme = false;
	let storedBeta = false;
	await act(async () => {
		storedAcme = record("acme", notice, hub.client, [marketplace], 1);
		storedBeta = record("beta", notice, hub.client, [beta], 3);
	});
	expect(storedAcme).toBe(true);
	expect(storedBeta).toBe(true);
	await act(async () => {
		report([marketplace, beta], hub.client, 2);
	});
	const names = pluginsProps(tree).appliedRemovalNames;
	expect(names.has("acme")).toBe(false);
	expect(names.has("beta")).toBe(true);
});

it("leaves no fence when the store's snapshot already omits the target", async () => {
	const beta: MarketplaceEntry = {
		name: "beta",
		source: { kind: "github", repo: "beta/plugins" },
		lastUpdated: 1,
	};
	const hub = marketplaceClient({});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	const notice = "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";
	const record = pluginsProps(tree).onAppliedRemoval;

	// An accepted snapshot that already omits the target is the outcome's own
	// reconciliation, so it leaves no fence at all - the shape the web's sheet
	// records (MarketplaceSheet.tsx) - while the outcome's notice still warns.
	let stored = false;
	await act(async () => {
		stored = record("acme", notice, hub.client, [], 4);
	});
	expect(stored).toBe(true);
	expect(pluginsProps(tree).appliedRemovalNames.size).toBe(0);
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// A snapshot that still carries the target - or no list at all - fences.
	await act(async () => {
		stored = record("beta", notice, hub.client, [beta], 5);
	});
	expect(stored).toBe(true);
	expect(pluginsProps(tree).appliedRemovalNames.has("beta")).toBe(true);
});

it("clears the fence when a same-name re-add registers while reconciliation reads keep failing", async () => {
	let releaseRemoval!: () => void;
	const firstRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The hub answers the mount read with acme; every reconciliation read
			// after the applied removal fails, so no list ever shows acme gone.
			if (listCalls === 2) throw new Error("list unavailable");
			return { marketplaces: [marketplace] };
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
		},
		add: async () => ({ marketplaces: [{ ...marketplace, lastUpdated: 2 }] }),
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => confirm.onPress?.());
	releaseRemoval();
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// Re-add acme with no intervening list ever showing it gone: the add is a
	// NEW registration the hub accepted, so the stale removal's fence has to
	// clear for it.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root
			.findByProps({ accessibilityLabel: "Marketplace source" })
			.props.onChangeText("https://example.test/plugins.git");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence when another client re-adds the marketplace", async () => {
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows acme; the reconciliation read after the applied
			// removal fails; the retry read finds the registration another client
			// made - a fresh lastUpdated, not the stale row the removal applied to.
			if (listCalls === 2) throw new Error("list unavailable");
			return {
				marketplaces: listCalls >= 3 ? [{ ...marketplace, lastUpdated: 2 }] : [marketplace],
			};
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});

	// The reconciliation read failed, so the hub's list this screen last saw
	// still carries the stale registration the removal applied to. Another
	// client re-adds the marketplace: the retry read's fresh identity has to
	// clear the fence, or the new registration could never be removed.
	await act(async () => {
		retryMarketplaces(tree);
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence when another client re-adds the name from a different source in the same wire second", async () => {
	let listCalls = 0;
	let removals = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; the retry read finds the
			// registration another client made after the hub applied the removal
			// - the same whole-second stamp, from a different source.
			if (listCalls === 2) throw new Error("list unavailable");
			return {
				marketplaces:
					listCalls === 1 ? [marketplace] : [{ ...marketplace, source: { kind: "github", repo: "acme/other" } }],
			};
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// The re-add's stamp matches the removed registration's whole second, but
	// its source is its own: the fence the stale read kept has to recognize a
	// replacement registration, or the re-added marketplace could never be
	// removed.
	await act(async () => {
		retryMarketplaces(tree);
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("re-enables Remove for a same-source same-second re-registration", async () => {
	let removals = 0;
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; the retry read carries
			// another client's re-registration - the SAME source, stamped within
			// the SAME whole second, indistinguishable on the wire from the row
			// the removal took out.
			if (listCalls === 2) throw new Error("list unavailable");
			return { marketplaces: [marketplace] };
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? Promise.reject(cloneLitterError(null, false)) : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");
	expect(renderedText(tree)).toContain("Could not load marketplaces.");

	// The reconciliation read failed, so the retry read is the first
	// authoritative read after the outcome - and it carries a row the wire
	// cannot tell from the stale one. The fence has to retire for it (the
	// fallback ruling), or the re-registered marketplace could never be
	// removed from this client.
	await act(async () => {
		retryMarketplaces(tree);
	});
	await act(async () => {});
	// The removal's detail is still the open view - the retry read re-enables
	// Remove in place.
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("retires the fence on the trusted read when an add resolving after unmount registers another name", async () => {
	let releaseAdd!: () => void;
	const pendingAdd = new Promise<{ marketplaces: MarketplaceEntry[] }>((resolve) => {
		releaseAdd = () =>
			resolve({
				marketplaces: [
					marketplace,
					{ name: "gamma", source: { kind: "github", repo: "gamma/plugins" }, lastUpdated: 2 },
				],
			});
	});
	let listCalls = 0;
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// post-removal reconciliation read fails; the remount's read carries
			// the hub's truth after the add registered gamma - acme's stale row
			// still under the wire-indistinguishable identity, gamma beside it.
			if (listCalls === 2) throw new Error("list unavailable");
			return {
				marketplaces:
					listCalls === 1
						? [marketplace]
						: [marketplace, { name: "gamma", source: { kind: "github", repo: "gamma/plugins" }, lastUpdated: 2 }],
			};
		},
		remove: async () => {
			throw cloneLitterError(null, false);
		},
		add: () => pendingAdd,
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});

	// Start an add for another marketplace and leave the Marketplaces segment before it
	// resolves: the answer's own naming reports only the name it registered -
	// gamma - never acme, and the registration acme still carries retires
	// under the fallback ruling on the trusted publication that follows.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root
			.findByProps({ accessibilityLabel: "Marketplace source" })
			.props.onChangeText("https://example.test/gamma.git");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => submit.props.onPress());
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		releaseAdd();
		await Promise.resolve();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});

	expect(hub.methods.filter((method) => method === "evener/marketplace/add")).toHaveLength(1);
	// The remount's read carries the hub's truth - gamma registered, acme's
	// row under the identity the wire cannot tell from the stale one - and it
	// is the first authoritative read after the outcome, so the fallback
	// retires acme's fence for whatever it vouches for: both rows render and
	// acme is removable again, a press only drawing the same idempotent
	// applied outcome.
	expect(marketplaceRows(tree, "gamma").length).toBeGreaterThan(0);
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("briefly fences a re-added registration observed before the removal settles, then clears it", async () => {
	let releaseRemoval!: () => void;
	const firstRemoval = new Promise<never>((_resolve, reject) => {
		releaseRemoval = () => reject(cloneLitterError(null, false));
	});
	let removals = 0;
	let listCalls = 0;
	let releaseRead!: () => void;
	const pendingRead = new Promise<void>((resolve) => {
		releaseRead = () => resolve();
	});
	const hub = marketplaceClient({
		list: async () => {
			listCalls += 1;
			// The mount read shows the registration the removal targets; the
			// remount's read - issued while the removal is still pending - finds
			// the registration another client made after the hub applied it. The
			// outcome's own reconciliation read - and anything that follows it -
			// stays on the wire until the test releases it, so the fence the
			// record raises stays observable before the first authoritative read
			// after the outcome retires it.
			if (listCalls >= 3) await pendingRead;
			return {
				marketplaces: listCalls === 1 ? [marketplace] : [{ ...marketplace, lastUpdated: 2 }],
			};
		},
		remove: () => {
			removals += 1;
			return removals === 1 ? firstRemoval : Promise.resolve({ marketplaces: [] });
		},
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.text === "Remove");
	if (!confirm?.onPress) throw new Error("Remove confirmation was not shown");
	await act(async () => confirm.onPress?.());

	// Another client re-added acme while the removal was pending, and the
	// remounted browser read that new registration before the outcome settled:
	// the outcome's record lands anyway and fences the name - whatever the
	// hub's truth currently carries - so the re-added registration stays
	// fenced until the next authoritative read re-establishes it.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	releaseRemoval();
	await act(async () => {});
	expect(renderedText(tree)).toContain("Marketplace removed; clone cleanup failed");

	// The fence covers the re-added registration for now: no authoritative
	// read has landed since the record.
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	expect(tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.disabled).toBe(true);

	// The next authoritative read carries the re-add's fresh identity, which
	// clears the fence, so the newer registration is removable again.
	releaseRead();
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Installed" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

it("clears the fence for a re-added marketplace whose registration carries the same wire timestamp", async () => {
	const hub = marketplaceClient({
		remove: async () => {
			throw cloneLitterError(null, false);
		},
		// The re-add lands within the same second the original was registered,
		// so the wire's whole-second lastUpdated does not distinguish them.
		add: async () => ({ marketplaces: [marketplace] }),
	});
	harness.connection = readyConnection(hub.client);
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await browseMarketplace(tree);
	await confirmMarketplaceRemoval(tree);
	await act(async () => {});

	// Re-add acme by name within the same second: the add this screen itself
	// made replaced the registration the fence guards, so the fence clears
	// even though the wire cannot tell the two registrations apart.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Back to Plugins" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root
			.findByProps({ accessibilityLabel: "Marketplace source" })
			.props.onChangeText("https://example.test/plugins.git");
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplace name" }).props.onChangeText("acme");
	});
	const submit = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	await act(async () => {
		submit.props.onPress();
		await Promise.resolve();
	});
	await act(async () => {});

	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	const remove = tree.root.findByProps({ accessibilityLabel: "Remove marketplace" });
	expect(remove.props.disabled).toBe(false);
	await confirmMarketplaceRemoval(tree);
	expect(hub.methods.filter((method) => method === "evener/marketplace/remove")).toHaveLength(2);
});

// ---------------------------------------------------------------------------
// The reconnect-recovery suite (#1915's round-1 review gap): a transport
// flap keeps the SAME AppwireClient object (only its state moves
// ready -> reconnecting -> ready), so PluginsPage's own mount effect -
// the only thing that ever called fetchPlugins() - never runs again, and
// nothing else told the store the flap happened. These mount the real
// screen (the only way to observe the wiring between useConnection's
// state and the store's own connectionChanged) and count the wire calls
// it makes.
// ---------------------------------------------------------------------------

/** A plugins client: every method call is recorded in `methods`, every
 * `evener/plugin/list` answers with `plugins`, and `evener/marketplace/list`
 * (the marketplace segments' own read) answers with an empty list - shaped for
 * whichever surface a test mounts. */
function pluginsClient(plugins: PluginEntry[]) {
	const methods: string[] = [];
	const client = Object.assign(new FakeClient("ready"), {
		request: async (method: string) => {
			methods.push(method);
			if (method === "evener/marketplace/list") return { marketplaces: [] };
			// A hub from before the update check.
			if (method === "evener/plugin/checkUpdates") throw new Error("method not found");
			return { plugins };
		},
		onNotification: () => () => {},
	} as Omit<ConversationClientLike, "state" | "onReady" | "onStateChange">) as ConversationClientLike;
	return { client, methods };
}

const plugin: PluginEntry = {
	plugin: "demo-plugin",
	marketplace: "core",
	version: "1.0.0",
	enabled: true,
	autoUpgrade: false,
	broken: false,
	installPath: "/plugins/demo-plugin",
	installedAt: 0,
	lastUpdated: 0,
};

it("re-reads the installed list once the connection returns to ready after a flap", async () => {
	const hub = pluginsClient([plugin]);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("demo-plugin");
	expect(hub.methods.filter((m) => m === "evener/plugin/list")).toHaveLength(1);

	// A passive flap: the connection layer's own generation guard keeps the
	// SAME client object through it (hubConnection.ts) - only `state` moves.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	expect(renderedText(tree)).toContain("demo-plugin");

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});

	expect(hub.methods.filter((m) => m === "evener/plugin/list")).toHaveLength(2);
});

it("MarketplaceBrowser re-reads its list once the connection returns to ready after a flap", async () => {
	const hub = pluginsClient([plugin]);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	// Switch to the Marketplaces segment, which mounts MarketplaceBrowser.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.methods.filter((m) => m === "evener/marketplace/list")).toHaveLength(1);

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});

	expect(hub.methods.filter((m) => m === "evener/marketplace/list")).toHaveLength(2);
});

it("keeps the marketplace draft through a flap, with no Reconnect anywhere", async () => {
	const hub = pluginsClient([plugin]);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Marketplaces" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add marketplace" }).props.onPress();
	});
	await act(async () => {
		tree.root
			.findByProps({ accessibilityLabel: "Marketplace source" })
			.props.onChangeText("https://example.test/plugins.git");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	const sourceInput = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	expect(sourceInput.props.value).toBe("https://example.test/plugins.git");
	// The app reconnects on its own (spec principle 2).
	expect(tree.root.findAllByProps({ accessibilityLabel: "Reconnect" })).toHaveLength(0);
});

it("adds a marketplace from a grouped form: a segmented kind, field rows, Add up top", async () => {
	const onAdd = vi.fn(async () => {});
	const onClose = vi.fn();
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const kinds = tree.root.findByProps({ accessibilityRole: "radiogroup", accessibilityLabel: "Kind" });
	const kindLabels = kinds
		.findAllByProps({ accessibilityRole: "radio" })
		.map((radio) => radio.props.accessibilityLabel);
	expect(kindLabels).toEqual(["Git URL", "GitHub", "Hub directory"]);
	const add = () => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" });
	expect(add().props.disabled).toBe(true);
	act(() => kinds.findByProps({ accessibilityLabel: "GitHub" }).props.onPress());
	const source = tree.root.find(
		(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Marketplace source",
	);
	expect(source.props.placeholder).toBe("owner/repo");
	// An empty machine field shows its placeholder in the UI font; once it holds
	// the machine's value the field is Menlo (spec 16.2).
	expect(source.props.style.fontFamily).toBeUndefined();
	act(() => source.props.onChangeText("acme/plugins"));
	expect(source.props.style.fontFamily).toBe("Menlo");
	// The source's section label names what the kind asks for.
	expect(renderedText(tree)).toContain("Repository");
	expect(source.props.returnKeyType).toBe("next");
	expect(tree.root.findByProps({ accessibilityLabel: "Marketplace name" }).props.returnKeyType).toBe("done");
	await act(async () => {
		add().props.onPress();
	});
	expect(onAdd).toHaveBeenCalledWith({ name: "", source: { kind: "github", repo: "acme/plugins" } });
	expect(onClose).toHaveBeenCalledOnce();
});

it("holds Add marketplace open, and says Adding, while the add is in flight", async () => {
	let finish = () => {};
	const onAdd = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)));
	const onClose = vi.fn();
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const source = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	act(() => source.props.onChangeText("https://example.test/plugins.git"));
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});
	const adding = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Adding…" });
	expect(adding.props.accessibilityState).toEqual({ disabled: true, busy: true });
	const cancel = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" });
	expect(cancel.props.disabled).toBe(true);
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).not.toHaveBeenCalled();
	alertRequests.length = 0;
	await act(async () => finish());
	// The add landed: it closes without asking, though a source was typed.
	expect(onClose).toHaveBeenCalledOnce();
	expect(alertRequests).toHaveLength(0);
});

it("heads Add marketplace with the shared sheet header: its title and Cancel, and no second title in the body", () => {
	const onClose = vi.fn();
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={async () => {}}
		/>,
	);
	// The sheet's title comes first; the form's section labels are headers too.
	const title = tree.root.findAllByProps({ accessibilityRole: "header" })[0];
	expect(title?.props.children).toBe("Add marketplace");
	// The hub it adds to rides under the title.
	expect(renderedText(tree)).toContain("Work hub");
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	expect(onClose).toHaveBeenCalledOnce();
	// Only the header says it.
	const repeats = tree.root.findAll(
		(node) => String(node.type) === "Text" && node !== title && node.props.children === "Add marketplace",
	);
	expect(repeats).toHaveLength(0);
});

it("keeps Add marketplace open when readiness is lost during submit", async () => {
	const hub = pluginsClient([]);
	const onAdd = vi.fn(async () => {});
	const onClose = vi.fn();
	let readinessChecks = 0;
	const tree = render(
		<AddMarketplace
			client={hub.client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => readinessChecks++ === 0}
			onClose={onClose}
			onAdd={onAdd}
		/>,
	);
	const source = tree.root.findByProps({ accessibilityLabel: "Marketplace source" });
	act(() => source.props.onChangeText("https://example.test/plugins.git"));

	// The press passed whenReady's own recheck; the gate rechecks the same
	// predicate once more, after readiness was lost between the two. Nothing
	// ran, so the modal keeps the draft rather than closing as if it had.
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});

	expect(onAdd).not.toHaveBeenCalled();
	expect(onClose).not.toHaveBeenCalled();
	expect(tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.value).toBe(
		"https://example.test/plugins.git",
	);
});

it("never renders the previous hub's retained client once the route names a hub whose profile is not active", async () => {
	// The panel-review gap: the route is re-keyed to hub B while the
	// connection still reports hub A - ready, with hub A's client. The
	// mismatch window's early return hides that data, but the retention
	// hooks must not record hub A's readiness under the route's hub either:
	// when the profile then moves to hub B mid-flap, hub B must meet a wall
	// for a hub it has never been ready for - not a banner over hub A's
	// retained client and rows.
	const hubA = pluginsClient([plugin]);
	harness.connection = {
		activeProfile: { id: "hub-a", name: "A hub" },
		client: hubA.client,
		state: "ready",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	const props = {
		route: { params: { hubId: "hub-b" } },
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("no longer selected");

	harness.connection = {
		activeProfile: { id: "hub-b", name: "B hub" },
		client: null,
		state: "reconnecting",
		fatal: false,
		downSince: null,
		lastLiveAt: null,
	};
	await act(async () => {
		tree.update(<PluginsStack {...props} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).toContain("B hub");
	expect(rekeyed).toContain("Connecting to B hub…");
	expect(rekeyed).not.toContain("demo-plugin");
});

// ---------------------------------------------------------------------------
// The page itself (spec 12's Plugins; rulings 7 and 9): three segments, the
// installed plugins grouped by marketplace with an "On by default" switch per
// row, a detail sheet per plugin, and nothing that asks you to reconnect or
// pull to refresh.

function entry(name: string, overrides: Partial<PluginEntry> = {}): PluginEntry {
	return { ...plugin, plugin: name, installPath: `/plugins/${name}`, ...overrides };
}

/** A hub whose installed list is `plugins` and whose only marketplace is
 * acme, with a two-plugin catalog. */
function pageHub(plugins: PluginEntry[]) {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => ({ plugins }));
	hub.on("evener/marketplace/list", () => ({ marketplaces: [marketplace] }));
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		description: "Acme's tools",
		plugins: [{ name: "tool", description: "A tool" }],
	}));
	return hub;
}

async function mountPage(hub: FakeClient, params: Record<string, unknown> = {}) {
	harness.connection = readyConnection(hub as unknown as ConversationClientLike);
	const navigation = { setParams: vi.fn(), navigate: vi.fn() };
	const props = {
		route: { params: { hubId: "hub-1", ...params } },
		navigation,
	} as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsStack {...props} />);
	await act(async () => {});
	return { tree, navigation, props };
}

async function choose(tree: ReturnType<typeof render>, segment: string) {
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "radio", accessibilityLabel: segment }).props.onPress();
	});
	await act(async () => {});
}

/** An installed plugin's row, which opens its detail. */
function pluginRow(tree: ReturnType<typeof render>, name: string) {
	const rows = tree.root.findAll(
		(node) =>
			node.props.accessibilityRole === "button" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${name}, `),
	);
	if (rows.length !== 1) throw new Error(`expected one ${name} row, found ${rows.length}`);
	return rows[0];
}

async function openDetail(tree: ReturnType<typeof render>, name: string) {
	await act(async () => {
		pluginRow(tree, name).props.onPress();
	});
	await act(async () => {});
	return tree.root.findByType("Modal" as never);
}

it("asks the hub for updates once the installed list loads, and offers Upgrade only where it found one", async () => {
	const hub = pageHub([entry("stale"), entry("current")]);
	const releaseList = deferRequest<{ plugins: PluginEntry[] }>(hub, "evener/plugin/list");
	hub.on("evener/plugin/checkUpdates", () => {
		// The hub now holds the answer, so the list read that follows carries it.
		hub.on("evener/plugin/list", () => ({ plugins: [entry("stale", { updateAvailable: true }), entry("current")] }));
		return { plugins: [] };
	});
	const { tree } = await mountPage(hub);
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);
	await act(async () => releaseList({ plugins: [entry("stale"), entry("current")] }));
	await act(async () => {});
	expect(hub.calls.filter((call) => call.method === "evener/plugin/checkUpdates")).toHaveLength(1);
	expect(pluginRow(tree, "stale").props.accessibilityLabel).toBe("stale, core, 1.0.0 · Update available");
	expect(pluginRow(tree, "current").props.accessibilityLabel).toBe("current, core, 1.0.0");

	expect((await openDetail(tree, "stale")).findAllByProps({ label: "Upgrade" }).length).toBeGreaterThan(0);
	await act(async () => {
		tree.root
			.findByType("Modal" as never)
			.findByProps({ accessibilityLabel: "Done" })
			.props.onPress();
	});
	expect((await openDetail(tree, "current")).findAllByProps({ label: "Upgrade" })).toHaveLength(0);
});

it("asks for no updates when the page closes before its installed list lands", async () => {
	const hub = pageHub([entry("stale")]);
	const releaseList = deferRequest<{ plugins: PluginEntry[] }>(hub, "evener/plugin/list");
	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	const { tree } = await mountPage(hub);
	await act(async () => tree.unmount());
	await act(async () => releaseList({ plugins: [entry("stale")] }));
	await act(async () => {});
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);
});

/** Flaps the page's connection to reconnecting and back to ready. */
async function flap(tree: ReturnType<typeof render>, props: ComponentProps<typeof PluginsPage>) {
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	await act(async () => {});
}

const checks = (hub: FakeClient) => hub.calls.filter((call) => call.method === "evener/plugin/checkUpdates").length;

it("asks for updates again after a reconnect when the check never reached the hub's answer", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new Error("connection closed");
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);

	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(2);
});

it("waits for the connection to be ready before asking again", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new Error("request timed out");
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);

	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	expect(checks(hub)).toBe(1);
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => tree.update(<PluginsStack {...props} />));
	await act(async () => {});
	expect(checks(hub)).toBe(2);
});

it("asks a hub that refused the check, or answered it, only once across reconnects and list failures", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/checkUpdates", () => {
		throw new WireError("method not found", -32601);
	});
	const { tree, props } = await mountPage(hub);
	await act(async () => {});
	expect(checks(hub)).toBe(1);
	await flap(tree, props);
	expect(checks(hub)).toBe(1);

	// The list fails on one reconnect and recovers on the next.
	hub.on("evener/plugin/list", () => {
		throw new Error("hub busy");
	});
	await flap(tree, props);
	hub.on("evener/plugin/list", () => ({ plugins: [entry("stale")] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(1);
});

it("asks for updates once a failed installed list recovers, not while it is failing", async () => {
	const hub = pageHub([entry("stale")]);
	hub.on("evener/plugin/list", () => {
		throw new Error("hub busy");
	});
	hub.on("evener/plugin/checkUpdates", () => ({ plugins: [] }));
	const { tree, props } = await mountPage(hub);
	expect(hub.calls.some((call) => call.method === "evener/plugin/checkUpdates")).toBe(false);

	// The same client flaps and comes back; the store re-reads its list.
	hub.on("evener/plugin/list", () => ({ plugins: [entry("stale")] }));
	await flap(tree, props);
	expect(checks(hub)).toBe(1);
});

it("says a broken plugin's row has an update when the hub found one", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked", { broken: true, updateAvailable: true })]));
	expect(pluginRow(tree, "cracked").props.accessibilityLabel).toBe("cracked, core, Broken · Update available");
});

it("offers no Upgrade on a hub without the update check, and tells a broken plugin only to be removed", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked", { broken: true })]));
	const detail = await openDetail(tree, "cracked");
	expect(detail.findAllByProps({ label: "Upgrade" })).toHaveLength(0);
	expect(renderedText(tree)).toContain("This plugin is broken. Remove it.");
	expect(renderedText(tree)).not.toContain("Upgrade it or remove it.");
});

it("shows Installed, Marketplaces and Browse, with Installed first", async () => {
	const { tree } = await mountPage(pageHub([entry("demo-plugin")]));
	const segments = tree.root.findAllByProps({ accessibilityRole: "radio" });
	expect(segments.map((node) => node.props.accessibilityLabel)).toEqual(["Installed", "Marketplaces", "Browse"]);
	expect(segments.map((node) => node.props.accessibilityState.checked)).toEqual([true, false, false]);
	expect(renderedText(tree)).toContain(
		"“On by default” sets which plugins new sessions start with. You can still choose per session.",
	);

	await choose(tree, "Marketplaces");
	expect(marketplaceRow(tree, "acme").props.accessibilityLabel).toBe("acme, github: acme/plugins");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" }).length).toBeGreaterThan(0);
	expect(renderedText(tree)).toContain("Add marketplace…");

	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("A tool");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install tool from acme" }).length).toBeGreaterThan(0);
});

it("groups installed plugins by marketplace, with each one's version and state", async () => {
	const { tree } = await mountPage(
		pageHub([
			entry("demo-plugin"),
			entry("auto", { autoUpgrade: true }),
			entry("cracked", { broken: true }),
			entry("elsewhere", { marketplace: "acme" }),
		]),
	);
	const labels = tree.root
		.findAll((node) => node.props.accessibilityRole === "header")
		.map((node) => node.props.children);
	expect(labels).toEqual(["core", "acme"]);
	expect(pluginRow(tree, "demo-plugin").props.accessibilityLabel).toBe("demo-plugin, core, 1.0.0");
	expect(pluginRow(tree, "auto").props.accessibilityLabel).toBe("auto, core, 1.0.0 · Upgrades automatically");
	expect(pluginRow(tree, "cracked").props.accessibilityLabel).toBe("cracked, core, Broken");
});

it("turns a plugin on and off by default from its row's switch", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	hub.on("evener/plugin/disable", () => ({ plugins: [entry("demo-plugin", { enabled: false })] }));
	hub.on("evener/plugin/enable", () => ({ plugins: [entry("demo-plugin")] }));
	const { tree } = await mountPage(hub);
	const toggle = () => tree.root.findByProps({ accessibilityLabel: "demo-plugin on by default" });
	expect(toggle().props.value).toBe(true);
	await act(async () => {
		toggle().props.onValueChange(false);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/disable",
		params: { plugin: "demo-plugin", marketplace: "core" },
	});
	expect(toggle().props.value).toBe(false);
	await act(async () => {
		toggle().props.onValueChange(true);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/enable",
		params: { plugin: "demo-plugin", marketplace: "core" },
	});
	expect(toggle().props.value).toBe(true);
});

it("says Already up to date when an upgrade changes neither version nor commit", async () => {
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({ plugins: [entry("demo-plugin", { gitCommitSha: "abc" })] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Already up to date");
	expect(renderedText(tree)).not.toContain("Upgraded to");
});

it("shows no upgrade result for a plugin the answering list no longer carries", async () => {
	// The detail shows only a listed plugin, so an upgrade whose answer drops
	// it closes the detail; "Already up to date" is never said for it.
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({ plugins: [] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(
		tree.root.findAll((node) => String(node.type) === "HoldingModal" || String(node.type) === "Modal"),
	).toHaveLength(0);
	expect(renderedText(tree)).not.toContain("Already up to date");
});

it("filters installed plugins by plugin or marketplace", async () => {
	const hub = pageHub([
		entry("demo-plugin", { marketplace: "core" }),
		entry("linter", { marketplace: "core" }),
		entry("tool", { marketplace: "acme" }),
	]);
	const { tree } = await mountPage(hub);
	const field = () =>
		tree.root.find(
			(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Filter installed plugins",
		);
	act(() => field().props.onChangeText("lint"));
	expect(renderedText(tree)).toContain("linter");
	expect(renderedText(tree)).not.toContain("demo-plugin");
	act(() => field().props.onChangeText("ACME"));
	expect(renderedText(tree)).toContain("tool");
	expect(renderedText(tree)).not.toContain("linter");
	act(() => field().props.onChangeText("nothing-like-it"));
	expect(renderedText(tree)).toContain("No matching plugins.");
});

it("keeps the filter field while a filter is set, even once the list empties", async () => {
	let plugins = [entry("demo-plugin")];
	const hub = pageHub(plugins);
	hub.on("evener/plugin/list", () => ({ plugins }));
	const { tree } = await mountPage(hub);
	const fields = () =>
		tree.root.findAll(
			(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Filter installed plugins",
		);
	act(() => fields()[0]?.props.onChangeText("demo"));
	plugins = [];
	await act(async () => {
		hub.emitNotification({ method: "evener/plugin/updated", params: {} } as AnyNotification);
	});
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 300));
	});
	expect(fields()).toHaveLength(1);
});

it("reads the installed list again on coming back to the page after a read failed, with nothing to press", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	let reads = 0;
	hub.on("evener/plugin/list", () => {
		reads += 1;
		if (reads === 1) throw new Error("hub busy");
		return { plugins: [entry("demo-plugin")] };
	});
	const { tree } = await mountPage(hub);
	await act(async () => {});
	expect(reads).toBe(1);
	expect(renderedText(tree)).not.toMatch(/\bRetry\b/);
	await act(async () => {
		refocus();
	});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).toContain("demo-plugin");
});

it("says Upgraded to the new version when the upgrade's list carries one", async () => {
	const hub = pageHub([entry("demo-plugin", { gitCommitSha: "abc", updateAvailable: true })]);
	hub.on("evener/plugin/upgrade", () => ({
		plugins: [entry("demo-plugin", { version: "1.2.0", gitCommitSha: "def" })],
	}));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Upgraded to 1.2.0");
	expect(renderedText(tree)).not.toContain("Already up to date");
});

it("holds the detail's switches, Upgrade and Remove, and says a broken plugin is broken", async () => {
	const hub = pageHub([entry("cracked", { broken: true, updateAvailable: true })]);
	hub.on("evener/plugin/setAutoUpgrade", () => ({ plugins: [entry("cracked", { broken: true, autoUpgrade: true })] }));
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "cracked");
	expect(renderedText(tree)).toContain("This plugin is broken. Upgrade it or remove it.");
	expect(detail.findByProps({ accessibilityLabel: "On by default" }).props.value).toBe(true);
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade automatically" }).props.onValueChange(true);
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/setAutoUpgrade",
		params: { plugin: "cracked", marketplace: "core", autoUpgrade: true },
	});
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Remove plugin" }).props.onPress();
	});
	expect(alertRequests.at(-1)?.title).toBe("Remove plugin?");
	expect(alertRequests.at(-1)?.message).toBe("cracked from core on Work hub");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

it("heads a plugin's detail with the shared sheet header: its name, and Done on the right", async () => {
	const { tree } = await mountPage(pageHub([entry("cracked")]));
	const detail = await openDetail(tree, "cracked");
	// The sheet's title comes first; the page's section labels are headers too.
	expect(detail.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("cracked");
	expect(detail.findAllByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" })).toHaveLength(0);
	await act(async () => {
		detail.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
});

// The page clears a link's focus once it acts, so the same plugin named again
// by a later link opens again after its detail was closed.
it("opens a plugin again when a later link names it again", async () => {
	const hub = pageHub([entry("demo-plugin"), entry("cracked", { broken: true })]);
	const target = { plugin: "cracked", marketplace: "core" };
	const { tree, props } = await mountPage(hub, { focus: target });
	const detail = tree.root.findByType("Modal" as never);
	await act(async () => {
		detail.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(0);
	const withFocus = (focus: typeof target | undefined) => (
		<PluginsStack {...props} route={{ ...props.route, params: { ...props.route.params, focus } }} />
	);
	await act(async () => tree.update(withFocus(undefined)));
	await act(async () => tree.update(withFocus({ ...target })));
	await act(async () => {});
	expect(
		tree.root.findByType("Modal" as never).findAllByProps({ accessibilityLabel: "Remove plugin" }).length,
	).toBeGreaterThan(0);
});

it("opens the plugin a notice named once, then clears the focus", async () => {
	const hub = pageHub([entry("demo-plugin"), entry("cracked", { broken: true, updateAvailable: true })]);
	const { tree, navigation } = await mountPage(hub, { focus: { plugin: "cracked", marketplace: "core" } });
	const detail = tree.root.findByType("Modal" as never);
	expect(detail.findAllByProps({ accessibilityLabel: "Remove plugin" }).length).toBeGreaterThan(0);
	expect(renderedText(tree)).toContain("This plugin is broken. Upgrade it or remove it.");
	expect(navigation.setParams).toHaveBeenCalledWith({ focus: undefined });
});

it("leaves an open plugin's late result behind when a link opens another plugin", async () => {
	const hub = pageHub([entry("demo-plugin", { updateAvailable: true }), entry("other", { updateAvailable: true })]);
	let fail: (reason: Error) => void = () => {};
	hub.on(
		"evener/plugin/upgrade",
		() =>
			new Promise((_resolve, reject) => {
				fail = reject;
			}),
	);
	const { tree, props } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	await act(async () => {
		detail.findByProps({ accessibilityLabel: "Upgrade" }).props.onPress();
	});
	const other = { plugin: "other", marketplace: entry("other").marketplace };
	await act(async () => {
		tree.update(
			<PluginsStack {...props} route={{ ...props.route, params: { ...props.route.params, focus: other } }} />,
		);
	});
	await act(async () => fail(new Error("upstream 502")));
	await act(async () => {});
	expect(
		tree.root.findByType("Modal" as never).findAllByProps({ accessibilityLabel: "Upgrade" }).length,
	).toBeGreaterThan(0);
	expect(renderedText(tree)).toContain("other");
	expect(renderedText(tree)).not.toContain("Could not confirm the change");
});

it("updates a marketplace's source and removes it from its detail in Marketplaces", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/refresh", () => ({ marketplaces: [marketplace] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Marketplaces");
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	await act(async () => {});
	// The detail is the marketplace's, not its catalog: Browse reads that.
	expect(hub.calls.some((call) => call.method === "evener/marketplace/browse")).toBe(false);
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Update source" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({ method: "evener/marketplace/refresh", params: { name: "acme" } });
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Remove marketplace" }).props.onPress();
	});
	expect(alertRequests.at(-1)?.title).toBe("Remove marketplace?");
});

it("installs a catalog's plugin from Browse", async () => {
	const hub = pageHub([]);
	hub.on("evener/plugin/install", () => ({ plugins: [entry("tool", { marketplace: "acme" })] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Install tool from acme" }).props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/install",
		params: { plugin: "tool", marketplace: "acme" },
	});
	expect(tree.root.findAllByProps({ accessibilityLabel: "Open tool from acme" }).length).toBeGreaterThan(0);
});

it("pushes a marketplace's page from its list, so the edge swipe returns there (audit M8)", async () => {
	const hub = pageHub([]);
	harness.connection = readyConnection(hub as unknown as ConversationClientLike);
	const navigation = { setParams: vi.fn(), navigate: vi.fn(), push: vi.fn() };
	const props = { route: { params: { hubId: "hub-1" } }, navigation } as unknown as ComponentProps<typeof PluginsPage>;
	const tree = render(<PluginsPage {...props} />);
	await act(async () => {});
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	expect(navigation.push).toHaveBeenCalledWith("Marketplace", { hubId: "hub-1", name: "acme", segment: "browse" });
	await choose(tree, "Marketplaces");
	await act(async () => {
		marketplaceRow(tree, "acme").props.onPress();
	});
	expect(navigation.push).toHaveBeenLastCalledWith("Marketplace", {
		hubId: "hub-1",
		name: "acme",
		segment: "marketplaces",
	});
	// The list stays whole: nothing drills in place.
	expect(renderedText(tree)).not.toContain("Update source");
});

it("opens an installed plugin from its marketplace's page on the Plugins page, with its detail up", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Open tool from acme" }).props.onPress();
	});
	await act(async () => {});
	expect(tree.root.findAllByProps({ accessibilityLabel: "Back to Plugins" })).toHaveLength(0);
	expect(tree.root.findByType("Modal" as never).props.visible).not.toBe(false);
	expect(renderedText(tree)).toContain("On by default");
});

it("filters a marketplace's catalog with the shared search field, as Installed does", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		plugins: [
			{ name: "tool", description: "A tool" },
			{ name: "gadget", description: "A gadget" },
		],
	}));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	const field = tree.root.findByType(SearchField);
	expect(field.props.label).toBe("Filter this catalog");
	await act(async () => field.props.onChangeText("gad"));
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install gadget from acme" }).length).toBeGreaterThan(0);
	expect(tree.root.findAllByProps({ accessibilityLabel: "Install tool from acme" })).toHaveLength(0);
});

it("shows a failed list read as a line with nothing to press", async () => {
	const hub = new FakeClient("ready");
	hub.on("evener/plugin/list", () => {
		throw new Error("list unavailable");
	});
	const { tree } = await mountPage(hub);
	expect(renderedText(tree)).toContain("Could not load installed plugins.");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Retry" })).toHaveLength(0);
});

it("never asks to reconnect or offers pull-to-refresh, in any segment", async () => {
	const hub = pageHub([entry("demo-plugin")]);
	const { tree } = await mountPage(hub);
	for (const segment of ["Installed", "Marketplaces", "Browse"]) {
		await choose(tree, segment);
		if (segment !== "Installed") {
			await act(async () => {
				(segment === "Browse"
					? tree.root.findByProps({ accessibilityLabel: "Browse acme" })
					: marketplaceRow(tree, "acme")
				).props.onPress();
			});
			await act(async () => {});
		}
		expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
		expect(tree.root.findAll((node) => node.props.onRefresh !== undefined)).toHaveLength(0);
	}
	await choose(tree, "Installed");
	await openDetail(tree, "demo-plugin");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
});

it("waits for the installed list in the same padded space every Hub page waits in", async () => {
	const hub = pageHub([]);
	hub.on("evener/plugin/list", () => new Promise(() => {}));
	const { tree } = await mountPage(hub);
	const spinner = tree.root.findByProps({ accessibilityLabel: "Loading installed plugins" });
	expect(spinner.props.style).toEqual({ padding: 32 });
});

function mountAdd(onClose = vi.fn()) {
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={onClose}
			onAdd={async () => {}}
		/>,
	);
	return { tree, onClose };
}

it("shows the busy copy when the shared gate refuses the add", async () => {
	const tree = render(
		<AddMarketplace
			client={pluginsClient([]).client}
			connectionState="ready"
			hubName="Work hub"
			ready
			canUseConnection={() => true}
			onClose={vi.fn()}
			onAdd={() => Promise.reject(new HubWriteBusyError())}
		/>,
	);
	act(() => tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins"));
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Add" }).props.onPress();
	});
	// A refusal is not a failure: the modal stays open on the busy copy.
	expect(renderedText(tree)).toContain(HUB_WRITE_BUSY);
});

it("closes an untouched Add marketplace at once, by Cancel or a swipe", () => {
	alertRequests.length = 0;
	const { tree, onClose } = mountAdd();
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).toHaveBeenCalledTimes(2);
	expect(alertRequests).toHaveLength(0);
});

it("asks before Cancel or a swipe throws away a typed source (spec 6)", () => {
	alertRequests.length = 0;
	const { tree, onClose } = mountAdd();
	act(() => tree.root.findByProps({ accessibilityLabel: "Marketplace source" }).props.onChangeText("acme/plugins"));
	act(() => tree.root.findByType("Modal" as never).props.onRequestClose());
	expect(onClose).not.toHaveBeenCalled();
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === "Keep editing")
			?.onPress?.(),
	);
	expect(onClose).not.toHaveBeenCalled();
	act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === "Discard")
			?.onPress?.(),
	);
	expect(onClose).toHaveBeenCalledOnce();
});

it("says when a plugin was installed and last updated, and leaves out a time the hub doesn't know (audit M9)", async () => {
	const nowSeconds = Math.floor(Date.now() / 1000);
	const hub = pageHub([
		entry("demo-plugin", { installedAt: nowSeconds - 3 * 86400, lastUpdated: nowSeconds - 5 * 3600 }),
		entry("fresh", { installedAt: 0, lastUpdated: 0 }),
	]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	expect(detail.findAllByProps({ accessibilityLabel: "Installed, 3d ago" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ accessibilityLabel: "Updated, 5h ago" }).length).toBeGreaterThan(0);
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	const fresh = await openDetail(tree, "fresh");
	expect(fresh.findAll((node) => node.props.label === "Installed" || node.props.label === "Updated")).toHaveLength(0);
});

it("describes an installed plugin from its marketplace's catalog, as the web does (audit M9)", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	const { tree } = await mountPage(hub);
	await openDetail(tree, "tool");
	await act(async () => {});
	expect(renderedText(tree)).toContain("A tool");
	expect(hub.calls.filter((call) => call.method === "evener/marketplace/browse")).toHaveLength(1);
});

it("says a broken plugin is broken under the actions that fix it, not floating above them (audit M9)", async () => {
	const hub = pageHub([entry("cracked", { broken: true, updateAvailable: true })]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "cracked");
	const nodes = detail.findAll(() => true);
	const actions = nodes.findIndex(
		(node) => node.type === Group && node.findAllByProps({ label: "Upgrade" }).length > 0,
	);
	const warning = nodes.findIndex(
		(node) => node.type === GroupFooter && node.props.children === "This plugin is broken. Upgrade it or remove it.",
	);
	expect(actions).toBeGreaterThan(-1);
	expect(warning).toBeGreaterThan(actions);
});

it("says a time from a clock ahead of this phone's was just now, not a count of 0 (audit M9)", async () => {
	const nowSeconds = Math.floor(Date.now() / 1000);
	const hub = pageHub([entry("demo-plugin", { installedAt: nowSeconds + 3600, lastUpdated: nowSeconds + 3600 })]);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "demo-plugin");
	expect(detail.findAllByProps({ accessibilityLabel: "Installed, just now" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ accessibilityLabel: "Updated, just now" }).length).toBeGreaterThan(0);
});

it("says a time under a second old was just now", async () => {
	// The clock stands still, so however long the mount takes the time is
	// half a second old when the detail reads it.
	vi.useFakeTimers({ toFake: ["Date"] });
	try {
		const halfASecondAgo = (Date.now() - 500) / 1000;
		const hub = pageHub([entry("demo-plugin", { installedAt: halfASecondAgo, lastUpdated: halfASecondAgo })]);
		const { tree } = await mountPage(hub);
		const detail = await openDetail(tree, "demo-plugin");
		expect(detail.findAllByProps({ accessibilityLabel: "Installed, just now" }).length).toBeGreaterThan(0);
	} finally {
		vi.useRealTimers();
	}
});

it.each([
	[
		"the catalog read fails",
		() => {
			throw new Error("catalog unavailable");
		},
	],
	[
		"the catalog doesn't list the plugin",
		() => ({ name: "acme", plugins: [{ name: "other", description: "Another" }] }),
	],
])("leaves out About when %s, and shows the rest", async (_name, browse) => {
	const hub = pageHub([entry("tool", { marketplace: "acme", updateAvailable: true })]);
	hub.on("evener/marketplace/browse", browse);
	const { tree } = await mountPage(hub);
	const detail = await openDetail(tree, "tool");
	await act(async () => {});
	expect(detail.findAll((node) => node.props.label === "About")).toHaveLength(0);
	expect(detail.findAllByProps({ label: "Version" }).length).toBeGreaterThan(0);
	expect(detail.findAllByProps({ label: "Upgrade" }).length).toBeGreaterThan(0);
});

it("points an empty plugin list at Browse when the hub has marketplaces, and at adding one when it has none (audit L6)", async () => {
	const installed = async (hub: FakeClient) => {
		const { tree } = await mountPage(hub);
		// Before the hub's marketplaces are read, it points at nothing.
		expect(renderedText(tree)).toContain("No plugins installed on this hub.");
		expect(renderedText(tree)).not.toMatch(/Browse a marketplace|Add a marketplace/);
		await choose(tree, "Marketplaces");
		await choose(tree, "Installed");
		return renderedText(tree);
	};
	expect(await installed(pageHub([]))).toContain(
		"No plugins installed on this hub. Browse a marketplace to install one.",
	);
	const hub = pageHub([]);
	hub.on("evener/marketplace/list", () => ({ marketplaces: [] }));
	const none = await installed(hub);
	expect(none).toContain("No plugins installed on this hub. Add a marketplace to find plugins.");
	expect(none).not.toContain("Browse a marketplace");
});

it("points an empty marketplace list at the action its own segment has (audit L6)", async () => {
	const hub = pageHub([]);
	hub.on("evener/marketplace/list", () => ({ marketplaces: [] }));
	const { tree } = await mountPage(hub);
	await choose(tree, "Marketplaces");
	expect(renderedText(tree)).toContain("No marketplaces on this hub. Add one to browse its plugins.");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" }).length).toBeGreaterThan(0);
	// Browse has no Add row: it points at the segment that does.
	await choose(tree, "Browse");
	expect(renderedText(tree)).toContain("No marketplaces on this hub. Add one on Marketplaces to browse its plugins.");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Add marketplace" })).toHaveLength(0);
});

it("offers Install and Open as a catalog row's own mini button, as the prototype does, not the whole row (audit M13)", async () => {
	const hub = pageHub([entry("tool", { marketplace: "acme" })]);
	hub.on("evener/marketplace/browse", () => ({
		name: "acme",
		plugins: [
			{ name: "tool", description: "A tool" },
			{ name: "gadget", description: "A gadget" },
		],
	}));
	const { tree } = await mountPage(hub);
	await choose(tree, "Browse");
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Browse acme" }).props.onPress();
	});
	await act(async () => {});
	const minis = tree.root.findAll((node) => node.type === Button && node.props.mini === true);
	expect(minis.map((node) => [node.props.label, node.props.accessibilityLabel])).toEqual([
		["Open", "Open tool from acme"],
		["Install", "Install gadget from acme"],
	]);
	// The row itself is one plain reading beside its button: the button is the
	// row's only target.
	const row = tree.root.find((node) => node.type === Row && node.props.label === "gadget");
	const elements = row.findAll(
		(node) => typeof node.type === "string" && (node.props.accessible === true || String(node.type) === "Pressable"),
	);
	expect(elements.map((node) => [String(node.type), node.props.accessibilityLabel])).toEqual([
		["View", "gadget, A gadget"],
		["Pressable", "Install gadget from acme"],
	]);
	hub.on("evener/plugin/install", () => ({
		plugins: [entry("tool", { marketplace: "acme" }), entry("gadget", { marketplace: "acme" })],
	}));
	await act(async () => {
		pressable(tree, "Install gadget from acme")?.props.onPress();
	});
	await act(async () => {});
	expect(hub.calls.at(-1)).toMatchObject({
		method: "evener/plugin/install",
		params: { plugin: "gadget", marketplace: "acme" },
	});
});
