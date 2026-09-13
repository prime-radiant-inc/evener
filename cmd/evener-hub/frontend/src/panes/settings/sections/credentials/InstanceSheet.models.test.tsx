import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { InstanceSheet } from "./InstanceSheet";

function entry(): InstanceEntry {
  return {
    name: "work",
    providerId: "openai",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "env:WORK_KEY",
    hasStoredOAuth: false,
    credentialRequired: true,
    models: [{ id: "gpt-5.5" }, { id: "gpt-4o", disabled: true }],
  };
}

function handlers() {
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
    onClose: vi.fn(),
  };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetCredentialsStoreForTests();
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("model toggles", () => {
  test("renders one switch per model row, reflecting the disabled flag", () => {
    credentialsStore.setState({ instances: [entry()], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} />);
    const enabled = screen.getByRole("switch", { name: "gpt-5.5" });
    const disabled = screen.getByRole("switch", { name: "gpt-4o" });
    expect(enabled.getAttribute("aria-checked")).toBe("true");
    expect(disabled.getAttribute("aria-checked")).toBe("false");
  });

  test("flipping a switch delegates to the section owner", () => {
    const h = handlers();
    credentialsStore.setState({ instances: [entry()], availableProviders: [] });
    render(<InstanceSheet name="work" {...h} />);
    fireEvent.click(screen.getByRole("switch", { name: "gpt-5.5" }));
    expect(h.onToggleModel).toHaveBeenCalledWith("gpt-5.5", true);
    fireEvent.click(screen.getByRole("switch", { name: "gpt-4o" }));
    expect(h.onToggleModel).toHaveBeenCalledWith("gpt-4o", false);
  });

  test("models section shows the refresh button even when the instance carries no inventory", () => {
    const h = handlers();
    credentialsStore.setState({ instances: [{ ...entry(), models: undefined }], availableProviders: [] });
    render(<InstanceSheet name="work" {...h} />);
    expect(screen.getByText("Models")).toBeTruthy();
    expect(screen.queryByRole("switch")).toBeNull();
    const button = screen.getByRole("button", { name: "Refresh live models" });
    fireEvent.click(button);
    expect(h.onRefreshModels).toHaveBeenCalledTimes(1);
  });

  test("refresh button stays enabled when writes are refused: refresh is a read", () => {
    const h = handlers();
    credentialsStore.setState({ instances: [{ ...entry(), models: undefined }], availableProviders: [] });
    render(<InstanceSheet name="work" {...h} writesRefused />);
    const button = screen.getByRole("button", { name: "Refresh live models" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(button);
    expect(h.onRefreshModels).toHaveBeenCalledTimes(1);
  });

  test("refresh button delegates to the section owner and disables while refreshing", () => {
    const h = handlers();
    credentialsStore.setState({ instances: [entry()], availableProviders: [] });
    render(<InstanceSheet name="work" {...h} />);
    const button = screen.getByRole("button", { name: "Refresh live models" });
    fireEvent.click(button);
    expect(h.onRefreshModels).toHaveBeenCalledTimes(1);
  });

  test("refresh button shows pending state while a refresh is in flight", () => {
    credentialsStore.setState({ instances: [entry()], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} modelsRefreshing />);
    const button = screen.getByRole("button", { name: "Refreshing live models…" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });
});
