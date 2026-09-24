import type { InstanceEntry } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ProviderInstanceGroups } from "./ProviderInstanceGroups";

// The interactive half of the shared listing. Its sibling InstanceRow enforces
// this contract in its props type; this half has to as well, or a caller passing
// neither readOnly nor onSelect compiles and renders a full-width
// tappable-looking row whose button silently does nothing (round-4 M-2).

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    ...overrides,
  };
}

afterEach(cleanup);

test("an interactive row hands its instance name to onSelect", async () => {
  const onSelect = vi.fn();
  render(
    <ProviderInstanceGroups
      instances={[instance({ name: "on-beta", providerId: "anthropic" })]}
      availableProviders={[]}
      onSelect={onSelect}
    />,
  );

  await userEvent.setup().click(screen.getByRole("button", { name: /on-beta/ }));

  expect(onSelect).toHaveBeenCalledWith("on-beta");
});

// A compile-time assertion, checked by tsc: with readOnly unset, onSelect is
// REQUIRED. If the props stop requiring it, the directive below is unused and tsc
// fails (TS2578) - which is exactly how the hole reached review.
test("the props type requires onSelect whenever the listing is not read-only", () => {
  // @ts-expect-error a listing that is not readOnly must be given onSelect
  render(<ProviderInstanceGroups instances={[]} availableProviders={[]} />);
});
