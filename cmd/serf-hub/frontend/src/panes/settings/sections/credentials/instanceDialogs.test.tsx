import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { Toast } from "../../../../widgets";
import { AddInstanceDialog, ApiKeyDialog, EditInstanceDialog } from "./instanceDialogs";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function instance(overrides: Partial<InstanceEntry> & Pick<InstanceEntry, "name" | "type">): InstanceEntry {
  return {
    apiStyle: "",
    baseUrl: "",
    isDefault: false,
    activeSource: "absent",
    hasStoredOAuth: false,
    ...overrides,
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cleanup();
});

describe("AddInstanceDialog", () => {
  test("Type select is populated from availableTypes", () => {
    connectFakeClient();
    render(<AddInstanceDialog availableTypes={["anthropic", "openai"]} onCancel={() => {}} onSuccess={() => {}} />);
    const select = screen.getByLabelText("Type") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "anthropic", "openai"]);
  });

  test("the API-style radio block is hidden until Type is openai, live on change", async () => {
    connectFakeClient();
    render(<AddInstanceDialog availableTypes={["anthropic", "openai"]} onCancel={() => {}} onSuccess={() => {}} />);
    expect(screen.queryByRole("radiogroup", { name: /api style/i })).toBeNull();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Type"), "openai");
    expect(screen.getByRole("radiogroup", { name: /api style/i })).toBeTruthy();
  });

  test("client-side validation: Type required, then Name required", async () => {
    connectFakeClient();
    const user = userEvent.setup();
    render(<AddInstanceDialog availableTypes={["anthropic"]} onCancel={() => {}} onSuccess={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(screen.getByText("Type is required.")).toBeTruthy();
    await user.selectOptions(screen.getByLabelText("Type"), "anthropic");
    await user.click(screen.getByRole("button", { name: "Create" }));
    expect(screen.getByText("Name is required.")).toBeTruthy();
  });

  test("apiStyle is sent only for type openai, forced to '' otherwise even with a stale radio selection", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/create", (params) => {
      expect(params).toEqual({ type: "anthropic", name: "work", apiStyle: "", baseUrl: "" });
      return { instances: [], availableTypes: ["anthropic"] };
    });
    const user = userEvent.setup();
    render(<AddInstanceDialog availableTypes={["anthropic", "openai"]} onCancel={() => {}} onSuccess={() => {}} />);
    // Select openai first (revealing + implicitly defaulting the apiStyle
    // radio to "responses"), then switch back to anthropic - the stale
    // radio selection must not leak into the payload.
    await user.selectOptions(screen.getByLabelText("Type"), "openai");
    await user.selectOptions(screen.getByLabelText("Type"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/instance/create")).toBe(true));
  });

  test("submit calls instanceCreate and, on success, toasts + calls onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/create", (params) => {
      expect(params).toEqual({ type: "anthropic", name: "work", apiStyle: "", baseUrl: "https://x" });
      return { instances: [], availableTypes: ["anthropic"] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableTypes={["anthropic"]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Type"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.type(screen.getByLabelText(/base url/i), "https://x");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(screen.getAllByText("Created instance work").length).toBeGreaterThan(0);
  });

  test("a create failure shows an inline error and a 'Create failed' toast, without calling onSuccess", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/create", () => {
      throw new Error("name already exists");
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <AddInstanceDialog availableTypes={["anthropic"]} onCancel={() => {}} onSuccess={onSuccess} />
        <Toast />
      </>,
    );
    await user.selectOptions(screen.getByLabelText("Type"), "anthropic");
    await user.type(screen.getByLabelText("Name"), "work");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await screen.findByText("name already exists");
    expect(screen.getAllByText("Create failed: name already exists").length).toBeGreaterThan(0);
    expect(onSuccess).not.toHaveBeenCalled();
  });
});

describe("EditInstanceDialog", () => {
  test("shows the API-style radio, pre-checked, only when the instance type is openai", () => {
    connectFakeClient();
    const { rerender } = render(
      <EditInstanceDialog
        instance={instance({ name: "work", type: "openai", apiStyle: "chat-completions" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByRole("radio", { name: "chat-completions" }).getAttribute("aria-checked")).toBe("true");

    rerender(
      <EditInstanceDialog
        instance={instance({ name: "work", type: "anthropic" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.queryByRole("radiogroup", { name: /api style/i })).toBeNull();
  });

  test("Base URL is pre-filled from the instance", () => {
    connectFakeClient();
    render(
      <EditInstanceDialog
        instance={instance({ name: "work", type: "anthropic", baseUrl: "https://existing" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByLabelText(/base url/i)).toHaveProperty("value", "https://existing");
  });

  test("submit calls instanceEdit with apiStyle '' when the instance isn't openai", async () => {
    const fake = connectFakeClient();
    fake.on("serf/instance/edit", (params) => {
      expect(params).toEqual({ name: "work", apiStyle: "", baseUrl: "https://x" });
      return { instances: [], availableTypes: [] };
    });
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <EditInstanceDialog
          instance={instance({ name: "work", type: "anthropic" })}
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
    fake.on("serf/instance/edit", () => {
      throw new Error("boom");
    });
    const user = userEvent.setup();
    render(
      <>
        <EditInstanceDialog
          instance={instance({ name: "work", type: "anthropic" })}
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
    fake.on("serf/auth/apiKey/set", setKey);
    const onCancel = vi.fn();
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", type: "anthropic" })}
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
    fake.on("serf/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "file", hasStoredOAuth: false };
    });
    fake.on("serf/instance/list", () => ({ instances: [], availableTypes: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", type: "anthropic" })}
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
    fake.on("serf/auth/apiKey/set", () => {
      throw new Error("rejected");
    });
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", type: "anthropic" })}
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
        instance={instance({ name: "work", type: "anthropic" })}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByLabelText(/api key/i, { selector: "input" }).getAttribute("type")).toBe("password");
  });
});
