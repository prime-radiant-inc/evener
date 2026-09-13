import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { InstanceSheet } from "./InstanceSheet";

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
  };
}

function renderSheet(
  inst: InstanceEntry | null,
  extra: Partial<Parameters<typeof InstanceSheet>[0]> = {},
  providers: ProviderDescriptor[] = [],
) {
  const handlers = noopHandlers();
  const onClose = vi.fn();
  credentialsStore.setState({ instances: inst === null ? [] : [inst], availableProviders: providers });
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
  // have been, so the badge explaining an implicit instance has to be here
  // too - not only on the row behind it.
  test("an implicit instance is badged 'from environment'", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(screen.getByText("from environment")).toBeTruthy();
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

  // Removing an implicit instance is refused server-side (spec §11.3), so
  // the sheet must not even offer the button; the form stays, since editing
  // an implicit instance writes a shadow rather than changing it.
  test("an implicit instance offers the form but no Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(screen.getByLabelText("Base URL")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  });

  // The danger zone is Clear + Remove under a divider; an implicit instance
  // with nothing stored offers neither, so the divider must go too rather
  // than trailing an empty section.
  test("no danger-zone divider when the instance offers neither Clear nor Remove", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "none" }));
    expect(document.querySelectorAll("hr").length).toBe(0);
  });

  test("the danger-zone divider stays when Clear alone is offered", () => {
    renderSheet(
      instance({ name: "groq", providerId: "groq", implicit: true, activeSource: "store", hasStoredFile: true }),
    );
    expect(document.querySelectorAll("hr").length).toBe(1);
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
    expect((screen.getByRole("button", { name: "Testing credentials…" }) as HTMLButtonElement).disabled).toBe(true);
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
    expect((screen.getByRole("button", { name: "Test credentials" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Replace key" }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole("button", { name: "Clear" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("leaves Clear stored key enabled", () => {
    renderSheet(instance({ name: "a", providerId: "x", activeSource: "adc", hasStoredFile: true }), {
      writesRefused: true,
    });
    expect((screen.getByRole("button", { name: "Clear stored key" }) as HTMLButtonElement).disabled).toBe(false);
  });

  test("an implicit instance under writesRefused still has no Remove button at all", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }), { writesRefused: true });
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
    expect(await sentEditParams(fake)).toEqual({ name: "v", vars: { BASE_URL: "https://vx.example.test" } });
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
  test("a save in flight disables every instance action", async () => {
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
    const disabledStates = () =>
      actions.map((name) => (screen.getByRole("button", { name }) as HTMLButtonElement).disabled);

    const { fake, finish } = deferredEdit();
    renderSheet(FULL, {}, [OPENAI]);
    expect(disabledStates()).toEqual(actions.map(() => false));

    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });
    expect(disabledStates()).toEqual(actions.map(() => true));

    await act(async () => finish({ instances: [FULL], availableProviders: [OPENAI] }));
    expect(disabledStates()).toEqual(actions.map(() => false));
  });

  test("an implicit instance's Name is disabled with the environment note", () => {
    renderSheet(instance({ name: "groq", providerId: "groq", implicit: true }));
    expect(field("Name").disabled).toBe(true);
    expect(screen.getByText(/comes from the environment/)).toBeTruthy();
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
      expect(params).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });
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
    expect(await sentEditParams(fake)).toEqual({ name: "work", clearBaseUrl: true });
  });

  test("choosing inherit from base sends clearProtocol", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", () => ({ instances: [WORK], availableProviders: [OPENAI] }));
    connectionStore.getState().connect(fake);
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.selectOptions(select("Protocol"), "");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", clearProtocol: true });
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
    expect(await sentEditParams(fake)).toEqual({ name: "v", vars: { GOOGLE_VERTEX_PROJECT: "" } });
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

  test("a rename toasts the new name, calls onRenamed, and does not close the sheet", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", newName: "work2" });
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
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2" });

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
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2" });

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
    expect(await sentEditParams(fake)).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });

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
    expect(await sentEditParams(fake)).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });

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
  async function refreshList(fake: FakeClient, instances: InstanceEntry[]): Promise<void> {
    fake.on("evener/instance/list", () => ({ instances, availableProviders: [OPENAI] }));
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
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2" });

    await refreshList(fake, [WORK]);
    await act(async () => finish({ instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
    // The draft is the user's, and nothing reseeded over it.
    expect(field("Name").value).toBe("work2");
  });

  test("a rename the store superseded is a saved rename when the list confirms it", async () => {
    const { fake, finish } = deferredEdit();
    const { handlers } = renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Name"), "2");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2" });

    await refreshList(fake, [{ ...WORK, name: "work2" }]);
    await act(async () => finish({ instances: [{ ...WORK, name: "work2" }], availableProviders: [OPENAI] }));

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
    expect(await sentEditParams(fake)).toEqual({ name: "work", newName: "work2" });

    await refreshList(fake, [{ ...OTHER, name: "work2" }]);
    await act(async () => finish({ instances: [{ ...OTHER, name: "work2" }], availableProviders: [OPENAI] }));

    expect(handlers.onRenamed).not.toHaveBeenCalled();
    expect(getToasts().some((t) => t.text === "Saved work2")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
  });

  test("a plain save the store superseded keeps the draft and does not claim a save", async () => {
    const { fake, finish } = deferredEdit();
    renderSheet(WORK, {}, [OPENAI]);
    const user = userEvent.setup();
    await user.type(field("Base URL"), "/x");
    await user.click(saveButton());
    expect(await sentEditParams(fake)).toEqual({ name: "work", baseUrl: "https://gw.example.test/v1/x" });

    await refreshList(fake, [WORK]);
    await act(async () =>
      finish({ instances: [{ ...WORK, baseUrl: "https://gw.example.test/v1/x" }], availableProviders: [OPENAI] }),
    );

    expect(getToasts().some((t) => t.text === "Saved work")).toBe(false);
    expect(getToasts().some((t) => t.kind === "warning" && t.text === STALE_SAVE_WARNING)).toBe(true);
    expect(field("Base URL").value).toBe("https://gw.example.test/v1/x");
  });
});
