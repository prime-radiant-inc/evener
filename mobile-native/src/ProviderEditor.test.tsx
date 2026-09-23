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
      canUseConnection={() => true}
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
      canUseConnection={() => true}
      onSaved={onSaved}
      onEndpointConflict={() => {}}
      onCancel={() => {}}
    />,
  );
  pressLabel(tree, "Save instance");
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
    accessibilityLabel: "Base URL (optional)",
  });
  act(() => {
    baseUrl.props.onChangeText("https://changed.example");
  });

  ready = false;
  pressLabel(tree, "Save instance");
  await act(async () => {});

  expect(onEdit).not.toHaveBeenCalled();
  expect(onSaved).not.toHaveBeenCalled();
  // Nothing ran, so nothing reports: no failure copy claims a save was
  // tried, and the draft keeps what was typed for the connection's return.
  expect(renderedText(tree)).not.toContain("Save could not be confirmed");
  expect(
    tree.root.findByProps({ accessibilityLabel: "Base URL (optional)" })
      .props.value,
  ).toBe("https://changed.example");
});
