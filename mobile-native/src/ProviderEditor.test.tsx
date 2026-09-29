// ProviderEditor reports success only on a confirmed write: the applied
// verdict the screen's surface returns decides whether the editor closes with
// onSaved or stays open with an unconfirmed-save message. Mounting it over the
// render harness is what pins that, since the actor here is the editor itself,
// not the screen around it.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceEntry, ProviderDescriptor } from "@evener/appwire-client";
import { ProviderEditor } from "./ProviderEditor";
import { render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const instance = {
	name: "alpha",
	providerId: "anthropic",
	protocol: "https",
	auth: "apiKey",
	implicit: false,
	isDefault: true,
	activeSource: "store",
	hasStoredOAuth: false,
	baseUrl: "https://alpha.example",
} as unknown as InstanceEntry;

function pressLabel(tree: ReturnType<typeof render>, label: string): void {
	const target = tree.root
		.findAll((node) => String(node.type) === "Pressable")
		.find((node) => node.props.accessibilityLabel === label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
}

it("stays open and does not report success when the save is unconfirmed", async () => {
	const onSaved = vi.fn();
	const tree = render(
		<ProviderEditor
			instance={instance}
			providers={[]}
			onCreate={async () => true}
			onEdit={async () => false}
			disabled={false}
			canUseConnection={() => true}
			onSaved={onSaved}
			onEndpointConflict={() => {}}
			onCancel={() => {}}
		/>,
	);
	pressLabel(tree, "Save");
	await act(async () => {});
	expect(onSaved).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Save could not be confirmed");
});

it("reports success when the save is confirmed", async () => {
	const onSaved = vi.fn();
	const tree = render(
		<ProviderEditor
			instance={instance}
			providers={[]}
			onCreate={async () => true}
			onEdit={async () => true}
			disabled={false}
			canUseConnection={() => true}
			onSaved={onSaved}
			onEndpointConflict={() => {}}
			onCancel={() => {}}
		/>,
	);
	pressLabel(tree, "Save");
	await act(async () => {});
	expect(onSaved).toHaveBeenCalledWith("alpha");
});

it("keeps the draft when readiness is lost before the save runs", async () => {
	// The render said ready; readiness is lost before the save runs - the
	// window every other mutation entry on the providers screen guards at
	// invocation time (act/whenReady), and the save is the one entry that
	// trusts only its render-time disabled snapshot.
	const onEdit = vi.fn(async () => true);
	const onSaved = vi.fn();
	let ready = true;
	const tree = render(
		<ProviderEditor
			instance={instance}
			providers={[]}
			onCreate={async () => true}
			onEdit={onEdit}
			disabled={false}
			canUseConnection={() => ready}
			onSaved={onSaved}
			onEndpointConflict={() => {}}
			onCancel={() => {}}
		/>,
	);
	const baseUrl = tree.root.findByProps({
		accessibilityLabel: "Base URL",
	});
	act(() => {
		baseUrl.props.onChangeText("https://changed.example");
	});

	ready = false;
	pressLabel(tree, "Save");
	await act(async () => {});

	expect(onEdit).not.toHaveBeenCalled();
	expect(onSaved).not.toHaveBeenCalled();
	// Nothing ran, so nothing reports: no failure copy claims a save was
	// tried, and the draft keeps what was typed for the connection's return.
	expect(renderedText(tree)).not.toContain("Save could not be confirmed");
	expect(tree.root.findByProps({ accessibilityLabel: "Base URL" }).props.value).toBe("https://changed.example");
});

const providers = [
	{ id: "anthropic", name: "Anthropic" },
	{ id: "azure", name: "Azure OpenAI", vars: { "{resource}": "AZURE_RESOURCE" } },
] as unknown as ProviderDescriptor[];

function sectionLabels(tree: ReturnType<typeof render>): string[] {
	// The sheet's own title is the first header; the rest are section labels.
	return tree.root
		.findAllByProps({ accessibilityRole: "header" })
		.slice(1)
		.map((header) => header.props.children);
}

it("adds a provider in a grouped form: a base picker, field rows, Save and Cancel up top", async () => {
	const onCreate = vi.fn(async () => true);
	const onCancel = vi.fn();
	const tree = render(
		<ProviderEditor
			providers={providers}
			onCreate={onCreate}
			onEdit={async () => true}
			disabled={false}
			canUseConnection={() => true}
			onSaved={() => {}}
			onEndpointConflict={() => {}}
			onCancel={onCancel}
		/>,
	);
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("Add provider");
	expect(tree.root.findAll((node) => node.props.accessibilityRole === "radio")).toHaveLength(0);
	pressLabel(tree, "Choose base provider");
	expect(tree.root.findByProps({ accessibilityLabel: "Find provider" })).toBeDefined();
	pressLabel(tree, "Azure OpenAI");
	// Chosen, the picker folds back to one row that reopens it.
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Find provider")).toHaveLength(0);
	const chosen = tree.root.findAll((node) => node.props.accessibilityLabel === "Base provider, Azure OpenAI");
	expect(chosen).not.toHaveLength(0);
	expect(sectionLabels(tree)).toEqual([
		"Base provider",
		"Name",
		"Base URL",
		"AZURE_RESOURCE",
		"API key variable",
		"Credential header",
	]);
	expect(renderedText(tree)).not.toContain("(optional)");
	act(() => tree.root.findByProps({ accessibilityLabel: "Instance name" }).props.onChangeText("work"));
	act(() => tree.root.findByProps({ accessibilityLabel: "AZURE_RESOURCE" }).props.onChangeText("contoso"));
	pressLabel(tree, "Save");
	await act(async () => {});
	expect(onCreate).toHaveBeenCalledOnce();
	pressLabel(tree, "Cancel");
	expect(onCancel).toHaveBeenCalledOnce();
});

it("edits a provider's base URL under an Edit title, and says an emptied URL resets it", () => {
	const tree = render(
		<ProviderEditor
			instance={instance}
			providers={[]}
			onCreate={async () => true}
			onEdit={async () => true}
			disabled={false}
			canUseConnection={() => true}
			onSaved={() => {}}
			onEndpointConflict={() => {}}
			onCancel={() => {}}
		/>,
	);
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("Edit alpha");
	expect(sectionLabels(tree)).toEqual(["Base URL"]);
	act(() => tree.root.findByProps({ accessibilityLabel: "Base URL" }).props.onChangeText(""));
	expect(renderedText(tree)).toContain("Resets the endpoint to the provider’s default.");
});

function mountCreate(overrides: Partial<Parameters<typeof ProviderEditor>[0]> = {}) {
	return render(
		<ProviderEditor
			providers={providers}
			onCreate={async () => true}
			onEdit={async () => true}
			disabled={false}
			canUseConnection={() => true}
			onSaved={() => {}}
			onEndpointConflict={() => {}}
			onCancel={() => {}}
			{...overrides}
		/>,
	);
}

const headerButton = (tree: ReturnType<typeof render>, label: string) =>
	tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label });

it("won't save a new provider until it has a base and a name", () => {
	const tree = mountCreate();
	expect(headerButton(tree, "Save").props.disabled).toBe(true);
	pressLabel(tree, "Choose base provider");
	pressLabel(tree, "Anthropic");
	expect(headerButton(tree, "Save").props.disabled).toBe(true);
	act(() => tree.root.findByProps({ accessibilityLabel: "Instance name" }).props.onChangeText("work"));
	expect(headerButton(tree, "Save").props.disabled).toBe(false);
});

it("says Saving, busy, and holds Cancel while a save is in flight", async () => {
	let finish = (_applied: boolean) => {};
	const onEdit = vi.fn(() => new Promise<boolean>((resolve) => (finish = resolve)));
	const onCancel = vi.fn();
	const tree = render(
		<ProviderEditor
			instance={instance}
			providers={[]}
			onCreate={async () => true}
			onEdit={onEdit}
			disabled={false}
			canUseConnection={() => true}
			onSaved={() => {}}
			onEndpointConflict={() => {}}
			onCancel={onCancel}
		/>,
	);
	pressLabel(tree, "Save");
	await act(async () => {});
	const saving = headerButton(tree, "Saving…");
	expect(saving.props.accessibilityState).toEqual({ disabled: true, busy: true });
	expect(headerButton(tree, "Cancel").props.disabled).toBe(true);
	await act(async () => finish(true));
	expect(headerButton(tree, "Save").props.disabled).toBe(false);
});

it("puts a refused save's reason at the top of the form, where VoiceOver hears it", async () => {
	const tree = mountCreate();
	pressLabel(tree, "Choose base provider");
	pressLabel(tree, "Anthropic");
	act(() => tree.root.findByProps({ accessibilityLabel: "Instance name" }).props.onChangeText("work"));
	act(() => tree.root.findByProps({ accessibilityLabel: "Credential header" }).props.onChangeText("Bearer sk-live"));
	pressLabel(tree, "Save");
	await act(async () => {});
	const page = tree.root.findByType("ScrollView" as never);
	const first = page.findAll((node) => String(node.type) === "Text")[0];
	expect(first?.props.children).toBe(
		"Credential header must reference a $VARIABLE or run a $(command), never a literal secret.",
	);
	expect(first?.props.accessibilityLiveRegion).toBe("polite");
});

it("says when no provider matches the search, and a second tap on the base row folds the picker", () => {
	const tree = mountCreate();
	pressLabel(tree, "Choose base provider");
	act(() => tree.root.findByProps({ accessibilityLabel: "Find provider" }).props.onChangeText("zzz"));
	expect(renderedText(tree)).toContain("No providers match.");
	pressLabel(tree, "Choose base provider");
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Find provider")).toHaveLength(0);
});

it("leads from each field to the next, and saves from the last", async () => {
	const onCreate = vi.fn(async () => true);
	const tree = mountCreate({ onCreate });
	pressLabel(tree, "Choose base provider");
	pressLabel(tree, "Azure OpenAI");
	const keys = ["Instance name", "Base URL", "AZURE_RESOURCE", "API key environment variable", "Credential header"];
	const inputs = keys.map((key) => tree.root.findByProps({ accessibilityLabel: key }));
	expect(inputs.map((input) => input.props.returnKeyType)).toEqual(["next", "next", "next", "next", "done"]);
	act(() => inputs[0]?.props.onChangeText("work"));
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Credential header" }).props.onSubmitEditing();
	});
	expect(onCreate).toHaveBeenCalledOnce();
});

it("answers a return key on an unfinished form with the reason, and sends nothing", async () => {
	const onCreate = vi.fn(async () => true);
	const tree = mountCreate({ onCreate });
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Credential header" }).props.onSubmitEditing();
	});
	expect(onCreate).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Select an available base provider.");
});
