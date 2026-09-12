import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, InstanceListResponse, ProviderDescriptor } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { AddInstanceDialog, ApiKeyDialog, CredentialJsonDialog } from "./instanceDialogs";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

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

function provider(overrides: Partial<ProviderDescriptor> & Pick<ProviderDescriptor, "id">): ProviderDescriptor {
  return {
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    ...overrides,
  };
}

const ANTHROPIC = provider({ id: "anthropic", protocol: "anthropic" });
const VERTEX = provider({
  id: "google-vertex-anthropic",
  protocol: "anthropic",
  auth: "gcp-adc",
  varsEnv: ["GOOGLE_VERTEX_LOCATION", "GOOGLE_VERTEX_PROJECT"],
  vars: { GOOGLE_VERTEX_PROJECT: "GOOGLE_VERTEX_PROJECT", GOOGLE_VERTEX_LOCATION: "GOOGLE_VERTEX_LOCATION" },
});
const BEDROCK = provider({
  id: "amazon-bedrock",
  protocol: "anthropic",
  auth: "gcp-adc",
  varsEnv: ["AWS_REGION"],
  vars: { AWS_REGION: "AWS_REGION" },
});
const VERTEX_EXPRESS = provider({
  id: "google-vertex-express",
  protocol: "google",
  auth: "header",
  apiKeyEnv: ["GOOGLE_VERTEX_API_KEY"],
  varsEnv: ["GOOGLE_VERTEX_EXPRESS_BASE_URL"],
  vars: { BASE_URL: "GOOGLE_VERTEX_EXPRESS_BASE_URL" },
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

describe("AddInstanceDialog", () => {
  test("Base provider select is populated from availableProviders", () => {
    connectFakeClient();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    const select = screen.getByLabelText("Base provider") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "anthropic", "google-vertex-anthropic"]);
  });

  test("a provider's display name is used as its option label when present", () => {
    connectFakeClient();
    render(
      <AddInstanceDialog
        availableProviders={[provider({ id: "anthropic", name: "Anthropic" })]}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByRole("option", { name: "Anthropic" })).toBeTruthy();
  });

  test("no variable inputs until a base with vars is selected", () => {
    connectFakeClient();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    expect(screen.queryByLabelText("GOOGLE_VERTEX_PROJECT")).toBeNull();
  });

  test("selecting a base renders one input per vars entry, labeled by name", async () => {
    connectFakeClient();
    const varsEnvOnly = provider({ id: "vars-env-only", varsEnv: ["LEGACY_ENV"] });
    render(
      <AddInstanceDialog
        availableProviders={[ANTHROPIC, VERTEX, varsEnvOnly]}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
    expect(screen.getByLabelText("GOOGLE_VERTEX_PROJECT")).toBeTruthy();
    expect(screen.getByLabelText("GOOGLE_VERTEX_LOCATION")).toBeTruthy();
    // varsEnv is the v3 name list, not a fallback: only vars drives the form.
    await user.selectOptions(screen.getByLabelText("Base provider"), "vars-env-only");
    expect(screen.queryByLabelText("LEGACY_ENV")).toBeNull();
  });

  // The Add form and the sheet show the same instance's variables, so they
  // order them the same way: by code point, the order a placeholder name has
  // as an identifier, not the one the reader's locale collates. The two
  // disagree here - a locale sort puts EXTRA_ONE first, ignoring the
  // underscore, while "S" is below "_" by code point.
  test("variable rows are ordered by code point, as the sheet orders them", async () => {
    connectFakeClient();
    const extras = provider({ id: "extras", vars: { EXTRAS: "EXTRAS_ENV", EXTRA_ONE: "EXTRA_ONE_ENV" } });
    render(<AddInstanceDialog availableProviders={[extras]} onCancel={() => {}} onSuccess={() => {}} />);
    await userEvent.setup().selectOptions(screen.getByLabelText("Base provider"), "extras");
    const rows = Array.from(document.querySelectorAll('input[id^="add-instance-var-"]')).map((el) => el.id);
    expect(rows).toEqual(["add-instance-var-EXTRAS", "add-instance-var-EXTRA_ONE"]);
  });

  test("google-vertex-express renders only its own base-URL override, no project or location", async () => {
    connectFakeClient();
    render(
      <AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX_EXPRESS]} onCancel={() => {}} onSuccess={() => {}} />,
    );
    await userEvent.setup().selectOptions(screen.getByLabelText("Base provider"), "google-vertex-express");
    expect(screen.getByLabelText("GOOGLE_VERTEX_EXPRESS_BASE_URL")).toBeTruthy();
    expect(screen.queryByLabelText("GOOGLE_VERTEX_PROJECT")).toBeNull();
    expect(screen.queryByLabelText("GOOGLE_VERTEX_LOCATION")).toBeNull();
  });

  test("google-vertex-express's base-URL override is sent under its template key, not the env var name", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({
        name: "vertex-express",
        base: "google-vertex-express",
        baseUrl: "",
        vars: { BASE_URL: "https://example.test/v1" },
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(
      <AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX_EXPRESS]} onCancel={() => {}} onSuccess={() => {}} />,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-express");
    await user.type(screen.getByLabelText("Name"), "vertex-express");
    await user.type(screen.getByLabelText("GOOGLE_VERTEX_EXPRESS_BASE_URL"), "https://example.test/v1");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("switching base providers clears the previous base's variable inputs and values", async () => {
    connectFakeClient();
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[BEDROCK, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "amazon-bedrock");
    await user.type(screen.getByLabelText("AWS_REGION"), "us-east-1");
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
    expect(screen.queryByLabelText("AWS_REGION")).toBeNull();
    expect((screen.getByLabelText("GOOGLE_VERTEX_PROJECT") as HTMLInputElement).value).toBe("");
  });

  test("client-side validation: Base provider required, then Name required", async () => {
    connectFakeClient();
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(screen.getByText("Base provider is required.")).toBeTruthy();
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(screen.getByText("Name is required.")).toBeTruthy();
  });

  test("a credential header without $ is rejected client-side, with no RPC", async () => {
    const fake = connectFakeClient();
    const create = vi.fn();
    fake.on("evener/instance/create", create);
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.type(screen.getByLabelText(/credential header/i), "Authorization=Bearer secret");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(screen.getByText("Credential header must reference a $VARIABLE, never a literal secret.")).toBeTruthy();
    expect(create).not.toHaveBeenCalled();
  });

  test("a credential header with $ is accepted and sent verbatim", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({
        name: "work",
        base: "anthropic",
        baseUrl: "",
        credentialHeader: "Authorization=Bearer $PORTKEY_KEY",
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.type(screen.getByLabelText(/credential header/i), "Authorization=Bearer $PORTKEY_KEY");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("api-key-env sends the bare variable name", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "", apiKeyEnv: "PORTKEY_KEY" });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.type(screen.getByLabelText(/api key environment variable/i), "PORTKEY_KEY");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("variable inputs are sent trimmed, with blank ones omitted", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({
        name: "vertex",
        base: "google-vertex-anthropic",
        baseUrl: "",
        vars: { GOOGLE_VERTEX_PROJECT: "my-proj" },
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
    await user.type(screen.getByLabelText("Name"), "vertex");
    await user.type(screen.getByLabelText("GOOGLE_VERTEX_PROJECT"), "  my-proj  ");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("submit calls instanceCreate and, on success, toasts + calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "https://x" });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.type(screen.getByLabelText(/base url/i), "https://x");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Created instance work").length).toBeGreaterThan(0);
  });

  // The listing a create answers with is only the truth if the store kept it.
  // The guided flow steers on the created row, so success must not be reported
  // against a listing a concurrent read already threw away.
  test("a superseded create reconciles the listing before success is reported", async () => {
    const fake = connectFakeClient();
    const WORK2 = instance({ name: "work2", providerId: "anthropic" });
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    const WITH_WORK2: InstanceListResponse = { instances: [WORK2], availableProviders: [] };
    fake.on("evener/instance/list", () => WITHOUT_WORK2);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    let resolveCreate!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/create",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveCreate = resolve;
        }),
    );

    let signalSuccess!: () => void;
    const successCalled = new Promise<void>((resolve) => {
      signalSuccess = resolve;
    });
    const listingsAtSuccess: InstanceEntry[][] = [];
    const onSuccess = vi.fn(() => {
      listingsAtSuccess.push(credentialsStore.getState().instances);
      signalSuccess();
    });
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work2");
    await user.click(screen.getByRole("button", { name: "Create" }));

    // A listing read issued after the create wins the store race, so the
    // create's own response - the only one carrying the new row - is discarded.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    // The reconcile read the dialog must now make sees the row.
    fake.on("evener/instance/list", () => WITH_WORK2);
    await act(async () => {
      resolveCreate(WITH_WORK2);
      await successCalled;
    });
    expect(onSuccess).toHaveBeenCalledWith("work2");
    expect(listingsAtSuccess[0]).toEqual([WORK2]);
  });

  // A consumer with its own missing-row recovery (the guided flow's
  // not-ready/reload state) takes the unconfirmed create through its callback
  // instead of the dialog claiming success or blocking on a re-confirm.
  test("an unconfirmed create hands off to the consumer's recovery without a success toast", async () => {
    const fake = connectFakeClient();
    const WORK2 = instance({ name: "work2", providerId: "anthropic" });
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    const WITH_WORK2: InstanceListResponse = { instances: [WORK2], availableProviders: [] };
    fake.on("evener/instance/list", () => WITHOUT_WORK2);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    let resolveCreate!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/create",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    const onSuccess = vi.fn();
    const onUnconfirmedCreate = vi.fn();
    // The toast queue is a module singleton shared across this file's tests;
    // clear it so "no success toast" means this create pushed none.
    resetToastStoreForTests();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog
          availableProviders={[ANTHROPIC]}
          onCancel={() => {}}
          onSuccess={onSuccess}
          onUnconfirmedCreate={onUnconfirmedCreate}
        />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work2");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await act(async () => {
      resolveCreate(WITH_WORK2);
    });

    await waitFor(() => expect(onUnconfirmedCreate).toHaveBeenCalledWith("work2"));
    expect(onSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText(/Created instance work2/)).toBeNull();
  });

  // A resolved reconcile is only confirmation if the listing it applied
  // actually contains the created row: reporting success on a listing that
  // never showed the instance closes the editor on a connection the host may
  // not have and steers the guided flow to a row that is not there. The retry
  // re-confirms the listing instead of re-issuing the create, which already
  // landed on the host.
  test("a superseded create whose reconciled listing never shows the row stays open unconfirmed", async () => {
    const fake = connectFakeClient();
    const WORK2 = instance({ name: "work2", providerId: "anthropic" });
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    const WITH_WORK2: InstanceListResponse = { instances: [WORK2], availableProviders: [] };
    fake.on("evener/instance/list", () => WITHOUT_WORK2);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    let resolveCreate!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/create",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work2");
    await user.click(screen.getByRole("button", { name: "Create" }));

    // A listing read issued after the create supersedes its response, so the
    // create's own listing is discarded and the dialog must reconcile.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await act(async () => {
      resolveCreate(WITH_WORK2);
    });

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));
    expect(onSuccess).not.toHaveBeenCalled();

    // The host catches up; re-confirming reports the create without a second one.
    fake.on("evener/instance/list", () => WITH_WORK2);
    await user.click(screen.getByRole("button", { name: "Check again" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith("work2"));
    expect(fake.calls.filter((call) => call.method === "evener/instance/create")).toHaveLength(1);
  });

  // fetch() resolves normally even when its response was superseded or the
  // read failed, so a resolved reconcile is no confirmation: the dialog must
  // retry while the read keeps losing the race and confirm the row is really
  // in the listing before steering the guided flow on it.
  test("a create whose reconcile read loses the race retries before reporting success", async () => {
    const fake = connectFakeClient();
    const WORK2 = instance({ name: "work2", providerId: "anthropic" });
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    const WITH_WORK2: InstanceListResponse = { instances: [WORK2], availableProviders: [] };
    let listCalls = 0;
    let resolveHeldRead!: (value: InstanceListResponse) => void;
    const heldRead = new Promise<InstanceListResponse>((resolve) => {
      resolveHeldRead = resolve;
    });
    let signalReconcile!: () => void;
    const reconcileStarted = new Promise<void>((resolve) => {
      signalReconcile = resolve;
    });
    fake.on("evener/instance/list", () => {
      listCalls += 1;
      if (listCalls === 3) {
        signalReconcile();
        return heldRead; // the dialog's first reconcile read, held in flight
      }
      // Calls 1-2: the initial load and the read that supersedes the create.
      // Call 4: the read that supersedes the first reconcile. Call 5+: fresh.
      return listCalls <= 4 ? WITHOUT_WORK2 : WITH_WORK2;
    });
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    let resolveCreate!: (value: InstanceListResponse) => void;
    fake.on(
      "evener/instance/create",
      () =>
        new Promise<InstanceListResponse>((resolve) => {
          resolveCreate = resolve;
        }),
    );

    let signalSuccess!: () => void;
    const successCalled = new Promise<void>((resolve) => {
      signalSuccess = resolve;
    });
    const listingsAtSuccess: InstanceEntry[][] = [];
    const onSuccess = vi.fn(() => {
      listingsAtSuccess.push(credentialsStore.getState().instances);
      signalSuccess();
    });
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work2");
    await user.click(screen.getByRole("button", { name: "Create" }));

    // A listing read issued after the create wins the store race, so the
    // create's own response - the only one carrying the new row - is discarded.
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await act(async () => {
      resolveCreate(WITH_WORK2);
      // The dialog's first reconcile read is in flight; a concurrent read
      // issued after it wins the store race too, discarding the reconcile.
      await reconcileStarted;
      await credentialsStore.getState().fetch();
      resolveHeldRead(WITH_WORK2);
      await successCalled;
    });
    expect(onSuccess).toHaveBeenCalledWith("work2");
    expect(listingsAtSuccess[0]).toEqual([WORK2]);
  });

  test("a create failure shows an inline error and a 'Create failed' toast, without calling onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", () => {
      throw new Error("name already exists");
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await screen.findByText("name already exists");
    expect(screen.getAllByText("Create failed: name already exists").length).toBeGreaterThan(0);
    expect(onSuccess).not.toHaveBeenCalled();
  });

  test("Protocol and Surface default to inherit and are sent only when chosen", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({
        name: "work",
        base: "anthropic",
        baseUrl: "",
        protocol: "openai-responses",
        surface: "generic",
      });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    expect((screen.getByLabelText("Protocol") as HTMLSelectElement).value).toBe("");
    expect((screen.getByLabelText("Surface") as HTMLSelectElement).value).toBe("");
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.selectOptions(screen.getByLabelText("Protocol"), "openai-responses");
    await user.selectOptions(screen.getByLabelText("Surface"), "generic");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });
});

describe("ApiKeyDialog", () => {
  test("submitting an empty (trimmed) value silently cancels - no RPC", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const onCancel = vi.fn();
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        onCancel={onCancel}
        onSuccess={() => {}}
      />,
    );
    await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "   ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  test("a non-empty key calls authApiKeySet, refreshes, toasts, and calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("API key saved for work").length).toBeGreaterThan(0);
  });

  test("a failure shows an inline error and a 'Save failed' toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("rejected");
    });
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={() => {}}
          onSuccess={() => {}}
        />
        <Toast />
      </>,
    );
    await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "sk-bad");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("rejected");
    expect(screen.getAllByText("Save failed: rejected").length).toBeGreaterThan(0);
  });

  test("the API key input is type=password", () => {
    connectFakeClient();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByLabelText(/api key/i, { selector: "input" }).getAttribute("type")).toBe("password");
  });
});

describe("CredentialJsonDialog", () => {
  test("submitting an empty (trimmed) value silently cancels - no RPC", async () => {
    const fake = connectFakeClient();
    const setJson = vi.fn();
    fake.on("evener/auth/credentialJson/set", setJson);
    const onCancel = vi.fn();
    const user = userEvent.setup();
    render(
      <CredentialJsonDialog
        instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
        onCancel={onCancel}
        onSuccess={() => {}}
      />,
    );
    await user.type(screen.getByLabelText(/credential json/i, { selector: "textarea" }), "   ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setJson).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  test("a pasted JSON calls credentialJson/set with the instance name, refreshes, toasts, and calls onSuccess", async () => {
    const fake = connectFakeClient();
    const json = '{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}';
    fake.on("evener/auth/credentialJson/set", (params) => {
      expect(params).toEqual({ provider: "vertex", value: json });
      return { provider: "vertex", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <CredentialJsonDialog
          instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByLabelText(/credential json/i, { selector: "textarea" }));
    await user.paste(json);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Credential JSON saved for vertex").length).toBeGreaterThan(0);
  });

  test("a rejected paste shows the server's message inline", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/credentialJson/set", () => {
      throw new Error("not a Google credential JSON: unexpected end of JSON input");
    });
    const user = userEvent.setup();
    render(
      <>
        <CredentialJsonDialog
          instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
          onCancel={() => {}}
          onSuccess={() => {}}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByLabelText(/credential json/i, { selector: "textarea" }));
    await user.paste("{");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("not a Google credential JSON");
  });
});
