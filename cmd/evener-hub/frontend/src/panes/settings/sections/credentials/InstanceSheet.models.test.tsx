import type { InstanceEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
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
    expect(button.getAttribute("aria-disabled")).toBe("false");
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
    // A refusal, not the native attribute: the click that started the refresh
    // keeps the keyboard on this button (see widgets/switch and Button).
    expect(button.getAttribute("aria-disabled")).toBe("true");
  });
});

// A provider such as OpenRouter lists hundreds of models; the sheet mounts at
// most MODEL_LIST_CAP switches and offers a search to reach the rest (issue
// #3279, the same decision the phone makes).
describe("long model lists", () => {
  function manyModels(count: number) {
    return Array.from({ length: count }, (_, index) => ({
      id: `model-${String(index).padStart(2, "0")}`,
    }));
  }

  test("mounts at most the cap and says how many are hidden", () => {
    credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(60) }], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} />);
    expect(screen.getAllByRole("switch")).toHaveLength(50);
    expect(screen.getByText("Showing 50 of 60 models — search to narrow.")).toBeTruthy();
  });

  test("filters to the typed search, reaching a model past the cap", () => {
    credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(60) }], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "model-59" } });
    expect(screen.getAllByRole("switch")).toHaveLength(1);
    expect(screen.getByRole("switch", { name: "model-59" })).toBeTruthy();
  });

  test("says so when the search matches no model", () => {
    credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(60) }], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "zzz" } });
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.getByText("No matching models.")).toBeTruthy();
  });

  test("leaves a short list uncapped, with no search field", () => {
    credentialsStore.setState({ instances: [entry()], availableProviders: [] });
    render(<InstanceSheet name="work" {...handlers()} />);
    expect(screen.queryByRole("searchbox", { name: "Search models" })).toBeNull();
    expect(screen.getAllByRole("switch")).toHaveLength(2);
  });

  test("clears the search when a different instance opens", () => {
    credentialsStore.setState({
      instances: [
        { ...entry(), models: manyModels(60) },
        { ...entry(), name: "other", models: manyModels(2) },
      ],
      availableProviders: [],
    });
    const h = handlers();
    const view = render(<InstanceSheet name="work" {...h} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "zzz" } });
    view.rerender(<InstanceSheet name="other" {...h} />);
    // The seed cleared the query, so no stale filter hides the short instance's rows.
    expect(screen.queryByRole("searchbox", { name: "Search models" })).toBeNull();
    expect(screen.getAllByRole("switch")).toHaveLength(2);
  });

  test("keeps the search clearable when a refresh drops the list below the cap", () => {
    credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(60) }], availableProviders: [] });
    const h = handlers();
    const view = render(<InstanceSheet name="work" {...h} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "zzz" } });
    act(() => {
      credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(2) }], availableProviders: [] });
    });
    view.rerender(<InstanceSheet name="work" {...h} />);
    // The filter is still active, so the field stays to clear it rather than hiding the rows.
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "" } });
    expect(screen.getAllByRole("switch")).toHaveLength(2);
  });

  test("keeps the field while only whitespace is typed, even below the cap", () => {
    credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(60) }], availableProviders: [] });
    const h = handlers();
    const view = render(<InstanceSheet name="work" {...h} />);
    fireEvent.change(screen.getByRole("searchbox", { name: "Search models" }), { target: { value: "   " } });
    act(() => {
      credentialsStore.setState({ instances: [{ ...entry(), models: manyModels(2) }], availableProviders: [] });
    });
    view.rerender(<InstanceSheet name="work" {...h} />);
    // Whitespace trims to no filter, so nothing is hidden, and the field stays to clear it.
    expect(screen.getByRole("searchbox", { name: "Search models" })).toBeTruthy();
    expect(screen.getAllByRole("switch")).toHaveLength(2);
  });
});
