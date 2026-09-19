// ProvidersScreen's provider list issues its listing read from a mount effect,
// and React flushes a child's passive effects before its parent's: that read
// only finds a bound credential store because useCredentialStore binds it from
// a layout effect. The ordering is observable only by mounting the real screen,
// which renderNative.testkit makes possible; every native edge the screen
// reaches is mocked here and nowhere else.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceListResponse } from "@evener/appwire-client";
import { ProvidersScreen } from "./ProvidersScreen";
import { render, renderedText, scriptedClient } from "./renderNative.testkit";

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

const rows: InstanceListResponse = {
	instances: [
		{
			name: "work",
			providerId: "anthropic",
			protocol: "https",
			auth: "apiKey",
			implicit: false,
			isDefault: true,
			activeSource: "store",
			hasStoredOAuth: false,
			credentialRequired: true,
		},
	],
	availableProviders: [],
	diagnostics: ["from the hub"],
};

it("mounts on a ready client and issues and publishes the listing read", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	// The read is issued from the list's mount effect and answered asynchronously.
	await act(async () => {});
	expect(hub.methods).toEqual(["evener/instance/list"]);
	// Published: the row the store applied and the listing's diagnostics are
	// what the screen renders. A read that found no bound client would throw
	// and leave the screen on its empty state instead.
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).toContain("from the hub");
});

it("ready -> reconnecting keeps the screen tree mounted and shows the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");
	expect(renderedText(tree)).not.toContain("Reconnect");

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	// The list stayed mounted through the flap (never replaced by the wall) ...
	expect(text).toContain("work");
	// ... behind a banner announcing it.
	expect(text).toContain("reconnecting");
	expect(text).toContain("Reconnect");
});

it("reconnecting -> ready removes the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("Reconnect");

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).not.toContain("Reconnect");
});

it("a fatal (protocol) close replaces the mounted list with the wall", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");

	// `client: hub.client` deliberately kept set - a real hubConnection.ts
	// keeps it set on "closed" too, and this test must prove the wall comes
	// from `fatal`, not from `client` dropping to null.
	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	expect(text).not.toContain("work");
	expect(text).toContain("Connect to");
	expect(text).toContain("to manage providers.");
});

it("keeps the provider wall through a fatal retry until the replacement is ready", async () => {
	const hub = scriptedClient(rows);
	const replacement = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});

	harness.connection = {
		...harness.connection,
		client: replacement.client,
		state: "connecting",
		fatal: false,
	};
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("Connect to");
	expect(renderedText(tree)).toContain("to manage providers.");
	expect(replacement.methods).toEqual([]);

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	expect(replacement.methods.length).toBeGreaterThan(0);
	expect(
		replacement.methods.every((method) => method === "evener/instance/list"),
	).toBe(true);
});

it("a flap disables provider mutation controls, not only OAuth sign-in", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		fatal: false,
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	const row = tree.root.findAll(
		(node) =>
			(node.type as unknown) === "Pressable" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("work"),
	)[0];
	await act(async () => {
		row.props.onPress();
	});
	// Named by their button text, which Action forwards as accessibilityLabel
	// when no separate `label` is given (ui.tsx).
	const label = (name: string) =>
		tree.root.findByProps({ accessibilityLabel: name });
	expect(label("Add provider instance").props.disabled).toBe(false);
	expect(label("Test credentials").props.disabled).toBe(false);
	expect(label("Edit instance").props.disabled).toBe(false);
	expect(label("Clear credentials").props.disabled).toBe(false);
	expect(label("Remove instance").props.disabled).toBe(false);

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(label("Add provider instance").props.disabled).toBe(true);
	expect(label("Test credentials").props.disabled).toBe(true);
	expect(label("Edit instance").props.disabled).toBe(true);
	expect(label("Clear credentials").props.disabled).toBe(true);
	expect(label("Remove instance").props.disabled).toBe(true);
});
