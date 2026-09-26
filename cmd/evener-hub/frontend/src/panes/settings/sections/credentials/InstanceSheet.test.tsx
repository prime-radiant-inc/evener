import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "@evener/appwire-client";
import { ErrorEndpointConflict, ErrorInstanceRenamePersisted, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { InstanceSheet } from "./InstanceSheet";

/** The refusal these controls use instead of the native attribute: a click that
 * starts work must not drop the keyboard, so the pending/busy state is
 * aria-disabled (Button/Switch swallow the activation themselves). */
const isRefused = (el: HTMLElement): boolean => el.getAttribute("aria-disabled") === "true";

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

/** A row the hub cannot key: an endpoint the user can see and NO
 * endpointFingerprint at all. The key is deleted rather than left out of the
 * literal on purpose - the instance() helpers in the neighbouring suites stand
 * in an `fp-fixture` default, and a row carrying a fingerprint takes the other
 * arm of draftIdentity, proving nothing about the one this suite is written
 * for. The absence is asserted in the tests that use it, so a helper that
 * starts injecting one fails loudly instead of quietly retargeting them. */
function unkeyable(fields: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "providerId">): InstanceEntry {
  const row = instance(fields);
  delete row.endpointFingerprint;
  return row;
}

function noopHandlers() {
  return {
    onTestCredentials: vi.fn(),
    onSetApiKey: vi.fn(),
    onSetCredentialJson: vi.fn(),
    onOAuthStart: vi.fn(),
    onRenamed: vi.fn(),
    onClear: vi.fn(),
    onClearStoredKey: vi.fn(),
    onRemove: vi.fn(),
    onSetDefault: vi.fn(),
    onToggleModel: vi.fn(),
    onRefreshModels: vi.fn(),
  };
}

function renderSheet(
  inst: InstanceEntry | null,
  extra: Partial<Parameters<typeof InstanceSheet>[0]> = {},
  providers: ProviderDescriptor[] = [],
) {
  const handlers = noopHandlers();
  const onClose = vi.fn();
  // The section has these rows on screen because it read them on the
  // connection the store would write to now: seeding them with the stale mark
  // still set would describe a replaced connection, which is not the state
  // these tests are about - and the store refuses writes issued from one.
  credentialsStore.setState({
    instances: inst === null ? [] : [inst],
    availableProviders: providers,
    listingFromPreviousConnection: false,
  });
  const tree = (name: string | null) => (
    <>
      <Toast />
      <InstanceSheet name={name} onClose={onClose} {...handlers} {...extra} />
    </>
  );
  const { rerender } = render(tree(inst?.name ?? null));
  /** Points the sheet at another instance by name, the way the section does
   * when it re-selects the new name after a rename. */
  const selectName = (name: string) => rerender(tree(name));
  /** Drops the section's selection, the way it does on Escape, the scrim or
   * the close button. The sheet stays mounted with no name, so nothing about
   * it unmounts on dismissal. */
  const dismiss = () => rerender(tree(null));
  return { handlers, onClose, selectName, dismiss };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  resetToastStoreForTests();
  // Every instance mutation now stamps originClientId, so the assertions that
  // pin the exact params need an identity that cannot vary.
  setMutationClientIdentityForTests("test-tab");
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("visibility", () => {
  test("renders nothing when name is null", () => {
    renderSheet(null);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  test("renders a dialog named after the instance, with the default chip when set", () => {
    renderSheet(instance({ name: "openai-work", providerId: "openai", isDefault: true }));
    expect(screen.getByRole("dialog", { name: "openai-work" })).toBeTruthy();
    expect(screen.getByText(/default/i)).toBeTruthy();
  });

  // The sheet is the whole screen on mobile and the only place Remove would
  // have been, so the badge explaining an environment-backed instance has to
  // be here too - not only on the row behind it.
  test("an environment-backed instance is badged 'from environment'", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" }));
    expect(screen.getByText("from environment")).toBeTruthy();
  });

  test("an implicit instance credentialed through the UI carries no badge", () => {
    renderSheet(
      instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "store", hasStoredFile: true }),
    );
    expect(screen.queryByText("from environment")).toBeNull();
  });

  test("a signed-in Codex account carries no badge", () => {
    renderSheet(
      instance({
        name: "openai-codex",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        implicit: true,
        activeSource: "oauth",
        hasStoredOAuth: true,
      }),
    );
    expect(screen.queryByText("from environment")).toBeNull();
  });

  test("a non-implicit instance carries no such badge", () => {
    renderSheet(instance({ name: "work", providerId: "groq", base: "groq", implicit: false }));
    expect(screen.queryByText("from environment")).toBeNull();
  });

  test("the close button calls onClose", async () => {
    const user = userEvent.setup();
    const { onClose } = renderSheet(instance({ name: "a", providerId: "x" }));
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  test("closes itself when the instance disappears from the store", async () => {
    const { onClose } = renderSheet(instance({ name: "a", providerId: "x" }));
    expect(screen.getByRole("dialog", { name: "a" })).toBeTruthy();
    act(() => credentialsStore.setState({ instances: [] }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  test("the heading dot reads idle for a configured instance", () => {
    renderSheet(instance({ name: "a", providerId: "x", hasStoredFile: true, activeSource: "store" }));
    expect(screen.getByRole("img", { name: "Idle" })).toBeTruthy();
  });

  test("the heading dot reads ended when a required key is missing", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "none", credentialRequired: true }));
    expect(screen.getByRole("img", { name: "Ended" })).toBeTruthy();
  });
});

describe("credential display", () => {
  test("a stored key behind a higher-ranked source shows effective + shadowed chips", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "anthropic",
        hasStoredFile: true,
        activeSource: "api_key",
      }),
    );
    expect(screen.getByText("effective")).toBeTruthy();
    expect(screen.getByText("shadowed")).toBeTruthy();
    expect(screen.getByText(/Configured via providers\.toml/)).toBeTruthy();
    expect(screen.getByText(/Configured via stored API key/)).toBeTruthy();
  });

  test("an oauth layer carries the stored email", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        hasStoredOAuth: true,
        storedEmail: "me@x.com",
        activeSource: "oauth",
      }),
    );
    expect(screen.getByText(/Configured via OAuth \(me@x\.com\)/)).toBeTruthy();
  });

  test("an unconfigured instance shows its label instead of layers", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "none" }));
    expect(screen.getByText("Not configured")).toBeTruthy();
    expect(screen.queryByText("effective")).toBeNull();
  });
});

describe("actions are conditionally rendered", () => {
  test("Set key only when authModes includes apiKey", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["oauth"] }));
    expect(screen.queryByRole("button", { name: /set key|replace key/i })).toBeNull();
  });

  test("Sign in… only when authModes includes oauth", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["apiKey"] }));
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });

  test("Clear only when activeSource is store or oauth", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "env:X_API_KEY", envVar: "X_API_KEY" }));
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  test("Clear once a stored key is what resolves", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true }));
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
  });

  // #713: a stray stored key can sit shadowed behind an active oauth/adc
  // login - Clear stored key is the sheet's narrow affordance for exactly
  // that state, distinct from Clear (which for a signed-in Codex row would
  // drop the login instead of the stray key).
  test("Clear stored key when a stored key is shadowed behind an active OAuth login", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        activeSource: "oauth",
        hasStoredFile: true,
        hasStoredOAuth: true,
      }),
    );
    expect(screen.getByRole("button", { name: "Clear stored key" })).toBeTruthy();
  });

  test("Clear stored key when a stored key is shadowed behind ADC", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "adc", hasStoredFile: true }));
    expect(screen.getByRole("button", { name: "Clear stored key" })).toBeTruthy();
  });

  // A gcp-adc instance's stored credential is a JSON document, not an API
  // key - the danger-zone button must call it that, same as the sheet's own
  // Set/Replace credential JSON action above (roborev round 3, F3).
  test("Clear stored credential JSON for a gcp-adc instance with a stored file shadowed behind ADC", () => {
    renderSheet(
      instance({
        name: "vertex",
        providerId: "google-vertex",
        auth: "gcp-adc",
        authModes: ["adc", "credentialJson"],
        activeSource: "adc",
        hasStoredFile: true,
      }),
    );
    expect(screen.getByRole("button", { name: "Clear stored credential JSON" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear stored key" })).toBeNull();
  });

  test("no Clear stored key when the stored key IS the active source", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "store", hasStoredFile: true }));
    expect(screen.queryByRole("button", { name: "Clear stored key" })).toBeNull();
  });

  test("no Clear stored key when nothing is stored", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "oauth", hasStoredFile: false }));
    expect(screen.queryByRole("button", { name: "Clear stored key" })).toBeNull();
  });

  test("Remove is offered for a non-implicit instance", () => {
    renderSheet(instance({ name: "a", providerId: "x" }));
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  // Removing an environment-backed instance is refused server-side: the
  // variable that makes it exist would put it straight back. The form stays,
  // since editing it writes a shadow rather than changing the instance.
  test("an environment-backed instance offers the form but no Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" }));
    expect(screen.getByLabelText("Base URL")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });

  // The account a user adds through the UI is theirs to remove: the credential
  // is a file under the instance name (auth/<name>.json, credentials.toml), so
  // removing the instance is exactly how the account goes away.
  test("an implicit instance credentialed through the UI offers Remove", () => {
    renderSheet(
      instance({
        name: "openai-codex",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        implicit: true,
        activeSource: "oauth",
        hasStoredOAuth: true,
      }),
    );
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  // An instance that exists without a credential is not the user's to remove,
  // however its store layer looks: the reload re-derives it and the row comes
  // back wearing the badge this affordance just said it did not have, with the
  // key gone. Clear is the action for that key.
  test("a keyless-capable instance with a stored key keeps the badge and no Remove", () => {
    renderSheet(
      instance({
        name: "ollama",
        providerId: "ollama",
        auth: "optional-bearer",
        implicit: true,
        activeSource: "store",
        hasStoredFile: true,
        credentialRequired: false,
      }),
    );
    expect(screen.getByText("from environment")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
  });

  // The danger zone is Clear + Remove under a divider; an implicit instance
  // the environment supplies with nothing stored offers neither, so the divider
  // must go too rather than trailing an empty section.
  test("no danger-zone divider when the instance offers neither Clear nor Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" }));
    expect(document.querySelectorAll("hr").length).toBe(0);
  });

  // The other side of the allow-list: an implicit instance the environment does
  // NOT supply - a bearer row resolving no credential at all (none/empty/
  // unknown) - is the user's own, so Remove and its divider stay. The old
  // deny-list classified `none` as environment-backed and refused Remove with
  // nothing to say.
  test("an implicit instance with no environment-supplied source keeps Remove and its divider", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "none" }));
    expect(screen.queryByText("from environment")).toBeNull();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
    expect(document.querySelectorAll("hr").length).toBe(1);
  });

  test("the danger-zone divider stays for a stored-key instance, which offers Clear and Remove", () => {
    renderSheet(
      instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "store", hasStoredFile: true }),
    );
    expect(document.querySelectorAll("hr").length).toBe(1);
    expect(screen.getByRole("button", { name: "Clear" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });

  test("the danger-zone divider stays when Clear stored key alone is offered", () => {
    renderSheet(
      instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "adc", hasStoredFile: true }),
    );
    expect(document.querySelectorAll("hr").length).toBe(1);
    expect(screen.getByRole("button", { name: "Clear stored key" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  test("make default only when not already default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: false }));
    expect(screen.getByRole("button", { name: /make default/i })).toBeTruthy();
  });

  test("make default hidden when already default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: true }));
    expect(screen.queryByRole("button", { name: /make default/i })).toBeNull();
  });
});

describe("action labels follow stored state", () => {
  test("'Set key' when no stored key exists", () => {
    renderSheet(instance({ name: "a", providerId: "x", authModes: ["apiKey"], hasStoredFile: false }));
    expect(screen.getByRole("button", { name: "Set key" })).toBeTruthy();
  });

  test("'Replace key' whenever a stored key exists, and the sheet says that key is shadowed", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "x",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "api_key",
      }),
    );
    expect(screen.getByRole("button", { name: "Replace key" })).toBeTruthy();
    // Replacing a key that providers.toml outranks changes nothing the
    // instance actually uses, so the shadowed chip has to be on screen next
    // to the offer.
    expect(screen.getByText("shadowed")).toBeTruthy();
  });

  test("'Sign in…' when no OAuth is stored", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
        hasStoredOAuth: false,
      }),
    );
    expect(screen.getByRole("button", { name: "Sign in…" })).toBeTruthy();
  });

  test("'Refresh OAuth' once signed in", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        authModes: ["oauth"],
        hasStoredOAuth: true,
        activeSource: "oauth",
      }),
    );
    expect(screen.getByRole("button", { name: "Refresh OAuth" })).toBeTruthy();
  });

  // authModesFor maps each auth scheme to a fixed, non-overlapping set, so a
  // bearer instance is never oauth-capable however its credential is stored.
  test("a bearer-auth instance never offers Sign in, even with a stored key", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "openai",
        auth: "bearer",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    expect(screen.queryByRole("button", { name: /sign in|refresh oauth/i })).toBeNull();
  });

  test("'Set credential JSON' for a gcp-adc instance with nothing stored", () => {
    renderSheet(
      instance({
        name: "vertex",
        providerId: "google-vertex",
        auth: "gcp-adc",
        authModes: ["adc", "credentialJson"],
        hasStoredFile: false,
      }),
    );
    expect(screen.getByRole("button", { name: "Set credential JSON" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Set key" })).toBeNull();
  });

  test("'Replace credential JSON' and the stored-credential label once one is stored", () => {
    renderSheet(
      instance({
        name: "vertex",
        providerId: "google-vertex",
        auth: "gcp-adc",
        authModes: ["adc", "credentialJson"],
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    expect(screen.getByRole("button", { name: "Replace credential JSON" })).toBeTruthy();
    // A regex, not an exact string: the layer's "↳ " prefix and the label
    // render as sibling text nodes in the same <span> (same reason the
    // sibling assertions above, at :119-120 and :134, use a regex too).
    expect(screen.getByText(/Configured via stored credential JSON/)).toBeTruthy();
  });

  test("the credential JSON action calls onSetCredentialJson", async () => {
    const { handlers } = renderSheet(
      instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc", authModes: ["adc", "credentialJson"] }),
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Set credential JSON" }));
    expect(handlers.onSetCredentialJson).toHaveBeenCalled();
  });
});

describe("action callbacks fire", () => {
  test("clicking each action calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({
        name: "a",
        providerId: "anthropic",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Replace key" }));
    expect(handlers.onSetApiKey).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(handlers.onClear).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(handlers.onRemove).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /make default/i }));
    expect(handlers.onSetDefault).toHaveBeenCalled();
  });

  test("clicking Clear stored key calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({
        name: "a",
        providerId: "openai-codex",
        activeSource: "oauth",
        hasStoredFile: true,
        hasStoredOAuth: true,
      }),
    );
    await user.click(screen.getByRole("button", { name: "Clear stored key" }));
    expect(handlers.onClearStoredKey).toHaveBeenCalled();
  });

  test("clicking Sign in calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(
      instance({ name: "a", providerId: "openai-codex", auth: "oauth-openai-codex", authModes: ["oauth"] }),
    );
    await user.click(screen.getByRole("button", { name: "Sign in…" }));
    expect(handlers.onOAuthStart).toHaveBeenCalled();
  });

  test("clicking Test credentials calls its handler", async () => {
    const user = userEvent.setup();
    const { handlers } = renderSheet(instance({ name: "a", providerId: "x" }));
    await user.click(screen.getByRole("button", { name: "Test credentials" }));
    expect(handlers.onTestCredentials).toHaveBeenCalledTimes(1);
  });

  test("pending verification disables only the Test credentials action", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), { testCredentialsPending: true });
    expect(isRefused(screen.getByRole("button", { name: "Testing credentials…" }))).toBe(true);
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("a credential test result renders as a status line", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), {
      testCredentialsResult: { provider: "a", status: "success", message: "Credentials verified." },
    });
    expect(screen.getByRole("status").textContent).toContain("Credentials verified.");
  });

  test("an unknown test status is sanitized to the endpoint-failure message", () => {
    renderSheet(instance({ name: "a", providerId: "x" }), {
      testCredentialsResult: { provider: "a", status: "garbage", message: "raw provider prose" },
    });
    const line = screen.getByRole("status");
    expect(line.textContent).toContain("The provider endpoint could not be reached.");
    expect(line.textContent).not.toContain("raw provider prose");
  });
});

// writesRefused is the wire's "providers.toml cannot be written" flag
// (InstanceListResponse, spec §11.3): it gates the evener/instance/* writes
// only. Set key/Sign in/Clear/Clear stored key/Test credentials write the
// credentials store or an OAuth record, never providers.toml, so they stay
// live.
describe("writesRefused disables instance-CRUD actions only", () => {
  test("disables Remove and make default", () => {
    renderSheet(instance({ name: "a", providerId: "x", isDefault: false }), { writesRefused: true });
    expect((screen.getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /make default/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  test("leaves Test credentials, Set/Replace key, and Clear enabled", () => {
    renderSheet(
      instance({
        name: "a",
        providerId: "x",
        authModes: ["apiKey"],
        hasStoredFile: true,
        activeSource: "store",
      }),
      { writesRefused: true },
    );
    expect(isRefused(screen.getByRole("button", { name: "Test credentials" }))).toBe(false);
    expect((screen.getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("leaves Clear stored key enabled", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "adc", hasStoredFile: true }), {
      writesRefused: true,
    });
    expect((screen.getByRole("button", { name: "Clear stored key" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("an environment-backed implicit instance under writesRefused still has no Remove button at all", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "env:GROQ_API_KEY" }), {
      writesRefused: true,
    });
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });
});

describe("the form", () => {
  const OPENAI: ProviderDescriptor = { id: "openai", protocol: "openai-chat", auth: "bearer", implicit: true };
  // BASE_URL's key differs from its label on purpose: a template var is
  // KEYED by placeholder name and LABELLED by the env var name the docs tell
  // users to set. With key == label everywhere, a component that mixed the
  // two up would still pass (real templates differ - see vars_env in
  // llm/registry/data/providers_overlay.toml).
  const VERTEX: ProviderDescriptor = {
    id: "google-vertex-anthropic",
    protocol: "anthropic",
    auth: "gcp-adc",
    implicit: true,
    vars: {
      GOOGLE_VERTEX_PROJECT: "GOOGLE_VERTEX_PROJECT",
      GOOGLE_VERTEX_LOCATION: "GOOGLE_VERTEX_LOCATION",
      BASE_URL: "GOOGLE_VERTEX_BASE_URL",
    },
  };
  const WORK = instance({
    name: "work",
    providerId: "openai",
    protocol: "openai-responses",
    surface: "generic",
    baseUrl: "https://gw.example.test/v1",
    apiKeyEnv: "PORTKEY_KEY",
    credentialHeader: "Authorization=Bearer $PORTKEY_KEY",
    hasStoredFile: true,
    activeSource: "store",
  });
  const OTHER = instance({ name: "other", providerId: "openai", baseUrl: "https://other.example.test" });

  function field(label: string): HTMLInputElement {
    return screen.getByLabelText(label) as HTMLInputElement;
  }
  function select(label: string): HTMLSelectElement {
    return screen.getByLabelText(label) as HTMLSelectElement;
  }
  function saveButton(): HTMLButtonElement {
    return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
  }
  /** Waits for the save to reach the wire and answers with the params it
   * carried. The params are asserted against this OUTSIDE the fake's handler:
   * a throw inside the handler is only a rejected request, which the sheet
   * catches into a "Save failed" toast, so an assertion in there can never
   * fail the test. */
  async function sentEditParams(fake: FakeClient): Promise<unknown> {
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/edit")).toBe(true));
    return fake.calls.find((c) => c.method === "evener/instance/edit")?.params;
  }
  /** A save the test finishes by hand, so the sheet can be dismissed or
   * re-pointed while the request is still in flight. */
  function deferredEdit(): {
    fake: FakeClient;
    finish: (list: InstanceListResponse) => void;
    fail: (err: Error) => void;
  } {
    const fake = new FakeClient("ready");
    let resolve!: (list: InstanceListResponse) => void;
    let reject!: (err: Error) => void;
    fake.on("evener/instance/edit", () => {
      return new Promise<InstanceListResponse>((r, j) => {
        resolve = r;
        reject = j;
      });
    });
    connectionStore.getState().connect(fake);
    return { fake, finish: (list) => resolve(list), fail: (err) => reject(err) };
  }
  /** Watches the document for the removal of nodes that have to stay put, and
   * answers with the labels of the ones that were taken out. A before/after
   * snapshot cannot see a transient unmount, and the transient frame is the
   * whole defect: an unmounted panel replays its slide-in from off-screen. */
  function watchRemovals(watched: Record<string, Node>): () => string[] {
    const gone: string[] = [];
    // Called from the observer's own microtask (and once more at the end for
    // undelivered records), so the removed subtree still contains what it
    // took with it - containment read later would miss a re-parented node.
    const collect = (records: MutationRecord[]): void => {
      for (const record of records) {
        for (const removed of record.removedNodes) {
          for (const [label, node] of Object.entries(watched)) {
            if (removed === node || removed.contains(node)) gone.push(label);
          }
        }
      }
    };
    const observer = new MutationObserver(collect);
    observer.observe(document.body, { childList: true, subtree: true });
    return () => {
      collect(observer.takeRecords());
      observer.disconnect();
      return gone;
    };
  }

  test("prefills every field from the instance and shows the base provider as a fact", () => {
    renderSheet(WORK, {}, [OPENAI]);
    expect(field("Name").value).toBe("work");
    expect(field("Base URL").value).toBe("https://gw.example.test/v1");
    expect(select("Protocol").value).toBe("openai-responses");
    expect(select("Surface").value).toBe("generic");
    expect(field("API key environment variable").value).toBe("PORTKEY_KEY");
    expect(field("Credential header").value).toBe("Authorization=Bearer $PORTKEY_KEY");
    // "openai" is also a Surface option's text, so read the meta row's value cell.
    expect(screen.getByText("Base provider").nextElementSibling?.textContent).toBe("openai");
  });

  test("renders one input per base-provider variable, labelled by env var name, plus authored extras", () => {
    renderSheet(
      instance({ name: "v", providerId: "google-vertex-anthropic", vars: { GOOGLE_VERTEX_PROJECT: "p1", EXTRA: "e" } }),
      {},
      [VERTEX],
    );
    expect(field("GOOGLE_VERTEX_PROJECT").value).toBe("p1");
    expect(field("GOOGLE_VERTEX_LOCATION").value).toBe("");
    expect(field("EXTRA").value).toBe("e");
    // Labelled by env var name, keyed by template name: the row exists under
    // the label, and nothing renders under the bare key.
    expect(field("GOOGLE_VERTEX_BASE_URL").value).toBe("");
    expect(screen.queryByLabelText("BASE_URL")).toBeNull();
  });

  test("a template var whose key differs from its label is sent under the KEY", async () => {
    const V = instance({ name: "v", providerId: "google-vertex-anthropic" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [V], availableProviders: [VERTEX] }));
    connectionStore.getState().connect(fake);
    renderSheet(V, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.type(field("GOOGLE_VERTEX_BASE_URL"), "https://vx.example.test");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "v",
      vars: { BASE_URL: "https://vx.example.test" },
      originClientId: "test-tab",
    });
  });

  test("a command expression in the credential header saves as authored", async () => {
    // The hub's authoring rule accepts command expressions as credential
    // material; the sheet's save-time check only requires a $, so the
    // expression must cross the form and the wire unrefused and unaltered.
    const V = instance({ name: "work", providerId: "openai" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [V], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(V, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Credential header"));
    await user.type(field("Credential header"), "Authorization=Bearer $(get-gateway-token)");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      credentialHeader: "Authorization=Bearer $(get-gateway-token)",
      originClientId: "test-tab",
    });
  });

  test("Save is disabled until a field changes, and while writesRefused", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    expect(saveButton().disabled).toBe(true);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    expect(saveButton().disabled).toBe(false);

    cleanup();
    renderSheet(WORK, { writesRefused: true }, [OPENAI]);
    await user.type(field("Base URL"), "/x");
    expect(saveButton().disabled).toBe(true);
  });

  // Every action the sheet offers takes Save's gate, not only the form: the
  // request in flight may be a rename, and until it settles the sheet still
  // shows the instance under its old name, so anything clicked in that window
  // goes out against a name the write is moving away from - and the credential
  // actions would recreate under it the orphan the rename just moved.
  test("a save in flight makes every instance action unavailable", async () => {
    const FULL = instance({
      name: "work",
      providerId: "openai",
      baseUrl: "https://gw.example.test/v1",
      authModes: ["apiKey", "credentialJson", "oauth"],
      hasStoredFile: true,
      activeSource: "oauth",
    });
    const actions = [
      "Test credentials",
      "Replace key",
      "Replace credential JSON",
      "Sign in…",
      /make default/i,
      "Clear stored credential JSON",
      "Clear",
      "Remove",
    ];
    // Unavailable, not necessarily natively disabled: the actions the click
    // itself gated (the save) refuse with aria-disabled to keep the keyboard,
    // the rest are disabled outright - the user can reach neither.
    const unavailableStates = () =>
      actions.map((name) => {
        const control = screen.getByRole("button", { name }) as HTMLButtonElement;
        return control.disabled || control.getAttribute("aria-disabled") === "true";
      });

    const { fake, finish } = deferredEdit();
    renderSheet(FULL, {}, [OPENAI]);
    expect(unavailableStates()).toEqual(actions.map(() => false));

    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });
    expect(unavailableStates()).toEqual(actions.map(() => true));

    await act(async () => finish({ instances: [FULL], availableProviders: [OPENAI] }));
    expect(unavailableStates()).toEqual(actions.map(() => false));
  });

  test("the save button keeps the keyboard while the save is out", async () => {
    const { fake, finish } = deferredEdit();
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    await sentEditParams(fake);

    // Its own click made it unavailable, so it refuses (aria-disabled) and keeps
    // the keyboard rather than dropping it to <body>.
    expect(saveButton().getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(saveButton());

    await act(async () => finish({ instances: [WORK], availableProviders: [OPENAI] }));
  });

  // The Name field is editable on every instance. A rename always authors an
  // entry under the new name; when the hub marks the row as one the environment
  // re-supplies (renameLeavesRow), the old row stays too, so the note under the
  // field says that instead of promising a rename that cannot happen.
  test("a row the hub marks as left behind shows the old-row-stays note", async () => {
    renderSheet(
      instance({
        name: "groq",
        providerId: "groq",
        implicit: true,
        activeSource: "env:GROQ_API_KEY",
        renameLeavesRow: true,
      }),
    );
    expect(field("Name").disabled).toBe(false);
    const user = userEvent.setup();
    await user.type(field("Name"), "-2");
    expect(screen.getByText(/leaves this one in place/)).toBeTruthy();
  });

  // The same note for a row that only looks like the user's - a stored key
  // outranking a set variable, with the hub's bit saying the variable supplies
  // the old name once the key moves.
  test("a stored key shadowing a set variable shows the old-row-stays note", async () => {
    renderSheet(
      instance({
        name: "groq",
        providerId: "groq",
        implicit: true,
        activeSource: "store",
        hasStoredFile: true,
        shadowedEnvVar: "GROQ_API_KEY",
        renameLeavesRow: true,
      }),
    );
    const user = userEvent.setup();
    await user.type(field("Name"), "-2");
    expect(screen.getByText(/leaves this one in place/)).toBeTruthy();
  });

  test("a UI-credentialed instance's Name is editable with the ordinary rename note", async () => {
    renderSheet(
      instance({
        name: "openai-codex",
        providerId: "openai-codex",
        auth: "oauth-openai-codex",
        implicit: true,
        activeSource: "oauth",
        hasStoredOAuth: true,
      }),
    );
    expect(field("Name").disabled).toBe(false);
    const user = userEvent.setup();
    await user.type(field("Name"), "-2");
    expect(screen.getByText(/keep the old name/)).toBeTruthy();
    expect(screen.queryByText(/leaves this one in place/)).toBeNull();
  });

  test("changing the name shows the rename note", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    expect(screen.getByText(/reference "work" keep the old name/)).toBeTruthy();
  });

  // The note and instanceEditParams have to agree on what a rename is: an
  // emptied Name is not one - the wire reads an empty newName as unchanged, so
  // Save refuses it - and promising that past sessions keep the old name is a
  // promise about a rename that cannot go out.
  test("an emptied name shows no rename note", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Name"));
    expect(screen.queryByText(/reference "work" keep the old name/)).toBeNull();
  });

  test("Save sends only the changed fields", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x", originClientId: "test-tab" });
      return { instances: [{ ...WORK, baseUrl: "https://gw.example.test/v1/x" }], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    await waitFor(() => expect(getToasts().some((t) => t.text === "Saved work")).toBe(true));
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
    // Reseeded from the refreshed instance: clean again.
    expect(saveButton().disabled).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  test("emptying Base URL shows the reset note and sends clearBaseUrl", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [WORK], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Base URL"));
    expect(screen.getByText("Resets the endpoint to the provider's default.")).toBeTruthy();
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", clearBaseUrl: true, originClientId: "test-tab" });
  });

  test("choosing inherit from base sends clearProtocol", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [WORK], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Protocol"), "");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", clearProtocol: true, originClientId: "test-tab" });
  });

  test("emptying a var sends it empty so the hub deletes it", async () => {
    const V = instance({ name: "v", providerId: "google-vertex-anthropic", vars: { GOOGLE_VERTEX_PROJECT: "p1" } });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [V], availableProviders: [VERTEX] }));
    connectionStore.getState().connect(fake);
    renderSheet(V, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "" },
      originClientId: "test-tab",
    });
  });

  test("a credential header without $ is refused inline, with no RPC", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Credential header"));
    await user.type(field("Credential header"), "Authorization=Bearer sk-literal");
    await user.click(saveButton());
    expect(screen.getByRole("alert").textContent).toContain("$VARIABLE");
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
  });

  test("an emptied name is refused inline, with no RPC", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Name"));
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(screen.getByRole("alert").textContent).toContain("Name cannot be empty");
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
  });

  // The same refusal when emptying the Name is the ONLY edit: an empty
  // newName means "unchanged" on the wire, so the request the diff would
  // build carries nothing at all. Save has to stay live anyway, or the
  // mistake answers with a dead button and no reason.
  test("clearing only the name keeps Save live and refuses it inline, with no RPC", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Name"));
    expect(saveButton().disabled).toBe(false);
    await user.click(saveButton());
    expect(screen.getByRole("alert").textContent).toContain("Name cannot be empty");
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
  });

  // Save's disabled attribute guards the BUTTON; the form can be submitted
  // without it (Enter, or any submit control a later change adds), so the
  // write providers.toml has refused has to be refused by the action itself.
  test("submitting the form under writesRefused sends nothing", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, { writesRefused: true }, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    fireEvent.submit(screen.getByRole("form", { name: "Edit work" }));
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
  });

  test("a failed save shows the error inline and toasts Save failed", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("providers.toml: write: read-only");
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only"));
    expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  // The save carries the endpoint the draft was seeded from, and the hub refuses
  // an edit whose name another client has re-pointed since. The refusal is the
  // endpoint-conflict class: a clear message, a listing refresh so a retry
  // asserts the destination now on screen, and the draft kept.
  test("a save refused for a changed endpoint surfaces the message, refreshes, and keeps the draft", async () => {
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new WireError(
        "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
        -32013,
        { evenerErrorInfo: ErrorEndpointConflict },
      );
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK_FP, {}, [OPENAI]);
    const user = userEvent.setup();
    const listingsBefore = fake.calls.filter((call) => call.method === "evener/instance/list").length;
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    // The assertion the hub checks travels with the save.
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      expectedEndpointFingerprint: "fp-work",
      originClientId: "test-tab",
    });
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("changed to a different endpoint"));
    expect(getToasts().some((t) => t.text.includes("changed to a different endpoint"))).toBe(true);
    // The draft survives for the retry, and the listing is re-read.
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBeGreaterThan(listingsBefore);
  });

  // The refusal's message asks the user to "review its destination and save
  // again", so the retry has to be able to land: once the recovery read has
  // applied, the sheet must assert the destination the name now resolves to and
  // keep the edits the user typed. Before the fix the sheet kept both the stale
  // assertion and the stale seeded identity, so the second Save either re-sent
  // the refused fingerprint or tripped the identity guard and reseeded over the
  // draft.
  test("a retry after an endpoint conflict asserts the refreshed destination and keeps the edits", async () => {
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    const WORK_MOVED = {
      ...WORK,
      baseUrl: "https://gw.example.test/v2",
      endpointFingerprint: "fp-moved",
    };
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new WireError(
        "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
        -32013,
        { evenerErrorInfo: ErrorEndpointConflict },
      );
    });
    // The same instance, re-pointed: name/providerId/base/auth unchanged, only
    // the endpoint (baseUrl + fingerprint) moved.
    fake.on("evener/instance/list", () => ({ instances: [WORK_MOVED], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK_FP, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    const edits = () => fake.calls.filter((call) => call.method === "evener/instance/edit");
    expect(edits()).toHaveLength(1);
    expect(edits()[0]?.params).toMatchObject({ expectedEndpointFingerprint: "fp-work" });
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("changed to a different endpoint"));
    // The recovery read has applied the re-pointed row.
    await waitFor(() =>
      expect(credentialsStore.getState().instances.find((i) => i.name === "work")?.endpointFingerprint).toBe(
        "fp-moved",
      ),
    );

    await user.click(saveButton());
    await waitFor(() => expect(edits()).toHaveLength(2));
    // The retry lands against the destination now on screen, with the user's
    // typed field intact - not the refused fingerprint, and not a reseeded form.
    expect(edits()[1]?.params).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      expectedEndpointFingerprint: "fp-moved",
      originClientId: "test-tab",
    });
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  // The re-anchor must not fire for a replacement: when the refreshed row under
  // the name is a different instance, the sheet's existing identity guard is what
  // refuses - the edits typed for the old instance must not land on a stranger.
  test("a replacement at the name is refused, not re-anchored, after an endpoint conflict", async () => {
    const WORK_FP = { ...WORK, endpointFingerprint: "fp-work" };
    const IMPOSTOR = instance({
      name: "work",
      providerId: "anthropic",
      protocol: "anthropic",
      baseUrl: "https://gw.example.test/v9",
      endpointFingerprint: "fp-impostor",
    });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new WireError(
        "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
        -32013,
        { evenerErrorInfo: ErrorEndpointConflict },
      );
    });
    fake.on("evener/instance/list", () => ({ instances: [IMPOSTOR], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK_FP, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    const edits = () => fake.calls.filter((call) => call.method === "evener/instance/edit");
    expect(edits()).toHaveLength(1);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("changed to a different endpoint"));
    await waitFor(() =>
      expect(credentialsStore.getState().instances.find((i) => i.name === "work")?.providerId).toBe("anthropic"),
    );

    await user.click(saveButton());
    // The guard refuses and reseeds; nothing is sent to the replacement.
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("was replaced under the same name"));
    expect(edits()).toHaveLength(1);
  });

  // The hub's generic conflict is shared by genuine refusals. A rename onto an
  // occupied name is one: the sheet must surface the hub's own message and keep
  // the draft, not dress it up as a moved endpoint and refresh.
  test("a rename onto an occupied name surfaces the hub's refusal, not an endpoint conflict", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new WireError('instance "work2" already exists', -32013, { evenerErrorInfo: "conflict" });
    });
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    const listingsBefore = fake.calls.filter((call) => call.method === "evener/instance/list").length;
    await user.type(field("Name"), "2");
    await user.click(saveButton());

    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      newName: "work2",
      originClientId: "test-tab",
    });
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("already exists"));
    expect(screen.queryByText(/changed to a different endpoint/)).toBeNull();
    // Not the endpoint-conflict recovery: no listing read was asked for.
    expect(fake.calls.filter((call) => call.method === "evener/instance/list").length).toBe(listingsBefore);
  });

  test("a rename toasts the new name, calls onRenamed, and does not close the sheet", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });
      return { instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] };
    });
    connectionStore.getState().connect(fake);
    const { handlers, onClose } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  // The draft is seeded per instance, not per render: another client's change
  // to the SAME instance hands the sheet a fresh InstanceEntry, and reseeding
  // from it would silently discard whatever the user has typed.
  test("another client's change to the same instance leaves an in-progress edit alone", async () => {
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    act(() => {
      credentialsStore.setState({ instances: [{ ...WORK, baseUrl: "https://changed.example.test" }] });
    });
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
    expect(saveButton().disabled).toBe(false);
  });

  // A name frees when an instance is removed, and anything can be recreated
  // under it. The retained draft was typed against the instance that left;
  // saving it would write those edits onto the replacement. The draft survives
  // the listing swap (the effect above must not clobber edits), so the save
  // itself is where the identity has to be checked.
  test("a remove/recreate under the same name cannot take the retained draft's edit", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      credentialRequired: true,
    });
    const replacement = instance({
      name: "work",
      providerId: "openai",
      baseUrl: "https://gw.example.test/v2",
      endpointFingerprint: "fp-after",
      credentialRequired: true,
    });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/stale");

    act(() => {
      credentialsStore.setState({ instances: [replacement], availableProviders: [OPENAI] });
    });
    // The premise: the draft is retained across the swap, still dirty.
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/stale");
    expect(saveButton().disabled).toBe(false);

    await user.click(saveButton());
    // The stale edit must not reach the replacement...
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
    expect(screen.getByRole("alert").textContent).toContain("replaced under the same name");
    // ...and the form re-anchors to the instance now on screen.
    expect(field("Base URL").value).toBe("https://gw.example.test/v2");
  });

  // The store refuses a save issued from the previous connection's listing
  // (stores/credentials.ts's requireWritableClient): the draft was seeded from
  // rows that connection read. That is not a failed save - nothing was sent -
  // and the raw store message names the store's internals rather than what the
  // user can do, so the sheet says what changed and keeps the draft for the
  // retry this connection's own listing sets up.
  test("a save issued while the held listing is stale is refused with the change, not a save failure", async () => {
    const WORK = instance({ name: "work", providerId: "openai", baseUrl: "https://gw.example.test/v1" });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);

    // The connection is replaced and its own listing has not landed yet.
    act(() => credentialsStore.setState({ listingFromPreviousConnection: true }));
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/stale");

    await user.click(saveButton());

    // The edit was refused before it was sent...
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
    // ...reported as the change it is, in the form and as a warning, never as
    // the store's own words or a "Save failed" toast...
    await waitFor(() => expect(screen.getAllByText(/connection was replaced/).length).toBeGreaterThan(0));
    expect(screen.queryByText(/credentials store/)).toBeNull();
    expect(screen.queryByText(/Save failed/)).toBeNull();
    // ...the draft is kept, so the retry is the save the user typed...
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/stale");
    expect(saveButton().disabled).toBe(false);
    // ...and the refusal asked for this connection's listing, whose arrival is
    // what makes that retry land.
    expect(fake.calls.some((c) => c.method === "evener/instance/list")).toBe(true);
  });

  // The fingerprint is the part of the identity baseUrl cannot show: two
  // endpoints that differ only in a query parameter read identically, and a
  // recreation that changes only the query is still a different instance.
  test("a recreation differing only in the endpoint fingerprint is refused too", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      credentialRequired: true,
    });
    const replacement = instance({
      name: "work",
      providerId: "openai",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-after",
      credentialRequired: true,
    });
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("API key environment variable"), "STALE_KEY");

    act(() => {
      credentialsStore.setState({ instances: [replacement], availableProviders: [OPENAI] });
    });
    expect(field("API key environment variable").value).toBe("STALE_KEY");

    await user.click(saveButton());
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
    expect(screen.getByRole("alert").textContent).toContain("replaced under the same name");
    expect(field("API key environment variable").value).toBe("");
  });

  // The hub cannot key every row: one whose endpoint it could not fingerprint
  // serves no endpointFingerprint, and the digest is the only part of the
  // identity two rows at different destinations differ in - baseUrl, protocol,
  // surface and the credential fields are what a draft edits, so they are
  // deliberately not identity on their own (DRAFT_IDENTITY_FIELDS). Built from
  // name/provider/base/auth alone, then, a same-name replacement at another
  // endpoint is the SAME identity, and the retained draft - typed against the
  // endpoint that left - is written onto the replacement. Something the user
  // can see has to stand in for the digest the hub could not compute.
  test("an unkeyable same-name replacement at another endpoint does not inherit the draft", async () => {
    const original = unkeyable({ name: "work", providerId: "openai", baseUrl: "https://original.example/v1" });
    const replacement = unkeyable({ name: "work", providerId: "openai", baseUrl: "https://replacement.example/v2" });
    // The premise: neither row serves a fingerprint, so nothing but the visible
    // endpoint can tell them apart.
    expect("endpointFingerprint" in original).toBe(false);
    expect("endpointFingerprint" in replacement).toBe(false);

    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("must not be called");
    });
    connectionStore.getState().connect(fake);
    renderSheet(original, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("API key environment variable"), "STALE_KEY");

    // A concurrent change puts a different endpoint under the same name. The
    // draft survives the swap (the reseed is per instance, not per listing), so
    // the save is where the identity has to be checked.
    await refreshList(fake, [replacement]);
    expect(field("Base URL").value).toBe("https://original.example/v1");
    expect(field("API key environment variable").value).toBe("STALE_KEY");

    await user.click(saveButton());
    // The stale draft must not reach the replacement through either write path.
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(0);
    expect(fake.calls.filter((c) => c.method === "evener/auth/apiKey/set")).toHaveLength(0);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("replaced under the same name"));
    // ...and the form re-anchors to the endpoint now on screen.
    expect(field("Base URL").value).toBe("https://replacement.example/v2");
    expect(field("API key environment variable").value).toBe("");
  });

  // The other half of the same fallback: a row that is still the one the draft
  // was seeded from has to save as usual, fingerprint or not. Reading "no
  // fingerprint" as "not the seeded instance" would turn fingerprinting being
  // unavailable into an instance that can never be edited from this sheet.
  test("an unkeyable row whose listing did not move still saves", async () => {
    const row = unkeyable({ name: "work", providerId: "openai", baseUrl: "https://original.example/v1" });
    expect("endpointFingerprint" in row).toBe(false);

    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({
      instances: [{ ...row, apiKeyEnv: "PORTKEY_KEY" }],
      availableProviders: [OPENAI],
    }));
    connectionStore.getState().connect(fake);
    renderSheet(row, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("API key environment variable"), "PORTKEY_KEY");

    // The same row re-read: same visible endpoint, still no fingerprint. Its
    // identity is unchanged, so nothing about the save is.
    await refreshList(fake, [{ ...row }]);
    expect(field("Base URL").value).toBe("https://original.example/v1");

    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", apiKeyEnv: "PORTKEY_KEY", originClientId: "test-tab" });
    await waitFor(() => expect(getToasts().some((t) => t.text === "Saved work")).toBe(true));
    expect(screen.queryByRole("alert")).toBeNull();
    // Reseeded from the instance the save answered with: clean again, showing
    // what landed rather than a refusal.
    expect(field("API key environment variable").value).toBe("PORTKEY_KEY");
    expect(saveButton().disabled).toBe(true);
  });

  // The guard that keeps a rename's vanish from closing the sheet is spent by
  // the section's re-selection: a guard that outlived the rename would swallow
  // the next genuine removal too, leaving an editor open on a ghost.
  test("a rename keeps the sheet open, and a later removal still closes it", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({
      instances: [{ ...WORK, name: "work2" }],
      availableProviders: [OPENAI],
    }));
    connectionStore.getState().connect(fake);
    const { handlers, onClose, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    expect(onClose).not.toHaveBeenCalled();

    selectName("work2");
    await waitFor(() => expect(field("Name").value).toBe("work2"));
    expect(onClose).not.toHaveBeenCalled();

    act(() => {
      credentialsStore.setState({ instances: [] });
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  // The guard goes up before the request leaves, so a FAILED rename has to take
  // it back down: nothing renamed, so no re-selection is coming to spend it, and
  // a guard left standing swallows the next genuine removal - an editor left
  // open on a ghost, offering actions on an instance that no longer exists.
  test("a failed rename releases the guard, so a later removal still closes the sheet", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => {
      throw new Error("providers.toml: write: read-only");
    });
    connectionStore.getState().connect(fake);
    const { onClose } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("read-only"));
    expect(onClose).not.toHaveBeenCalled();

    act(() => {
      credentialsStore.setState({ instances: [] });
    });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  // The rename lands in the store before the section re-selects the new name,
  // so for that beat the sheet's own name is in neither place. A sheet that
  // reads only the store goes `open === false` there and UNMOUNTS: the panel
  // and its scrim are rebuilt as new nodes, the slide-in animation replays
  // (~200ms of the sheet sliding back in from off-screen, backdrop dim gone -
  // measured live), and FocusScope's mount hook throws focus out of the form.
  // Node identity pins the remount; the removal watch pins the empty frame,
  // which a before/after snapshot cannot see.
  test("a rename keeps the same panel and form nodes, with no frame in between", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({
      instances: [{ ...WORK, name: "work2" }],
      availableProviders: [OPENAI],
    }));
    connectionStore.getState().connect(fake);
    const { handlers, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");

    const panel = screen.getByRole("dialog");
    const form = screen.getByRole("form", { name: "Edit work" });
    const removals = watchRemovals({ panel, form });

    await user.click(saveButton());
    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    selectName("work2");
    await waitFor(() => expect(field("Name").value).toBe("work2"));

    expect(removals()).toEqual([]);
    expect(screen.getByRole("dialog")).toBe(panel);
    expect(screen.getByRole("form", { name: "Edit work2" })).toBe(form);
  });

  // The same remount, seen from the keyboard: FocusScope moves focus to the
  // first tabbable descendant every time it mounts, and at that moment `busy`
  // still disables every field, so the grab lands on the first action button -
  // measured live, `Test credentials`. What this pins is the absence of that
  // grab. It is not the whole of "focus does not move": a real browser also
  // blurs the field the save disables (focus falls to <body> there, on a plain
  // save as much as on a rename), which jsdom does not model.
  test("a rename does not hand focus to the first action button", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({
      instances: [{ ...WORK, name: "work2" }],
      availableProviders: [OPENAI],
    }));
    connectionStore.getState().connect(fake);
    const { handlers, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    // Submitting the form rather than clicking Save leaves focus in the field
    // being edited, so a remount's focus grab has something to take it from.
    field("Name").focus();
    const focused = document.activeElement;
    // Names what focus has to stay on. The assertion at the end holds for
    // whatever was captured here, `<body>` included, so a change that took
    // focus out of the form before this point would pass it while pinning
    // nothing at all.
    expect(focused).toBe(field("Name"));
    fireEvent.submit(screen.getByRole("form", { name: "Edit work" }));

    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    selectName("work2");
    await waitFor(() => expect(field("Name").value).toBe("work2"));

    expect(document.activeElement).toBe(focused);
  });

  // A save outlives the sheet's interest in it: the request is in flight while
  // the user is free to dismiss the sheet or pick another row, and the section
  // owns the selection either way. onRenamed is the sheet asking for the
  // selection to move, so answering a response the user has walked away from
  // yanks the section somewhere it did not ask to go - re-opening a dismissed
  // sheet, or dragging it off the row just picked. The write itself stands,
  // and its toast is owed: the user asked for it and it happened.
  test("a rename that lands after the sheet is dismissed does not re-open it", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers, dismiss, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });

    dismiss();
    expect(screen.queryByRole("dialog")).toBeNull();
    await act(async () => finish({ instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
    // The rename happened, so the user hears about it even though the sheet
    // they asked from is gone.
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(true);
    // And the sheet is not wedged busy: opening it on the new name gives a
    // live form again.
    selectName("work2");
    await waitFor(() => expect(field("Name").value).toBe("work2"));
    expect(field("Name").disabled).toBe(false);
  });

  test("a rename that lands after another row is selected does not steal the selection", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers, onClose, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });

    act(() => {
      credentialsStore.setState({ instances: [WORK, OTHER], availableProviders: [OPENAI] });
    });
    selectName("other");
    await waitFor(() => expect(field("Name").value).toBe("other"));
    await act(async () => finish({ instances: [{ ...WORK, name: "work2" }, OTHER], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "other" })).toBeTruthy();
    expect(field("Name").value).toBe("other");
  });

  // The other half of the same guard: a plain save reseeds the draft from the
  // response, and the instance it reseeds from is the one the save went out
  // for. Reseeding it into a sheet that has since moved on shows one
  // instance's values under another's title.
  test("a plain save that lands after another row is selected does not reseed its draft", async () => {
    const { fake, finish } = deferredEdit();
    const { onClose, selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    act(() => {
      credentialsStore.setState({ instances: [WORK, OTHER], availableProviders: [OPENAI] });
    });
    selectName("other");
    await waitFor(() => expect(field("Name").value).toBe("other"));
    await act(async () => finish({ instances: [WORK, OTHER], availableProviders: [OPENAI] }));

    expect(onClose).not.toHaveBeenCalled();
    expect(field("Name").value).toBe("other");
    expect(field("Base URL").value).toBe("https://other.example.test");
  });

  // A failure steers nothing, but it does write the form's own error line,
  // and that line belongs to the instance the save went out for. Landing it
  // in a sheet the user has since pointed elsewhere blames one instance for
  // another's failure. The toast is owed either way: the write was asked
  // for and it did not happen.
  test("a save that fails after another row is selected leaves the new sheet unmarked", async () => {
    const { fake, fail } = deferredEdit();
    const { selectName } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    act(() => {
      credentialsStore.setState({ instances: [WORK, OTHER], availableProviders: [OPENAI] });
    });
    selectName("other");
    await waitFor(() => expect(field("Name").value).toBe("other"));
    await act(async () => fail(new Error("providers.toml: write: read-only")));

    expect(screen.queryByRole("alert")).toBeNull();
    expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(true);
  });

  // A refresh that starts after the save answers first, so the store discards
  // the save's own response as superseded. The response is then a document
  // nothing holds: steering or reseeding on it shows the user values the
  // store does not have. Only the store's current list can say what landed —
  // and when that list already holds the renamed instance, it says the save
  // landed, so the sheet reports the ordinary success.
  const STALE_SAVE_WARNING =
    "Saved, but the list changed underneath; your edits were kept — refresh to see the current state";

  /** Answers an evener/instance/list refresh with `instances` and waits for
   * the store to apply it, so the save still in flight becomes superseded. */
  async function refreshList(
    fake: FakeClient,
    instances: InstanceEntry[],
    providers: ProviderDescriptor[] = [OPENAI],
  ): Promise<void> {
    fake.on("evener/instance/list", () => ({ instances, availableProviders: providers }));
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
  }

  test("a rename the store superseded neither steers the sheet nor claims a save", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });

    await refreshList(fake, [WORK]);
    await act(async () => finish({ instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
    // The draft is the user's, and nothing reseeded over it.
    expect(field("Name").value).toBe("work2");
  });

  test("a rename the store superseded is a saved rename when the list confirms it", async () => {
    const before = { ...WORK, endpointFingerprint: "fp-work" };
    const renamed = { ...before, name: "work2" };
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      newName: "work2",
      originClientId: "test-tab",
      expectedEndpointFingerprint: "fp-work",
    });

    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("work2");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved work2")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // A rename the hub persisted but could not finish (the OAuth record would
  // not move, or its reload failed) answers as an error carrying the hub's
  // ErrorInstanceRenamePersisted discriminator. That discriminator is the
  // authoritative fact - providers.toml already names the new instance - so the
  // sheet must follow it even when the refreshed listing cannot show the new
  // row (the hub's fallback registry, a reload that failed). Gating the steer
  // on the listing strands the sheet on a name the config no longer carries;
  // replacing the steer with a form error does the same while also blaming the
  // wrong thing. The listing is still refreshed (the store should catch up),
  // and the hub's own warning - it names the credential left behind - still
  // reaches the user.
  test("a persisted rename whose listing omits the new row still steers to the new name and warns", async () => {
    const HUB_MESSAGE =
      "renamed work to work2, but: OAuth record not read: open /state/auth/work.json: permission denied";
    const fake = new FakeClient("ready");
    // The refreshed listing never gains the new row: the hub's registry is on
    // its fallback listing, so reconciliation can never confirm the landing.
    fake.on("evener/instance/list", () => ({ instances: [WORK], availableProviders: [OPENAI] }));
    fake.on("evener/instance/edit", () => {
      throw new WireError(HUB_MESSAGE, -32603, { evenerErrorInfo: ErrorInstanceRenamePersisted });
    });
    connectionStore.getState().connect(fake);
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      newName: "work2",
      originClientId: "test-tab",
    });

    // The persisted name is followed regardless of the listing...
    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("work2"));
    // ...the listing was still reconciled...
    expect(fake.calls.some((c) => c.method === "evener/instance/list")).toBe(true);
    // ...the hub's message still warns...
    expect(getToasts().some((t) => t.kind === "warning" && t.text === HUB_MESSAGE)).toBe(true);
    // ...and no form error blames the instance the config already renamed.
    expect(screen.queryByRole("alert")).toBeNull();
    expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(false);
  });

  // A rename that also edits an endpoint-affecting field necessarily changes
  // the listing's endpointFingerprint: the digest is derived from the resolved
  // endpoint, which baseUrl/vars/protocol/surface are exactly what it resolves
  // from. Comparing that derived field as though the save left it untouched
  // rejects the store's own confirmation of a rename that plainly landed. It
  // stands as an independent identity field only when this save did not touch
  // what it derives from.
  test("a superseded rename that also edits the endpoint is confirmed by the store's own listing", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      newName: "work2",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    const renamed = {
      ...WORK,
      name: "work2",
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-work2",
    };
    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("work2");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved work2")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // A rename frees its old name for anyone to take: a removal and a recreation
  // under it, or another instance's own rename onto it. The store's list then
  // holds the new name without holding this save's rename, and steering the
  // sheet onto that entry would title one instance with another's values.
  test("a superseded rename whose new name now belongs to another instance claims nothing", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });

    await refreshList(fake, [{ ...OTHER, name: "work2" }]);
    await act(async () => finish({ instances: [{ ...OTHER, name: "work2" }], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // The rename's identity comparison has to cover every field the entry
  // carries that this save did not touch, not only the ones baseUrl can show:
  // endpointFingerprint is the digest of the complete resolved endpoint, so a
  // different instance whose query-only difference leaves every visible field
  // identical is still not the one that performed this rename. A save that
  // edits only a credential field derives no new digest, so the fingerprint
  // remains an independent identity field for it.
  test("a superseded rename that leaves the endpoint untouched is not confirmed by a differing fingerprint", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("API key environment variable"));
    await user.type(field("API key environment variable"), "PORTKEY_KEY_2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      newName: "work2",
      apiKeyEnv: "PORTKEY_KEY_2",
      originClientId: "test-tab",
    });

    const lookAlike = {
      ...WORK,
      name: "work2",
      apiKeyEnv: "PORTKEY_KEY_2",
      endpointFingerprint: "fp-other",
    };
    await refreshList(fake, [lookAlike]);
    await act(async () => finish({ instances: [lookAlike], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // Same for the auth scheme: an instance that authenticates differently is a
  // different instance, however alike the rest of its listing entry reads.
  test("a superseded rename is not confirmed by a look-alike differing only in auth", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2", originClientId: "test-tab" });

    const lookAlike = { ...WORK, name: "work2", auth: "oauth-openai-codex" };
    await refreshList(fake, [lookAlike]);
    await act(async () => finish({ instances: [lookAlike], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // Renaming a curated-shadow instance does change its listing's `base`: the
  // entry held the curated provider's own name, and that name is what its
  // configuration is inherited through, so the hub pins the old name into
  // base to keep the inherited configuration after the name is gone. That is
  // this rename carrying base over, not a different instance wearing the new
  // name, and reading it as a difference rejects the store's own confirmation.
  // The pre-save entry carries no `base` key at all: the wire drops the empty
  // value (json:"base,omitempty"), so an empty base arrives absent and the
  // comparison has to normalize it.
  test("a superseded rename of a curated shadow is confirmed though the rename pinned base", async () => {
    const shadow = instance({
      name: "openai",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      apiKeyEnv: "PORTKEY_KEY",
      endpointFingerprint: "fp-shadow",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(shadow, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "-work");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "openai",
      newName: "openai-work",
      expectedEndpointFingerprint: "fp-shadow",
      originClientId: "test-tab",
    });

    const renamed = { ...shadow, name: "openai-work", base: "openai" };
    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("openai-work");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved openai-work")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // Renaming an instance that had no authored entry authors one under the new
  // name, so the row the store's listing holds at that name is an authored one.
  // Implicit becoming authored is therefore the rename's own outcome, not a
  // different instance wearing the new name: reading the changed flag as a
  // difference rejects the store's confirmation and leaves the sheet on a row
  // that no longer exists.
  test("a superseded rename of an instance with no authored entry is confirmed as authored", async () => {
    const codex = instance({
      name: "openai-codex",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      implicit: true,
      activeSource: "oauth",
      hasStoredOAuth: true,
      endpointFingerprint: "fp-codex",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(codex, {}, []);
    const user = userEvent.setup();
    await user.type(field("Name"), "-work");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "openai-codex",
      newName: "openai-codex-work",
      expectedEndpointFingerprint: "fp-codex",
      originClientId: "test-tab",
    });

    // What the hub authors: the entry under the new name, pinning the curated
    // id it was inheriting from, so the listing's row is authored and carries
    // that base.
    const renamed = { ...codex, name: "openai-codex-work", base: "openai-codex", implicit: false };
    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("openai-codex-work");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved openai-codex-work")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // A rename always authors the entry under the new name, so an implicit row
  // holding that name is never this rename's result: it is a curated provider
  // the environment re-derived there, or a later tenant of the freed name.
  // Landing the sheet on it would title one instance with another's values.
  test("a superseded rename is not confirmed by an implicit row at the new name", async () => {
    const codex = instance({
      name: "openai-codex",
      providerId: "openai-codex",
      auth: "oauth-openai-codex",
      authModes: ["oauth"],
      implicit: true,
      activeSource: "oauth",
      hasStoredOAuth: true,
      endpointFingerprint: "fp-codex",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(codex, {}, []);
    const user = userEvent.setup();
    await user.type(field("Name"), "-work");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "openai-codex",
      newName: "openai-codex-work",
      expectedEndpointFingerprint: "fp-codex",
      originClientId: "test-tab",
    });

    // Every identity field matches, but the row is implicit - not something a
    // rename authors.
    const implicitLookAlike = { ...codex, name: "openai-codex-work", implicit: true };
    await refreshList(fake, [implicitLookAlike]);
    await act(async () => finish({ instances: [implicitLookAlike], availableProviders: [] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved openai-codex-work")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // The same rename answered by the store instead of superseded: the edit
  // response applies, so the sheet steers on its own request and the section
  // re-selects the new name. The pre-save entry is the wire shape again, with
  // no base, so a normalization applied to one path only would leave the
  // sheet pinned to the old name here.
  test("a rename of a curated shadow the store applied steers the sheet onto the new name", async () => {
    const shadow = instance({
      name: "openai",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      apiKeyEnv: "PORTKEY_KEY",
      endpointFingerprint: "fp-shadow",
    });
    const fake = new FakeClient("ready");
    const renamed = { ...shadow, name: "openai-work", base: "openai" };
    fake.on("evener/instance/edit", () => ({ instances: [renamed], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    const { handlers } = renderSheet(shadow, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "-work");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "openai",
      newName: "openai-work",
      expectedEndpointFingerprint: "fp-shadow",
      originClientId: "test-tab",
    });

    await waitFor(() => expect(handlers.onRenamed).toHaveBeenCalledWith("openai-work"));
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved openai-work")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // The pinning is the old name, exactly: a listing whose base is some other
  // value is not this rename carried over but a different instance, and
  // steering the sheet onto it would title one instance with another's values.
  test("a superseded rename is not confirmed by a listing whose base is not the old name", async () => {
    const shadow = instance({
      name: "openai",
      providerId: "openai",
      endpointFingerprint: "fp-shadow",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(shadow, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "-work");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "openai",
      newName: "openai-work",
      expectedEndpointFingerprint: "fp-shadow",
      originClientId: "test-tab",
    });

    const lookAlike = { ...shadow, name: "openai-work", base: "anthropic" };
    await refreshList(fake, [lookAlike]);
    await act(async () => finish({ instances: [lookAlike], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved openai-work")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  test("a plain save the store superseded keeps the draft and does not claim a save", async () => {
    const { fake, finish } = deferredEdit();
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    await refreshList(fake, [WORK]);
    await act(async () =>
      finish({ instances: [{ ...WORK, baseUrl: "https://gw.example.test/v1/x" }], availableProviders: [OPENAI] }),
    );

    expect(getToasts().some((t) => t.text === "Saved work")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });

  // A superseded save that edited an endpoint-affecting field lands in the
  // store's own listing: the derived endpointFingerprint is the digest of the
  // change this save produced, so the seeded identity anchor — taken before the
  // save — no longer matches the entry that carries it. Without re-anchoring,
  // the next Save refuses with the replacement error for an instance nothing
  // replaced. The draft is the user's own landed change: re-anchoring keeps it
  // and rebases the diff baseline to what landed, so the next Save is not
  // refused and is not spuriously dirty either.
  test("a superseded endpoint save re-anchors so the next save is not refused as a replacement", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    // The refresh that started after the save answers first, so the store
    // discards the save's own response as superseded while its listing already
    // holds the endpoint the save produced.
    const landed = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    // The draft now equals the landed row, so no replacement error appears and
    // Save is clean (disabled) rather than re-sending the same edit.
    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(saveButton().disabled).toBe(true);
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The re-anchor above turns on the listing carrying THIS save's declaration.
  // A same-name replacement that matches every untouched identity field but
  // differs in the very field the save edited is not this save's landing:
  // re-anchoring there would pin the draft to the replacement and let the next
  // Save write onto it.
  test("a superseded save is not re-anchored onto a concurrent write in the same field it touched", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    // The answer the store discards is THIS save's landing; the refresh that
    // superseded it carries another client's write to the same field.
    const ours = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-ours" };
    const foreign = { ...before, baseUrl: "https://other.example.test/y", endpointFingerprint: "fp-after" };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // A save's params carry only the variables it changed; every other authored
  // variable is left alone. Comparing `vars` as one field would therefore
  // accept a replacement that differs only in an untouched variable. The
  // re-anchor has to compare the variables key by key.
  test("a superseded save is not re-anchored onto a look-alike differing only in an untouched variable", async () => {
    const before = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "loc1" },
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p9");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p9" },
      originClientId: "test-tab",
    });

    // The listing carries the variable this save declared, but its untouched
    // variable differs: this is a different instance, not this save's landing.
    const lookAlike = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p9", GOOGLE_VERTEX_LOCATION: "loc-other" },
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [lookAlike], [VERTEX]);
    await act(async () => finish({ instances: [lookAlike], availableProviders: [VERTEX] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // Editing an implicit instance authors a shadow under the same name, so the
  // listing's implicit legitimately falls true -> false across this save. That
  // is still this save's own landing: the re-anchor must allow it, or the next
  // endpoint save is falsely refused and the draft reset.
  test("editing an implicit instance still re-anchors after it authors a shadow", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      implicit: true,
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    const authored = {
      ...before,
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-after",
      implicit: false,
    };
    await refreshList(fake, [authored]);
    await act(async () => finish({ instances: [authored], availableProviders: [OPENAI] }));

    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(saveButton().disabled).toBe(true);
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The transition is one-directional: an authored row does not become
  // implicit from a plain save, so a listing that flips the other way is a
  // different instance and the re-anchor must not accept it.
  test("a superseded save is not re-anchored onto a replacement under a differing implicit", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    const envRow = {
      ...before,
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-after",
      implicit: true,
    };
    await refreshList(fake, [envRow]);
    await act(async () => finish({ instances: [envRow], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The listing's Base URL is the endpoint the hub sanitized: userinfo, query
  // and fragment are stripped before it crosses the appwire boundary, so the
  // listing cannot show that query. The mutation's own discarded answer carries
  // the hub's fingerprint for the complete resolved endpoint, so the re-anchor
  // is confirmed from that rather than from the sanitized display string.
  test("a superseded save whose declared URL carries a stripped part re-anchors on the authoritative fingerprint", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x?token=abc");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x?token=abc",
      originClientId: "test-tab",
    });

    // The listing serves the sanitized endpoint; the answer the store discards
    // carries the fingerprint for the same landing.
    const landed = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(2);
  });

  // A rename whose captured row has no endpoint fingerprint cannot prove the
  // renamed destination, so it fails closed like the plain path: a stale-save
  // warning and no steer onto the new name (rather than a URL comparison that
  // could never see hidden parts).
  test("a superseded rename with no authoritative fingerprint fails closed", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      newName: "work2",
      originClientId: "test-tab",
    });

    // The rename landed, but the captured row serves no fingerprint.
    const renamed = {
      ...before,
      name: "work2",
      endpointFingerprint: "",
    };
    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // Clearing an endpoint override drops the authored value, and the listing
  // then serves the RESOLVED one: an instance with a different authored override
  // that resolves to the same effective endpoint carries the same fingerprint,
  // so the capture cannot tell this save's clear from a replacement. The plain
  // supersede fails closed on an endpoint clear instead of re-anchoring.
  test("a superseded endpoint clear is not re-anchored onto a same-endpoint replacement", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Base URL"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      clearBaseUrl: true,
      originClientId: "test-tab",
    });

    // This save's landing resolved the inherited URL; a replacement carries its
    // own override that resolves to the same effective endpoint.
    const ours = {
      ...before,
      baseUrl: "https://inherited.example.test/v1",
      endpointFingerprint: "fp-after",
    };
    const foreign = {
      ...before,
      baseUrl: "https://other.example.test/x",
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The hub normalizes a credential header to `name=value` (trimming around
  // the `=`) before the listing serves it, so a declared header with spacing
  // only matches after the same reduction. The save also edits the endpoint,
  // so the re-anchor is what the next Save depends on (the fingerprint moved).
  test("a superseded credential-header save re-anchors through the hub's normalization", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      credentialHeader: "Authorization=Bearer $OLDKEY",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.clear(field("Credential header"));
    await user.type(field("Credential header"), "Authorization = Bearer $NEWKEY");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      credentialHeader: "Authorization = Bearer $NEWKEY",
      originClientId: "test-tab",
    });

    // The listing serves the header the hub normalized and the endpoint the
    // save produced.
    const landed = {
      ...before,
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-after",
      credentialHeader: "Authorization=Bearer $NEWKEY",
    };
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(2);
  });

  // A malformed or hostless URL is stored verbatim by the hub but sanitizes to
  // empty for the listing, so two distinct invalid destinations read the same.
  // Matching empty-to-empty would re-anchor the draft onto a same-name
  // replacement; the check fails closed instead.
  test("a superseded save with a malformed URL does not re-anchor onto another malformed entry", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("Base URL"));
    await user.type(field("Base URL"), "not a url");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "not a url",
      originClientId: "test-tab",
    });

    // This save's own landing: a malformed URL, which the hub stores verbatim
    // but cannot key (no fingerprint).
    const ours = { ...before, baseUrl: "not a url", endpointFingerprint: "" };
    // A different invalid destination, which the hub also sanitizes to "".
    const foreign = { ...before, baseUrl: "also not a url", endpointFingerprint: "fp-after" };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The hub omits apiKeyEnv/credentialHeader when the authored value is invalid
  // or a literal secret, so an omitted listing field is not proof that this
  // save cleared it. On an instance the hub cannot key there is no authoritative
  // fingerprint to fall back on, so the clear fails closed instead of
  // re-anchoring onto redacted metadata.
  test("an unkeyable superseded endpoint-plus-credential clear does not re-anchor onto redacted metadata", async () => {
    const before = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      apiKeyEnv: "PORTKEY_KEY",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.clear(field("API key environment variable"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      clearApiKeyEnv: true,
      originClientId: "test-tab",
    });

    // This save's own landing (redacted: the cleared field is omitted), and a
    // replacement at the same endpoint carrying a hidden authored value.
    const ours = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    });
    const foreign = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
      apiKeyEnv: "HIDDEN_KEY",
    });
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The listing's baseUrl is the RESOLVED URL: editing a template variable that
  // feeds the provider's base_url changes it, so it is derived from this save
  // and cannot be compared as an untouched identity field on an endpoint-
  // affecting save.
  test("a superseded save that changes a template variable re-anchors on the new resolved URL", async () => {
    const before = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1" },
      baseUrl: "https://old.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      originClientId: "test-tab",
    });

    // The variable change resolves a different base URL in the listing.
    const landed = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://new.example.test/v1",
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [landed], [VERTEX]);
    await act(async () => finish({ instances: [landed], availableProviders: [VERTEX] }));

    // The re-anchor takes the landed resolution and leaves the draft clean.
    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(field("Base URL").value).toBe("https://new.example.test/v1");
    expect(saveButton().disabled).toBe(true);

    // A further variable edit must not carry the stale pre-save URL as an
    // explicit baseUrl override.
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p3");
    await user.click(saveButton());
    const edits = fake.calls.filter((c) => c.method === "evener/instance/edit");
    expect(edits).toHaveLength(2);
    expect(edits[1]?.params).toEqual({
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p3" },
      expectedEndpointFingerprint: "fp-after",
      originClientId: "test-tab",
    });
  });

  // A rename riding along with a credential clear cannot be confirmed: the hub
  // omits an authored value it cannot serve, so a replacement under the new name
  // with different (hidden) credentials reads the same. The rename fails closed.
  test("a superseded rename that also clears a credential field is not confirmed", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      apiKeyEnv: "PORTKEY_KEY",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("API key environment variable"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      newName: "work2",
      clearApiKeyEnv: true,
      originClientId: "test-tab",
    });

    // The renamed instance landed without the cleared authored field.
    const landed = instance({
      name: "work2",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // The credential-clear exemption above is only for clears whose landed value
  // the rename does not depend on. An ENDPOINT clear (baseUrl here) is
  // unverifiable and also removes baseUrl and endpointFingerprint from the
  // untouched comparison, so a replacement under the new name with any endpoint
  // must not be confirmed as this rename.
  test("a superseded rename that clears the endpoint is not confirmed by an arbitrary replacement", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("Base URL"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      newName: "work2",
      clearBaseUrl: true,
      originClientId: "test-tab",
    });

    const foreign = {
      ...before,
      name: "work2",
      baseUrl: "https://other.example.test/y",
      endpointFingerprint: "fp-other",
    };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [foreign], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // A variable-only save declares no baseUrl, so the listing's resolved URL is
  // the only endpoint evidence. The mutation's own answer carries the
  // fingerprint for the endpoint it resolved; a replacement that matches the
  // requested variable but resolves another URL must not be re-anchored.
  test("a superseded variable save is not re-anchored onto an entry with a different URL override", async () => {
    const before = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1" },
      baseUrl: "https://resolved-one.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      originClientId: "test-tab",
    });

    const ours = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved-one.example.test/v1",
      endpointFingerprint: "fp-ours",
    };
    const foreign = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved-other.example.test/v1",
      endpointFingerprint: "fp-foreign",
    };
    await refreshList(fake, [foreign], [VERTEX]);
    await act(async () => finish({ instances: [ours], availableProviders: [VERTEX] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The endpoint fingerprint proves the destination, not the credential fields
  // it excludes. A replacement at the same endpoint (matching fingerprint) that
  // carries different credential metadata must not be re-anchored over.
  test("a superseded endpoint-plus-credential clear is not re-anchored onto conflicting credential metadata", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      apiKeyEnv: "PORTKEY_KEY",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.clear(field("API key environment variable"));
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      clearApiKeyEnv: true,
      originClientId: "test-tab",
    });

    // Same endpoint, so the same fingerprint; the replacement keeps its own
    // credential metadata.
    const ours = {
      ...before,
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-after",
      apiKeyEnv: "",
    };
    const foreign = {
      ...before,
      baseUrl: "https://gw.example.test/v1/x",
      endpointFingerprint: "fp-after",
      apiKeyEnv: "OTHER_KEY",
    };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // A pathless URL is not a path disagreement: the hub serves `https://host`
  // while WHATWG supplies `/`, so the normalization guard must not refuse a
  // rename that moves the endpoint to a bare authority.
  test("a superseded rename to a pathless Base URL is confirmed", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("Base URL"));
    await user.type(field("Base URL"), "https://api.example.test");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      newName: "work2",
      baseUrl: "https://api.example.test",
      originClientId: "test-tab",
    });

    const renamed = {
      ...before,
      name: "work2",
      baseUrl: "https://api.example.test",
      endpointFingerprint: "fp-other",
    };
    await refreshList(fake, [renamed]);
    await act(async () => finish({ instances: [renamed], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("work2");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved work2")).toBe(true);
  });

  // The endpoint fingerprint excludes `surface` and the variables that do not
  // feed the endpoint, so a matching fingerprint alone must not confirm a
  // declared variable that the listing carries differently.
  test("a superseded variable save is not re-anchored onto an entry with a conflicting declared variable", async () => {
    const before = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      originClientId: "test-tab",
    });

    const ours = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    };
    const foreign = {
      ...before,
      vars: { GOOGLE_VERTEX_PROJECT: "p3" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [foreign], [VERTEX]);
    await act(async () => finish({ instances: [ours], availableProviders: [VERTEX] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // draftIdentity excludes the credential fields, so a credential-only save
  // whose superseding listing belongs to a same-endpoint replacement would pass
  // the next pre-write check and overwrite that replacement's credentials. An
  // unconfirmed credential supersede marks the draft stale instead.
  test("a superseded credential-only save is not re-anchored onto a replacement's credentials", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      apiKeyEnv: "PORTKEY_KEY",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.clear(field("API key environment variable"));
    await user.type(field("API key environment variable"), "NEW_KEY");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      apiKeyEnv: "NEW_KEY",
      originClientId: "test-tab",
    });

    const ours = { ...before, apiKeyEnv: "NEW_KEY" };
    const foreign = { ...before, apiKeyEnv: "FOREIGN_KEY" };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    // The next Save refuses and re-seeds rather than overwriting the foreign key.
    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // The endpoint fingerprint does not cover `surface`, and draftIdentity does
  // not either, so an unconfirmed supersede of a surface change must mark the
  // draft stale or a same-endpoint replacement is overwritten on the next Save.
  test("a superseded surface save whose landing cannot be confirmed marks the draft stale", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      surface: "generic",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Surface"), "openai");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      surface: "openai",
      originClientId: "test-tab",
    });

    // Same endpoint (same fingerprint), different surface.
    const ours = { ...before, surface: "openai" };
    const foreign = { ...before, surface: "anthropic" };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // A surface clear leaves no declared value to compare and the fingerprint
  // excludes surface, so it is verified against the captured row instead.
  test("a superseded clear-surface save is not re-anchored onto a replacement with a different surface", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      surface: "generic",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Surface"), "");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      clearSurface: true,
      originClientId: "test-tab",
    });

    const ours = { ...before, surface: "" };
    const foreign = { ...before, surface: "anthropic" };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // Without an authoritative fingerprint the sanitized listing cannot prove the
  // destination (baseUrl omits query/userinfo), so an unkeyable supersede fails
  // closed and marks the draft stale rather than re-anchoring from display fields.
  test("an unkeyable superseded save is not re-anchored without an authoritative fingerprint", async () => {
    const before = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    const ours = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    });
    const foreign = unkeyable({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1/x",
    });
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    await user.click(saveButton());
    expect(screen.getByText(/replaced under the same name/)).toBeTruthy();
    expect(fake.calls.filter((c) => c.method === "evener/instance/edit")).toHaveLength(1);
  });

  // A confirmed re-anchor rebases the diff baseline to the landed row, so the
  // draft is clean; reverting the landed edit to its pre-save value must then be
  // dirty and writable rather than a Save the sheet cannot press.
  test("a confirmed superseded re-anchor lets a landed edit be reverted", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      baseUrl: "https://gw.example.test/v1/x",
      originClientId: "test-tab",
    });

    const landed = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    // Draft equals landed: clean, no replacement error.
    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(saveButton().disabled).toBe(true);

    await user.clear(field("Base URL"));
    await user.type(field("Base URL"), "https://gw.example.test/v1");
    expect(saveButton().disabled).toBe(false);
    await user.click(saveButton());
    const edits = fake.calls.filter((c) => c.method === "evener/instance/edit");
    expect(edits).toHaveLength(2);
    expect(edits[1]?.params).toMatchObject({
      name: "work",
      baseUrl: "https://gw.example.test/v1",
      originClientId: "test-tab",
    });
  });

  // The baseline rebase must not manufacture a pending change: a field the save
  // did not declare whose store value moved concurrently takes the landed value,
  // so Save stays clean instead of reverting the other client's edit.
  test("a re-anchor does not turn a concurrently changed unedited field into a pending overwrite", async () => {
    const seeded = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      surface: "generic",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
      apiKeyEnv: "OLD",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(seeded, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Surface"), "openai");
    // Another client changes apiKeyEnv; the draft keeps its seeded value because
    // the reseed effect keys on the instance name only.
    await act(async () => {
      credentialsStore.setState({
        instances: [{ ...seeded, apiKeyEnv: "FOREIGN" }],
        availableProviders: [OPENAI],
      });
    });
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      surface: "openai",
      originClientId: "test-tab",
    });

    const landed = {
      ...seeded,
      surface: "openai",
      apiKeyEnv: "FOREIGN",
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [landed]);
    await act(async () => finish({ instances: [landed], availableProviders: [OPENAI] }));

    // The unedited apiKeyEnv takes the landed (foreign) value; nothing pending.
    expect(field("API key environment variable").value).toBe("FOREIGN");
    expect(saveButton().disabled).toBe(true);
  });

  // The rename path now proves the destination from the rename's own captured
  // row: a replacement under the new name at the same sanitized URL but a
  // different hidden endpoint (a query the listing strips) must not be confirmed.
  test("a superseded rename is not confirmed by a same-URL entry at a different hidden endpoint", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://old.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("Base URL"));
    await user.type(field("Base URL"), "https://gw.example.test/v1");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "work",
      newName: "work2",
      baseUrl: "https://gw.example.test/v1",
      originClientId: "test-tab",
    });

    const ours = {
      ...before,
      name: "work2",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-ours",
    };
    const foreign = {
      ...before,
      name: "work2",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-foreign",
    };
    await refreshList(fake, [foreign]);
    await act(async () => finish({ instances: [ours], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  // A rename that only changes a variable has no declared Base URL, but the
  // rename's own captured row carries the new-name fingerprint, so it confirms
  // rather than failing closed.
  test("a superseded rename that only changes a variable is confirmed by the authoritative fingerprint", async () => {
    const before = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(before, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      newName: "v2",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      originClientId: "test-tab",
    });

    const landed = {
      ...before,
      name: "v2",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [landed], [VERTEX]);
    await act(async () => finish({ instances: [landed], availableProviders: [VERTEX] }));

    expect(handlers.onRenamed).toHaveBeenCalledWith("v2");
    expect(getToasts().some((t) => t.kind === "success" && t.text === "Saved v2")).toBe(true);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(false);
  });

  // The real transport ordering: the superseding read is issued after this
  // write, but its response arrives AFTER the write's (evener/instance/list runs
  // inline on the connection's serial worker). The store therefore still holds
  // the pre-save listing when the edit's await resolves, and the sheet must
  // settle a post-write read before comparing.
  test("a superseded save re-anchors when the superseding read lands after the edit response", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    const landed = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    const resolvers: ((value: InstanceListResponse) => void)[] = [];
    fake.on("evener/instance/list", () => new Promise<InstanceListResponse>((resolve) => resolvers.push(resolve)));
    const pendingRead = credentialsStore.getState().fetch();

    await act(async () => {
      finish({ instances: [landed], availableProviders: [OPENAI] });
    });
    await waitFor(() => expect(resolvers.length).toBeGreaterThanOrEqual(2));
    await act(async () => {
      for (const resolve of resolvers) resolve({ instances: [landed], availableProviders: [OPENAI] });
    });
    await pendingRead;

    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
    expect(saveButton().disabled).toBe(true);
  });

  // A superseded save whose connection is torn down before its answer must not
  // report a failed save: the write may have landed, and the follow-up read is
  // only a confirmation (it throws when no client is connected).
  test("a superseded save does not report a failed save when the connection is gone", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    // A read starts after the write and supersedes its answer; the connection
    // is then torn down before the edit's response arrives.
    fake.on("evener/instance/list", () => new Promise<InstanceListResponse>(() => {}));
    void credentialsStore.getState().fetch();
    act(() => connectionStore.setState({ client: null, state: "idle" }));

    await act(async () => {
      finish({ instances: [before], availableProviders: [OPENAI] });
    });

    expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(false);
    expect(screen.queryByText(/Save failed/)).toBeNull();
    expect(screen.queryByText(/no client connected/)).toBeNull();
  });

  // `vars` is a map: the re-anchor must rebase per key, so an undeclared
  // variable a concurrent client changed does not become a pending overwrite.
  test("a re-anchor does not resurrect a concurrently changed undeclared variable", async () => {
    const seeded = instance({
      name: "v",
      providerId: "google-vertex-anthropic",
      protocol: "anthropic",
      vars: { GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "loc1" },
      baseUrl: "https://resolved.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(seeded, {}, [VERTEX]);
    const user = userEvent.setup();
    await user.clear(field("GOOGLE_VERTEX_PROJECT"));
    await user.type(field("GOOGLE_VERTEX_PROJECT"), "p2");
    // Another client changes the OTHER authored variable; the draft keeps its
    // seeded value because the reseed effect keys on the instance name only.
    await act(async () => {
      credentialsStore.setState({
        instances: [{ ...seeded, vars: { GOOGLE_VERTEX_PROJECT: "p1", GOOGLE_VERTEX_LOCATION: "loc-client" } }],
        availableProviders: [VERTEX],
      });
    });
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({
      expectedEndpointFingerprint: "fp-before",
      name: "v",
      vars: { GOOGLE_VERTEX_PROJECT: "p2" },
      originClientId: "test-tab",
    });

    const landed = {
      ...seeded,
      vars: { GOOGLE_VERTEX_PROJECT: "p2", GOOGLE_VERTEX_LOCATION: "loc-client" },
      endpointFingerprint: "fp-after",
    };
    await refreshList(fake, [landed], [VERTEX]);
    await act(async () => finish({ instances: [landed], availableProviders: [VERTEX] }));

    // The undeclared variable takes the landed (foreign) value; nothing pending.
    expect(field("GOOGLE_VERTEX_LOCATION").value).toBe("loc-client");
    expect(saveButton().disabled).toBe(true);
  });

  // The store already schedules its own debounced refetch on a superseded write;
  // if that read starts later it supersedes the sheet's confirmation read, which
  // then resolves without applying anything. The sheet must re-read until one
  // applies, not sample the pre-save listing.
  test("a superseded save re-reads when the store's own refetch supersedes the confirmation read", async () => {
    const before = instance({
      name: "work",
      providerId: "openai",
      protocol: "openai-responses",
      baseUrl: "https://gw.example.test/v1",
      endpointFingerprint: "fp-before",
    });
    const { fake, finish } = deferredEdit();
    renderSheet(before, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());

    const landed = { ...before, baseUrl: "https://gw.example.test/v1/x", endpointFingerprint: "fp-after" };
    const resolvers: ((value: InstanceListResponse) => void)[] = [];
    fake.on("evener/instance/list", () => new Promise<InstanceListResponse>((resolve) => resolvers.push(resolve)));

    // The read that supersedes the edit.
    void credentialsStore.getState().fetch();
    await act(async () => {
      finish({ instances: [landed], availableProviders: [OPENAI] });
    });

    // The sheet's confirmation read, then the store's own later refetch that
    // supersedes it so it resolves without applying.
    await waitFor(() => expect(resolvers.length).toBeGreaterThanOrEqual(2));
    void credentialsStore.getState().fetch();
    await waitFor(() => expect(resolvers.length).toBeGreaterThanOrEqual(3));
    await act(async () => {
      resolvers[1]?.({ instances: [landed], availableProviders: [OPENAI] });
    });

    // The re-read applies the post-write listing.
    await waitFor(() => expect(resolvers.length).toBeGreaterThanOrEqual(4));
    await act(async () => {
      resolvers[3]?.({ instances: [landed], availableProviders: [OPENAI] });
    });

    expect(screen.queryByText(/replaced under the same name/)).toBeNull();
    expect(saveButton().disabled).toBe(true);
  });
});
