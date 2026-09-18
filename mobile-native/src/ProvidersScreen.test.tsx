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
