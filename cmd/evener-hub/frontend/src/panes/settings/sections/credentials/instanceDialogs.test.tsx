import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry, ProviderDescriptor } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { AddInstanceDialog, ApiKeyDialog, CredentialJsonDialog, EditInstanceDialog } from "./instanceDialogs";

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
});

describe("EditInstanceDialog", () => {
  test("Base URL is pre-filled from the instance", () => {
    connectFakeClient();
    render(
      <EditInstanceDialog
        instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://existing" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByLabelText(/base url/i)).toHaveProperty("value", "https://existing");
  });

  test("emptying a set Base URL clears it back to the provider default", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", clearBaseUrl: true });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <EditInstanceDialog
        instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://existing" })}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    await user.clear(screen.getByLabelText(/base url/i));
    // InstanceEditParams.baseUrl keeps its old "empty means unchanged"
    // meaning (v3); clearBaseUrl is the additive signal that actually
    // drops the authored override back to the provider's default (#711).
    expect(screen.getByText(/resets the endpoint to the provider's default/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", false);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  test("an instance with no Base URL of its own may be saved empty", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work" });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    render(
      <EditInstanceDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    expect(screen.queryByText(/resets the endpoint to the provider's default/i)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  test("submit with an unchanged Base URL sends only { name }", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work" });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <EditInstanceDialog
          instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://existing" })}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  test("submit with a changed Base URL sends { name, baseUrl }", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", baseUrl: "https://x" });
      return { instances: [], availableProviders: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <EditInstanceDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.clear(screen.getByLabelText(/base url/i));
    await user.type(screen.getByLabelText(/base url/i), "https://x");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Saved work").length).toBeGreaterThan(0);
  });

  test("a failure shows an inline error and an 'Edit failed' toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/instance/edit", () => {
      throw new Error("boom");
    });
    const user = userEvent.setup();
    render(
      <>
        <EditInstanceDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          onCancel={() => {}}
          onSuccess={() => {}}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("boom");
    expect(screen.getAllByText("Edit failed: boom").length).toBeGreaterThan(0);
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
