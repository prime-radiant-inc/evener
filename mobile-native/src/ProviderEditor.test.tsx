// ProviderEditor reports success only on a confirmed write: the applied
// verdict the screen's surface returns decides whether the editor closes with
// onSaved or stays open with an unconfirmed-save message. Mounting it over the
// render harness is what pins that, since the actor here is the editor itself,
// not the screen around it.
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { InstanceEntry } from "@evener/appwire-client";
import { ProviderEditor } from "./ProviderEditor";
import { render, renderedText } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());

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

function pressLabel(
  tree: ReturnType<typeof render>,
  label: string,
): void {
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
      onSaved={onSaved}
      onEndpointConflict={() => {}}
      onCancel={() => {}}
    />,
  );
  pressLabel(tree, "Save instance");
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
      onSaved={onSaved}
      onEndpointConflict={() => {}}
      onCancel={() => {}}
    />,
  );
  pressLabel(tree, "Save instance");
  await act(async () => {});
  expect(onSaved).toHaveBeenCalledWith("alpha");
});
