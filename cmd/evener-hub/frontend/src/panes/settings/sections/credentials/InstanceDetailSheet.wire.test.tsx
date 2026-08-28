// InstanceDetailSheet.test.tsx builds its InstanceEntry values by hand. This
// file is the other half: the sheet's affordances are keyed on literal
// authModes and activeSource values (spec §11.3), and every instance here is
// one the hub actually sent — cmd/evener-hub's
// TestAuthWireFixturesMatchTheHubHandler drives the real
// evener/instance/list handler and re-verifies the corpus on every run.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import { hubInstance, hubInstanceEntries } from "../../../../protocol/testing/hubWireFixtures";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { InstanceDetailSheet } from "./InstanceDetailSheet";

function renderHubInstance(name: string) {
  credentialsStore.setState({ instances: [hubInstance(name)] });
  render(
    <InstanceDetailSheet
      name={name}
      onClose={vi.fn()}
      onTestCredentials={vi.fn()}
      onSetApiKey={vi.fn()}
      onOAuthStart={vi.fn()}
      onEdit={vi.fn()}
      onClear={vi.fn()}
      onClearStoredKey={vi.fn()}
      onRemove={vi.fn()}
      onSetDefault={vi.fn()}
    />,
  );
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  connectionStore.getState().connect(new FakeClient("ready"));
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("the sheet's affordances follow the hub's own answers", () => {
  test.each([
    // name, key action, OAuth action, Clear
    ["openai", true, false, false],
    ["openai-codex", false, true, true],
    ["anthropic", true, false, true],
    ["vertexish", false, false, false],
    // gateway is auth = "none": nothing to set, nothing to clear.
    ["gateway", false, false, false],
    // headered is an optional-bearer gateway, so a key is still offered.
    ["headered", true, false, false],
    ["unkeyed", true, false, false],
  ] as const)("%s", (name, wantKey, wantOAuth, wantClear) => {
    renderHubInstance(name);
    expect(screen.queryByRole("button", { name: /(set|replace) key/i }) !== null).toBe(wantKey);
    expect(screen.queryByRole("button", { name: /(sign in|refresh oauth)/i }) !== null).toBe(wantOAuth);
    expect(screen.queryByRole("button", { name: /^clear/i }) !== null).toBe(wantClear);
  });

  test("the key button says Replace only when the hub reports a stored file key", () => {
    renderHubInstance("anthropic"); // activeSource "store", hasStoredFile true
    expect(screen.getByRole("button", { name: /replace key/i })).toBeTruthy();
    cleanup();
    renderHubInstance("unkeyed"); // nothing stored
    expect(screen.getByRole("button", { name: /set key/i })).toBeTruthy();
  });

  test("every row the hub sends offers at least one way to reach it", () => {
    // A row with no key action, no OAuth and no Clear is one the sheet can
    // only display; that is correct for adc and auth-none, and a bug for
    // anything the user is expected to configure.
    for (const instance of hubInstanceEntries()) {
      const modes = instance.authModes ?? [];
      const reachable =
        modes.includes("apiKey") || modes.includes("oauth") || modes.includes("none") || modes.includes("adc");
      expect(reachable, `${instance.name} declares authModes ${JSON.stringify(modes)}`).toBe(true);
    }
  });
});
