import type {
  AuthStatusResponse,
  InstanceEntry,
  InstanceListResponse,
  ProviderDescriptor,
} from "@evener/appwire-client";
import { ErrorEndpointConflict, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { enterText } from "../../../../textEntryTestUtils";
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
  // The auth mutations carry the page's identity on the wire now, so the
  // assertions that pin their exact params need one that cannot vary.
  setMutationClientIdentityForTests("test-tab");
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
        originClientId: "test-tab",
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(
      <AddInstanceDialog availableProviders={[ANTHROPIC, VERTEX_EXPRESS]} onCancel={() => {}} onSuccess={() => {}} />,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-express");
    await enterText(user, screen.getByLabelText("Name"), "vertex-express");
    await enterText(user, screen.getByLabelText("GOOGLE_VERTEX_EXPRESS_BASE_URL"), "https://example.test/v1");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("switching base providers clears the previous base's variable inputs and values", async () => {
    connectFakeClient();
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[BEDROCK, VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "amazon-bedrock");
    await enterText(user, screen.getByLabelText("AWS_REGION"), "us-east-1");
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
    await enterText(user, screen.getByLabelText(/credential header/i), "Authorization=Bearer secret");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(
      screen.getByText("Credential header must reference a $VARIABLE or run a $(command), never a literal secret."),
    ).toBeTruthy();
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
        originClientId: "test-tab",
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await enterText(user, screen.getByLabelText(/credential header/i), "Authorization=Bearer $PORTKEY_KEY");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("api-key-env sends the bare variable name", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({
        name: "work",
        base: "anthropic",
        baseUrl: "",
        apiKeyEnv: "PORTKEY_KEY",
        originClientId: "test-tab",
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await enterText(user, screen.getByLabelText(/api key environment variable/i), "PORTKEY_KEY");
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
        originClientId: "test-tab",
      });
      return { instances: [], availableProviders: [] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableProviders={[VERTEX]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.selectOptions(screen.getByLabelText("Base provider"), "google-vertex-anthropic");
    await user.type(screen.getByLabelText("Name"), "vertex");
    await enterText(user, screen.getByLabelText("GOOGLE_VERTEX_PROJECT"), "  my-proj  ");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/create")).toBe(true));
  });

  test("submit calls instanceCreate and, on success, toasts + calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/create", (params) => {
      expect(params).toEqual({ name: "work", base: "anthropic", baseUrl: "https://x", originClientId: "test-tab" });
      // The hub's create returns the full updated listing, so the applied
      // response carries the new row (the dialog verifies it).
      return { instances: [instance({ name: "work", providerId: "anthropic" })], availableProviders: [] };
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
    await enterText(user, screen.getByLabelText(/base url/i), "https://x");
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

  // A dropped connection must not silently dead-end the retry: handleSubmit
  // reports a rejected read inline, and "Check again" fails the same way.
  test("Check again reports a dropped connection instead of dead-ending", async () => {
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
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={() => {}} />
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
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));

    await act(async () => {
      connectionStore.setState({ state: "idle", client: null });
    });
    await user.click(screen.getByRole("button", { name: "Check again" }));
    // The dropped connection makes the read reject, which is an unapplied
    // attempt, not a confirmation: the dialog reports what it can support
    // (the row is not confirmed) rather than the store's internal failure
    // text, and it keeps the name so Check again still works once the host is
    // back.
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));
    expect(screen.queryByText(/no client connected/)).toBeNull();
    expect(screen.getByRole("button", { name: "Check again" })).toBeTruthy();
  });

  // The applied path is not automatically a confirmation either: the store's
  // own listing is the applied response, and it must actually hold the row.
  test("a create whose applied listing omits the row is not reported as success", async () => {
    const fake = connectFakeClient();
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    fake.on("evener/instance/list", () => WITHOUT_WORK2);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    fake.on("evener/instance/create", () => WITHOUT_WORK2);
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

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));
    expect(onSuccess).not.toHaveBeenCalled();
  });

  // A curated provider the user has a credential or an environment variable
  // for is listed as an implicit instance, so a listing holding that row by
  // name is not evidence this create authored anything. Confirming against it
  // would steer the guided flow to a row the create never wrote.
  test("an implicit row with the created name does not confirm a create", async () => {
    const fake = connectFakeClient();
    const IMPLICIT_WORK2 = instance({ name: "work2", providerId: "anthropic", implicit: true });
    const WITH_IMPLICIT: InstanceListResponse = { instances: [IMPLICIT_WORK2], availableProviders: [] };
    fake.on("evener/instance/create", () => WITH_IMPLICIT);
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

    await waitFor(() => expect(onUnconfirmedCreate).toHaveBeenCalledWith("work2"));
    expect(onSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText(/Created instance work2/)).toBeNull();
  });

  // Without a consumer recovery callback, the unconfirmed implicit row leaves
  // the dialog open on its own re-confirm path rather than claiming success.
  test("an implicit row with the created name keeps the dialog open unconfirmed", async () => {
    const fake = connectFakeClient();
    const IMPLICIT_WORK2 = instance({ name: "work2", providerId: "anthropic", implicit: true });
    const WITH_IMPLICIT: InstanceListResponse = { instances: [IMPLICIT_WORK2], availableProviders: [] };
    fake.on("evener/instance/create", () => WITH_IMPLICIT);
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

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));
    expect(onSuccess).not.toHaveBeenCalled();
  });

  // The positive control: the entry a create actually authors (implicit:
  // false) still confirms the create.
  test("an authored row with the created name still confirms the create", async () => {
    const fake = connectFakeClient();
    const AUTHORED_WORK2 = instance({ name: "work2", providerId: "anthropic", implicit: false });
    const WITH_AUTHORED: InstanceListResponse = { instances: [AUTHORED_WORK2], availableProviders: [] };
    fake.on("evener/instance/create", () => WITH_AUTHORED);
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

    await waitFor(() => expect(onSuccess).toHaveBeenCalledWith("work2"));
  });

  // A superseded create reconciles through confirmCreate, which must apply the
  // same authored-row rule: a listing holding only the implicit row is not
  // confirmation either.
  test("a reconciled listing holding only an implicit row does not confirm the create", async () => {
    const fake = connectFakeClient();
    const IMPLICIT_WORK2 = instance({ name: "work2", providerId: "anthropic", implicit: true });
    const WITHOUT_WORK2: InstanceListResponse = { instances: [], availableProviders: [] };
    const WITH_IMPLICIT: InstanceListResponse = { instances: [IMPLICIT_WORK2], availableProviders: [] };
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

    // A listing read issued after the create supersedes its response; the
    // listing it leaves holds only the implicit row.
    fake.on("evener/instance/list", () => WITH_IMPLICIT);
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    await act(async () => {
      resolveCreate(WITH_IMPLICIT);
    });

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("could not confirm work2"));
    expect(onSuccess).not.toHaveBeenCalled();
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

  // The store refuses a write issued from the previous connection's listing
  // (stores/credentials.ts's requireWritableClient): the providers and names
  // this form was filled from were read on a connection that is gone. For a
  // create that is not a failed create - nothing was sent - and the raw store
  // message names the store's internals rather than what the user can do.
  test("a create from the previous connection's listing is refused with the change and keeps the draft", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({
      instances: [instance({ name: "work", providerId: "anthropic" })],
      availableProviders: [ANTHROPIC],
    }));
    await credentialsStore.getState().fetch();

    const replacement = new FakeClient("ready");
    // This connection's own read is held open: its rows are the ones the
    // create would be authored against, and until they arrive the form's
    // providers and names belong to the connection that is gone. Once released
    // it stays resolved, so the read the retry waits on is the same listing.
    let finishRestore!: (value: InstanceListResponse) => void;
    const restore = new Promise<InstanceListResponse>((resolve) => {
      finishRestore = resolve;
    });
    replacement.on("evener/instance/list", () => restore);
    replacement.on("evener/instance/create", () => ({ instances: [], availableProviders: [] }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableProviders={[ANTHROPIC]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Base provider"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "fresh");
    await user.click(screen.getByRole("button", { name: "Create" }));

    // Nothing was authored on the replacement connection...
    expect(replacement.calls.filter((call) => call.method === "evener/instance/create")).toHaveLength(0);
    // ...the dialog names the change instead of the store's internals, and
    // reports no create failure...
    await vi.waitFor(() => expect(screen.getByRole("alert").textContent).toContain("connection was replaced"));
    expect(screen.getByRole("alert").textContent).not.toContain("credentials store");
    expect(screen.queryByText(/Create failed/)).toBeNull();
    expect(onSuccess).not.toHaveBeenCalled();
    // ...the draft survives, so the retry the message asks for is the same
    // create the user filled in...
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("fresh");
    // ...and this connection's own listing read is what sets that retry up.
    await vi.waitFor(() => expect(replacement.calls.some((call) => call.method === "evener/instance/list")).toBe(true));
    await act(async () => finishRestore({ instances: [], availableProviders: [ANTHROPIC] }));
    await vi.waitFor(() => expect(credentialsStore.getState().listingFromPreviousConnection).toBe(false));
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
        originClientId: "test-tab",
      });
      return { instances: [instance({ name: "work", providerId: "anthropic" })], availableProviders: [] };
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
        expectedEndpointFingerprint={undefined}
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
      expect(params).toEqual({ provider: "work", value: "sk-secret", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("API key saved for work").length).toBeGreaterThan(0);
  });

  // The dialog is opened against one row's endpoint, and the value typed into
  // it belongs to that endpoint. A concurrent change can put a different
  // instance under the same name while the field holds the secret; submitting
  // then would send it to a destination the user never reviewed. The captured
  // fingerprint is what the write asserts, and a mismatch refuses the save
  // before any RPC, clears the field, and says why.
  test("a name that resolves to a different endpoint refuses the save, clears the value, and shows an error", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing for this name now carries a different endpoint.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("different endpoint"));
    expect(setKey).not.toHaveBeenCalled();
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  // The positive control: while the name still resolves to the endpoint the
  // dialog opened against, the captured fingerprint is what the write asserts,
  // not a live value read at submit time.
  test("an unchanged destination submits the captured fingerprint", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({
        provider: "work",
        value: "sk-secret",
        expectedEndpointFingerprint: "fp-original",
        originClientId: "test-tab",
      });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  // An undefined capture used to exempt the guard, so a row that gained a
  // fingerprint while the dialog sat open went out with no assertion. A hub
  // that keeps endpoint fingerprints refuses that write - it can describe the
  // destination, so an empty assertion leaves it unverified - which left the
  // user in a server refusal loop no local retry could break. The guard now
  // compares an undefined capture too: the row gaining one is a change to
  // refuse like any other, and the same local refusal re-anchors the
  // expectation to the row on screen so the retype saves against the
  // destination the user can see.
  test("a fingerprint gained while the dialog is open refuses the save and re-anchors to the row", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn(() => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    fake.on("evener/auth/apiKey/set", setKey);
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing entry this name resolves to gains an endpoint identity while
    // the field holds the secret.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-new" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    // Refused locally, before any RPC: the typed value asserted no endpoint
    // while the row now carries one.
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("different endpoint"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The refusal re-anchored the expectation to the row on screen: the
    // re-typed value saves against the destination the dialog now displays.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-new",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The hub serves a fingerprint only while it can key one, and the listing
  // omits what it cannot serve. A row that still carries a destination but no
  // fingerprint is therefore a destination the hub cannot describe right now -
  // the state a form opened during a key outage keeps when the listing has not
  // refreshed since. Submitting would assert nothing for an endpoint nobody
  // checked, so it is refused locally before any RPC, and the typed value is
  // kept for the retry the message asks for.
  test("a destination with no fingerprint refuses the save before any RPC", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://work.example/v1" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("cannot check this endpoint"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("sk-secret");
  });

  // The same state spelled as an explicit empty fingerprint, which a producer
  // that does not omit empty strings (or a fixture) carries; both spellings
  // mean "the row shows none" and both are refused.
  test("an explicit empty fingerprint on a destination refuses the save", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({
          name: "work",
          providerId: "anthropic",
          baseUrl: "https://work.example/v1",
          endpointFingerprint: "",
        })}
        expectedEndpointFingerprint=""
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("cannot check this endpoint"));
  });

  // The control: a row with no destination (baseUrl empty) and no fingerprint
  // is the "nothing shown, nothing to assert" case - the hub accepts a write
  // with no assertion there - so the dialog must still submit it.
  test("a row with no destination still submits with no assertion", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", baseUrl: "" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  // The other direction of the same guard: a capture that was defined when the
  // dialog opened and whose endpoint has since disappeared is a change too, so
  // the save must refuse rather than assert a fingerprint the row no longer
  // carries.
  test("a captured fingerprint that the row no longer carries refuses the save", async () => {
    const fake = connectFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The row loses its endpoint identity while the field holds the secret.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("different endpoint"));
    expect(setKey).not.toHaveBeenCalled();
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  // The refusal tells the user to enter the value again, so re-entering it has
  // to be able to succeed. The row on screen is the destination the user can
  // review, so the refusal re-anchors the dialog's expectation to it: the
  // re-typed value asserts the row the dialog is displaying instead of being
  // refused forever against the endpoint that is already gone.
  test("a value re-entered after a refusal asserts the row the dialog now displays", async () => {
    const fake = connectFakeClient();
    fake.on("evener/auth/apiKey/set", () => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing for this name now carries a different endpoint.
    rerender(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("different endpoint"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The error asks for the value again; entering it again saves against the
    // destination the dialog is now showing.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The guard above only sees a change the client's listing already reflects.
  // A move that lands between that listing and the write is invisible here -
  // the hub is the one that sees it, and it refuses the assertion
  // (appwire.Conflict: -32013 with evenerErrorInfo "conflict"). That refusal is
  // the same change, so it gets the same recovery the local guard makes: drop
  // the secret, re-read the listing, and re-anchor to the row now on screen, so
  // the retype the message asks for asserts the destination the user can
  // review. Reported as a generic save failure it left the dialog retrying a
  // dead assertion until something else refreshed.
  test("a hub endpoint-refusal drops the secret, re-reads the listing, and re-anchors to the row now on screen", async () => {
    const fake = connectFakeClient();
    const MOVED = instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" });
    fake.on("evener/instance/list", () => ({ instances: [MOVED], availableProviders: [] }));
    let attempts = 0;
    fake.on("evener/auth/apiKey/set", () => {
      attempts += 1;
      if (attempts === 1) {
        throw new WireError(
          "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
          -32013,
          { evenerErrorInfo: ErrorEndpointConflict },
        );
      }
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    // The toast queue is a module singleton shared across this file's tests;
    // clear it so "no save-failure toast" means this refusal pushed none.
    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    // The refusal is reported as the change it is, never as a save failure: the
    // hub's own words stay out of the alert and no "Save failed" toast goes up.
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("different endpoint"));
    expect(screen.getByRole("alert").textContent).not.toContain("no longer resolves");
    expect(screen.queryByText(/Save failed/)).toBeNull();
    // The value belonged to the endpoint that is gone.
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
    // The write that was refused asserted the endpoint the dialog opened on...
    expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
      provider: "work",
      value: "sk-secret",
      expectedEndpointFingerprint: "fp-original",
      originClientId: "test-tab",
    });
    // ...and the dialog re-read the listing to find the row now on screen.
    expect(fake.calls.filter((call) => call.method === "evener/instance/list")).toHaveLength(1);
    // The parent renders this dialog's row from the listing, so the refreshed
    // row is what the dialog now shows; the next submit's guard compares
    // against the destination the re-anchor just adopted.
    rerender(
      <>
        <ApiKeyDialog
          instance={MOVED}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );

    // The re-typed value asserts that row's endpoint, so the retry the message
    // asks for saves against the destination the user can review instead of
    // being refused forever against the one that already moved.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(fake.calls.filter((c) => c.method === "evener/auth/apiKey/set")[1]?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The same refusal with nothing left to re-anchor to: the re-read no longer
  // shows a row for the name at all. There is no destination on screen to
  // assert, so the dialog says the connection changed and the value was not
  // saved - it neither keeps a retry that silently goes nowhere nor empties the
  // assertion, which would let the retype land on whatever the name resolves to
  // next, unverified.
  test("a hub endpoint-refusal whose row is gone reports the change and saves nothing", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    fake.on("evener/auth/apiKey/set", () => {
      throw new WireError(
        "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
        -32013,
        { evenerErrorInfo: ErrorEndpointConflict },
      );
    });
    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("no longer in the provider list"));
    expect(screen.getByRole("alert").textContent).not.toContain("no longer resolves");
    expect(screen.queryByText(/Save failed/)).toBeNull();
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
    expect(fake.calls.filter((call) => call.method === "evener/instance/list")).toHaveLength(1);
    expect(onSuccess).not.toHaveBeenCalled();

    // A retry while the row is still gone is answered by the same honest
    // message, and it still asserts the endpoint the user last reviewed rather
    // than saving unverified.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("no longer in the provider list"));
    expect(fake.calls.filter((c) => c.method === "evener/auth/apiKey/set")[1]?.params).toEqual({
      provider: "work",
      value: "sk-secret-again",
      expectedEndpointFingerprint: "fp-original",
      originClientId: "test-tab",
    });
    expect(onSuccess).not.toHaveBeenCalled();
  });

  // The store's own refusal of a write from the previous connection's listing
  // (stores/credentials.ts's requireWritableClient) is the same change the
  // hub's conflict reports: the endpoint fingerprint this dialog captured, and
  // the value typed against it, belong to a connection that is gone. It gets
  // the same recovery, and the user never sees the store's internal words or a
  // "Save failed" toast for a save that was deliberately not sent.
  test("a save from the previous connection's listing is refused, re-reads, and re-anchors to the row now on screen", async () => {
    const fake = connectFakeClient();
    const MOVED = instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" });
    fake.on("evener/instance/list", () => ({ instances: [MOVED], availableProviders: [] }));
    await credentialsStore.getState().fetch();

    // A replacement whose own listing read is held open: everything on screen
    // still names the connection that is gone. Once released it stays resolved,
    // so the reads the recovery and the retry wait on see one listing.
    let finishRestore!: (value: InstanceListResponse) => void;
    const restore = new Promise<InstanceListResponse>((resolve) => {
      finishRestore = resolve;
    });
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => restore);
    replacement.on("evener/auth/apiKey/set", () => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    // The write was refused before any RPC reached the replacement connection.
    expect(replacement.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
    // The recovery waits on this connection's own listing read; answer it with
    // the row the re-anchor adopts.
    await vi.waitFor(() => expect(replacement.calls.some((call) => call.method === "evener/instance/list")).toBe(true));
    await act(async () => finishRestore({ instances: [MOVED], availableProviders: [] }));
    await vi.waitFor(() => expect(screen.getByRole("alert").textContent).toContain("connection was replaced"));
    expect(screen.getByRole("alert").textContent).not.toContain("credentials store");
    expect(screen.queryByText(/Save failed/)).toBeNull();
    // The value was typed for the listing that is gone.
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The parent renders this dialog's row from the listing, so the refreshed
    // row is what the dialog now shows.
    rerender(
      <>
        <ApiKeyDialog
          instance={MOVED}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );

    // The re-typed value asserts the endpoint the re-anchor adopted, so the
    // retry the message asks for saves against the destination the user can
    // review instead of being refused against one that is gone.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() =>
      expect(replacement.calls.filter((call) => call.method === "evener/auth/apiKey/set")[0]?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  test("a saved key whose listing read is lost is still reported as saved", async () => {
    const fake = connectFakeClient();
    let save!: (value: AuthStatusResponse) => void;
    fake.on(
      "evener/auth/apiKey/set",
      () =>
        new Promise<AuthStatusResponse>((resolve) => {
          save = resolve;
        }),
    );
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    // Dropped connection during the save: the credential write succeeded, and
    // the follow-up listing read rejecting must not turn it into "Save failed".
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    await act(async () =>
      save({ provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false }),
    );
    expect(onSuccess).toHaveBeenCalled();
    expect(within(screen.getByRole("dialog")).queryByText(/no client connected/)).toBeNull();
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
          expectedEndpointFingerprint={undefined}
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
        expectedEndpointFingerprint={undefined}
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
        expectedEndpointFingerprint={undefined}
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
      expect(params).toEqual({ provider: "vertex", value: json, originClientId: "test-tab" });
      return { provider: "vertex", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <CredentialJsonDialog
          instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
          expectedEndpointFingerprint={undefined}
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
          expectedEndpointFingerprint={undefined}
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
