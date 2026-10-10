import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { organizationJournal } from "../nativeOrganization";
import { renderHook, until } from "../renderNative.testkit";
import { organizationHub, SESSION_ID } from "./organizationTestUtils";
import { useBoardOrganization } from "./useBoardOrganization";

const harness = vi.hoisted(() => {
	const values = new Map<string, string>();
	return {
		values,
		storage: {
			getItemSync: (key: string) => values.get(key) ?? null,
			setItemSync: (key: string, value: string) => void values.set(key, value),
			removeItemSync: (key: string) => void values.delete(key),
			getAllKeysSync: () => [...values.keys()],
		},
		connection: {} as Record<string, unknown>,
		focused: true,
	};
});
vi.mock("expo-sqlite/kv-store", () => ({ Storage: harness.storage }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "change-1" }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", () => ({ useIsFocused: () => harness.focused }));

const hub = { id: "hub-1", name: "Work hub" };
beforeEach(() => {
	harness.values.clear();
	harness.focused = true;
});

it("settles a change another screen left unresolved once the Board is focused and connected", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	expect(hook.result.current.ready).toBe(true);
	expect(server.reads.map((read) => read.resource)).toEqual(["manifest", "location", "manifest"]);
	hook.unmount();
});

it("reads nothing while the Board is covered, and settles when it comes back", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.focused = false;
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await act(async () => {});
	expect(server.reads).toEqual([]);
	expect(hook.result.current.ready).toBe(false);
	harness.focused = true;
	hook.rerender();
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	hook.unmount();
});

it("settles again when the connection comes back", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({
		kind: "archive",
		params: { kind: "session", id: SESSION_ID, archived: true },
	});
	harness.connection = { client: server.client, activeProfile: hub, state: "reconnecting" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await act(async () => {});
	expect(server.reads).toEqual([]);
	expect(hook.result.current.actions).toBeNull();
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	hook.rerender();
	await until(() => expect(organizationJournal("hub-1").load()).toBeNull());
	hook.unmount();
});

it("keeps organization closed while a change it can't check here is unresolved", async () => {
	const server = organizationHub();
	organizationJournal("hub-1").begin({ kind: "deleteSession", params: { ref: `local:${SESSION_ID}` } });
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	// The journal's own text proves the settle ran and refused; the Board never shows it.
	await until(() => expect(hook.result.current.state?.error).toMatch(/Could not confirm/));
	expect(hook.result.current.ready).toBe(false);
	expect(organizationJournal("hub-1").load()).not.toBeNull();
	expect(server.reads).toEqual([]);
	hook.unmount();
});

it("stops counting a binding as current once the Board is covered or the connection drops", async () => {
	const server = organizationHub();
	harness.connection = { client: server.client, activeProfile: hub, state: "ready" };
	const hook = renderHook(() => useBoardOrganization("hub-1"));
	await act(async () => {});
	const first = hook.result.current.isCurrent;
	expect(first()).toBe(true);
	harness.focused = false;
	hook.rerender();
	expect(first()).toBe(false);
	expect(hook.result.current.isCurrent()).toBe(false);
	harness.focused = true;
	hook.rerender();
	expect(hook.result.current.isCurrent()).toBe(true);
	harness.connection = { client: server.client, activeProfile: hub, state: "reconnecting" };
	hook.rerender();
	expect(hook.result.current.isCurrent()).toBe(false);
	hook.unmount();
});
