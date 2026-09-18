// The mounting test ProvidersScreen never had: the D9 fix moved the credential
// store's connection binding into a layout effect (useCredentialStore), and the
// property that fix buys - the store knows its client before the provider list's
// mount effect reads through it - cannot be exercised without mounting the real
// screen. react-test-renderer (renderNative.testkit) provides that; every native
// edge the screen reaches is mocked here and nowhere else.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { AnyNotification, InstanceListResponse } from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProvidersScreen } from "./ProvidersScreen";
import { render, renderedText } from "./renderNative.testkit";

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

function scriptedClient(methods: string[]): ConversationClientLike {
	return {
		request: async (method: string) => {
			methods.push(method);
			return rows;
		},
		onNotification: (_handler: (n: AnyNotification) => void) => () => {},
	} as ConversationClientLike;
}

it("mounts on a ready client and issues and publishes the listing read", async () => {
	const methods: string[] = [];
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: scriptedClient(methods),
		state: "ready",
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	// The read is issued from the list's mount effect and answered asynchronously.
	await act(async () => {});
	expect(methods).toEqual(["evener/instance/list"]);
	// Published: the row the store applied, and the listing's diagnostics, are
	// what the screen renders. Before the D9 fix this read ran on an unbound
	// store, threw, and left the screen on its empty state.
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).toContain("from the hub");
});
