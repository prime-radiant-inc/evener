// The credential-JSON paste sheet (issue #3270): a multiline field can't be
// secure, so the sheet never shows the pasted JSON and covers itself while the
// app is inactive. Only the native edges are mocked; the real sheet renders.
import { useState } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { CredentialPasteSheet } from "./CredentialPasteSheet";

const clipboard = vi.hoisted(() => ({ getStringAsync: vi.fn(async () => "") }));
const appState = vi.hoisted(() => {
	const listeners = new Set<(state: string) => void>();
	return {
		send(state: string) {
			for (const listener of [...listeners]) listener(state);
		},
		listen(listener: (state: string) => void) {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		reset() {
			listeners.clear();
		},
	};
});
vi.mock("expo-clipboard", () => ({ getStringAsync: clipboard.getStringAsync }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: {
		currentState: "active",
		addEventListener: (_event: string, listener: (state: string) => void) => ({
			remove: appState.listen(listener),
		}),
	},
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

function control(tree: ReactTestRenderer, label: string) {
	return tree.root.findAll((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label)[0];
}

/** The sheet over page-owned state, so a paste re-renders it as the page does. */
function Harness() {
	const [value, setValue] = useState("");
	return (
		<CredentialPasteSheet
			title="Set credential JSON"
			kind="credentialJson"
			value={value}
			onChangeText={setValue}
			busy={false}
			canSave={!!value.trim()}
			error={null}
			onSave={() => {}}
			onCancel={() => {}}
		/>
	);
}

it("never renders a pasted credential JSON, only how much was pasted", async () => {
	const secret = '{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\\nMIIEvQIBADANBg"}';
	clipboard.getStringAsync.mockResolvedValue(secret);
	const tree = render(<Harness />);
	const paste = control(tree, "Paste credential JSON");
	if (!paste) throw new Error("no paste control");
	act(() => paste.props.onPress());
	await act(async () => {});
	// No host control carries the secret as a value; it is in no text either.
	expect(tree.root.findAll((node) => typeof node.type === "string" && node.props.value === secret)).toHaveLength(0);
	expect(renderedText(tree)).not.toContain("BEGIN PRIVATE KEY");
	// The sheet says only how much was pasted.
	expect(renderedText(tree)).toContain(`${secret.length} characters`);
});

it("covers the sheet while the app is inactive, so the snapshot holds no secret", () => {
	appState.reset();
	const tree = render(<Harness />);
	const covers = () => tree.root.findAll((node) => node.props.testID === "privacy-cover").length;
	expect(covers()).toBe(0);
	act(() => appState.send("background"));
	expect(covers()).toBe(1);
	act(() => appState.send("active"));
	expect(covers()).toBe(0);
	appState.reset();
});
