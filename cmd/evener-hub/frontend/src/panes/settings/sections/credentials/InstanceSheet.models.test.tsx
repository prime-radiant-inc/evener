import type {
  InstanceEditParams,
  InstanceEntry,
  InstanceListResponse,
  InstanceModelDetailResponse,
  InstanceModelEditParams,
  InstanceModelEditResponse,
} from "@evener/appwire-client";
import { ErrorEndpointConflict, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { toastKinds } from "../../../../widgets/toast/toastTestUtils";
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
  resetConnectionStoreForTests();
  resetCredentialsStoreForTests();
  resetToastStoreForTests();
  setMutationClientIdentityForTests("test-tab");
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
    expect(document.querySelector('[data-reason="no-match"]')).not.toBeNull();
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

function modelDetailFixture(overrides: Partial<InstanceModelDetailResponse> = {}): InstanceModelDetailResponse {
  return {
    name: "work",
    model: "gpt-5.5",
    disabled: false,
    authoredOnly: false,
    effective: {
      contextWindow: 1_000_000,
      maxOutputTokens: 64_000,
      reasoning: true,
      defaultEffort: "medium",
      effortValues: ["low", "medium", "high"],
      inputModalities: ["text", "image"],
      tools: true,
      protocol: "openai-chat",
      surface: "openai",
      thinkingFormat: "openai",
      thinkingShape: "level",
      wireId: "gpt-5.5",
    },
    provenance: {
      ContextWindow: "catalog",
      MaxOutputTokens: "catalog",
      Reasoning: "catalog",
      DefaultEffort: "catalog",
      EffortValues: "catalog",
      InputModalities: "catalog",
      Tools: "catalog",
      Protocol: "provider",
      Surface: "catalog",
      ThinkingFormat: "catalog",
      ThinkingShape: "catalog",
      WireID: "catalog",
    },
    authored: {},
    thinkingFormats: ["deepseek", "openai", "openrouter", "zai"],
    thinkingShapes: ["adaptive", "budget", "budget+effort", "level"],
    ...overrides,
  };
}

describe("per-model summary rows and drill-in editor", () => {
  test("summarizes customized status, context window, and compaction threshold on model rows", () => {
    const inst: InstanceEntry = {
      ...entry(),
      models: [
        {
          id: "deepseek/deepseek-4.1-flash",
          customized: true,
          contextWindow: 1_000_000,
          compactionThreshold: 250_000,
        },
        { id: "gpt-4o", disabled: true, contextWindow: 128_000 },
      ],
    };
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });
    render(<InstanceSheet name="work" {...handlers()} />);

    const flashSwitch = screen.getByRole("switch", { name: "deepseek/deepseek-4.1-flash" });
    const flashRow = flashSwitch.closest("div")?.parentElement;
    expect(flashRow?.querySelector('[data-kind="customized"]')).not.toBeNull();
    expect(flashRow?.textContent).toContain("1M");
    expect(flashRow?.textContent).toContain("250K");

    const gpt4oRow = screen.getByRole("switch", { name: "gpt-4o" }).closest("div")?.parentElement;
    expect(gpt4oRow?.querySelector('[data-kind="customized"]')).toBeNull();
    expect(gpt4oRow?.textContent).toContain("128K");
    expect(screen.getByRole("button", { name: "Configure deepseek/deepseek-4.1-flash" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "+ Add custom model" })).toBeTruthy();
  });

  test("drills into a model, edits compaction threshold, effort tiers, and modalities, saves, and enables resets", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [{ id: "gpt-5.5", contextWindow: 1_000_000 }],
    };
    fake.on("evener/instance/modelGet", () => modelDetailFixture());
    let capturedEdit: InstanceModelEditParams | undefined;
    fake.on("evener/instance/modelEdit", (raw): InstanceModelEditResponse => {
      const params = raw as InstanceModelEditParams;
      capturedEdit = params;
      if (params.resetAll) {
        return {
          list: { instances: [inst], availableProviders: [] },
          detail: modelDetailFixture(),
        };
      }
      const updatedInst: InstanceEntry = {
        ...inst,
        models: [{ id: "gpt-5.5", customized: true, contextWindow: 1_000_000, compactionThreshold: 250_000 }],
      };
      return {
        list: { instances: [updatedInst], availableProviders: [] },
        detail: modelDetailFixture({
          effective: {
            ...modelDetailFixture().effective,
            compactionThreshold: 250_000,
            effortValues: params.effortValues ?? ["low", "medium", "high"],
            inputModalities: params.inputModalities ?? ["text", "image"],
          },
          provenance: {
            ...modelDetailFixture().provenance,
            CompactionThreshold: "config",
            EffortValues: "config",
            InputModalities: "config",
          },
          authored: {
            compactionThreshold: 250_000,
            effortValues: params.effortValues,
            inputModalities: params.inputModalities,
          },
        }),
      };
    });
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));

    const backButton = await screen.findByRole("button", { name: "← work" });
    expect(document.activeElement).toBe(backButton);

    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    expect(thresholdInput.placeholder).toContain("900K");
    expect(thresholdInput.placeholder).toContain("1M");
    const contextWindowInput = screen.getByLabelText("Context window") as HTMLInputElement;
    expect(contextWindowInput.placeholder).toContain("1M");
    expect(contextWindowInput.placeholder).toContain("catalog");

    // Routing & wire overrides defaults closed when no routing override is authored
    const routingDetails = screen.getByText("Routing & wire overrides").closest("details");
    expect(routingDetails?.hasAttribute("open")).toBe(false);

    // No Reset all overrides before any override is authored
    expect(screen.queryByRole("button", { name: "Reset all overrides" })).toBeNull();

    await user.type(thresholdInput, "250k");
    // Toggle "max" effort tier on and "PDF" modality on
    await user.click(screen.getByRole("button", { name: "max" }));
    await user.click(screen.getByRole("switch", { name: "PDF" }));

    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(capturedEdit).toEqual({
        name: "work",
        model: "gpt-5.5",
        compactionThreshold: 250_000,
        effortValues: ["low", "medium", "high", "max"],
        inputModalities: ["text", "image", "pdf"],
        expectedEndpointFingerprint: "fp-1",
        originClientId: "test-tab",
      }),
    );

    // Baseline updated: Reset all overrides and per-field Reset controls are now available
    const resetAllBtn = await screen.findByRole("button", { name: "Reset all overrides" });
    expect(resetAllBtn.hasAttribute("disabled")).toBe(false);
    expect(screen.getAllByRole("button", { name: "Reset" }).length).toBeGreaterThanOrEqual(3);

    await user.click(resetAllBtn);
    await waitFor(() =>
      expect(capturedEdit).toEqual({
        name: "work",
        model: "gpt-5.5",
        resetAll: true,
        expectedEndpointFingerprint: "fp-1",
        originClientId: "test-tab",
      }),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "Reset all overrides" })).toBeNull());
  });

  test("auto-opens Routing & wire overrides when a routing field is authored", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    fake.on("evener/instance/modelGet", () =>
      modelDetailFixture({
        authored: { protocol: "openai-responses" },
        provenance: { ...modelDetailFixture().provenance, Protocol: "config" },
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [entry()], availableProviders: [], listingFromPreviousConnection: false });

    render(<InstanceSheet name="work" {...handlers()} />);
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));

    await screen.findByLabelText("Auto-compact threshold");
    const routingDetails = screen.getByText("Routing & wire overrides").closest("details");
    expect(routingDetails?.hasAttribute("open")).toBe(true);
  });

  test("surfaces alias-inherited config figures in the model editor placeholders", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    // An alias_of row whose target set these figures in its own config row.
    fake.on("evener/instance/modelGet", () =>
      modelDetailFixture({
        effective: { ...modelDetailFixture().effective, contextWindow: 128_000, compactionThreshold: 250_000 },
        provenance: {
          ...modelDetailFixture().provenance,
          ContextWindow: "config",
          CompactionThreshold: "config",
        },
        authored: { aliasOf: "gpt-4o" },
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [entry()], availableProviders: [], listingFromPreviousConnection: false });

    render(<InstanceSheet name="work" {...handlers()} />);
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));

    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    expect(thresholdInput.placeholder).toContain("250K");
    const contextWindowInput = screen.getByLabelText("Context window") as HTMLInputElement;
    expect(contextWindowInput.placeholder).toContain("128K");
  });

  test("returns to overview on Escape while restoring focus and scrollTop, and closes Sheet on Close button", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    fake.on("evener/instance/modelGet", () => modelDetailFixture());
    connectionStore.getState().connect(fake);
    const h = handlers();
    credentialsStore.setState({ instances: [entry()], availableProviders: [], listingFromPreviousConnection: false });

    render(<InstanceSheet name="work" {...h} />);

    const configureBtn = screen.getByRole("button", { name: "Configure gpt-5.5" });
    const overviewWrapper = configureBtn.closest("[hidden], div") as HTMLElement;
    // Find OverlayPanel's .body container (parent of the overview wrapper)
    const dialog = screen.getByRole("dialog", { name: "work" });
    const sheetBody = Array.from(dialog.children).find((el) => el.contains(configureBtn)) as HTMLElement;
    expect(sheetBody).toBeTruthy();
    sheetBody.scrollTop = 180;

    await user.click(configureBtn);
    await screen.findByLabelText("Auto-compact threshold");

    // While in subview, scrollTop is reset to 0 and overview is hidden
    expect(sheetBody.scrollTop).toBe(0);
    expect(overviewWrapper).toBeTruthy();

    // Pressing Escape inside the subview returns to overview without closing Sheet
    await user.keyboard("{Escape}");

    expect(h.onClose).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("Auto-compact threshold")).toBeNull();
    expect(sheetBody.scrollTop).toBe(180);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Configure gpt-5.5" }));

    // Clicking Sheet's Close button dismisses InstanceSheet even when inside the subview
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    await screen.findByLabelText("Auto-compact threshold");
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(h.onClose).toHaveBeenCalledTimes(1);
  });

  test("preserves an unsaved connection draft across a default-model protocol edit that moves endpointFingerprint", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const initialInst: InstanceEntry = {
      ...entry(),
      protocol: "openai-chat",
      endpointFingerprint: "fp-before",
      models: [{ id: "gpt-5.5" }],
    };
    const afterModelEditInst: InstanceEntry = {
      ...initialInst,
      protocol: "openai-responses",
      endpointFingerprint: "fp-after-model",
      models: [{ id: "gpt-5.5", customized: true }],
    };
    fake.on("evener/instance/modelGet", () => modelDetailFixture());
    fake.on(
      "evener/instance/modelEdit",
      (): InstanceModelEditResponse => ({
        list: { instances: [afterModelEditInst], availableProviders: [] },
        detail: modelDetailFixture({
          effective: { ...modelDetailFixture().effective, protocol: "openai-responses" },
          authored: { protocol: "openai-responses" },
        }),
      }),
    );
    let capturedInstanceEdit: InstanceEditParams | undefined;
    fake.on("evener/instance/edit", (raw): InstanceListResponse => {
      capturedInstanceEdit = raw as InstanceEditParams;
      return {
        instances: [{ ...afterModelEditInst, apiKeyEnv: "CUSTOM_WORK_KEY" }],
        availableProviders: [],
      };
    });
    connectionStore.getState().connect(fake);
    credentialsStore.setState({
      instances: [initialInst],
      availableProviders: [],
      listingFromPreviousConnection: false,
    });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    // 1. Edit API key environment variable on the connection overview without saving
    const apiKeyEnvInput = screen.getByLabelText("API key environment variable") as HTMLInputElement;
    await user.type(apiKeyEnvInput, "CUSTOM_WORK_KEY");

    // 2. Drill into default model gpt-5.5, change Protocol to openai-responses, and save
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    await screen.findByLabelText("Auto-compact threshold");
    await user.click(screen.getByText("Routing & wire overrides"));
    const modelProtocolSelect = screen.getByRole("combobox", { name: "Protocol" }) as HTMLSelectElement;
    await user.selectOptions(modelProtocolSelect, "openai-responses");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/instance/modelEdit")).toBe(true));

    // 3. Return to overview via <- work and click Save on the connection form
    await user.click(screen.getByRole("button", { name: "← work" }));
    expect((screen.getByLabelText("API key environment variable") as HTMLInputElement).value).toBe("CUSTOM_WORK_KEY");

    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(capturedInstanceEdit).toEqual({
        name: "work",
        apiKeyEnv: "CUSTOM_WORK_KEY",
        expectedEndpointFingerprint: "fp-after-model",
        originClientId: "test-tab",
      }),
    );
    expect(document.querySelector('[role="alert"][data-reason="instance-replaced"]')).toBeNull();
  });

  test("creates a custom model and removes it via the sibling ConfirmDialog outside Sheet", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [{ id: "gpt-5.5" }],
    };
    const modelEdits: InstanceModelEditParams[] = [];
    fake.on("evener/instance/modelEdit", (raw): InstanceModelEditResponse => {
      const params = raw as InstanceModelEditParams;
      modelEdits.push(params);
      if (params.removeCustom) {
        return {
          list: { instances: [inst], availableProviders: [] },
        };
      }
      const withCustom: InstanceEntry = {
        ...inst,
        models: [...(inst.models ?? []), { id: params.model, customized: true, compactionThreshold: 250_000 }],
      };
      return {
        list: { instances: [withCustom], availableProviders: [] },
        detail: modelDetailFixture({
          model: params.model,
          authoredOnly: true,
          effective: { ...modelDetailFixture().effective, compactionThreshold: 250_000 },
          authored: { compactionThreshold: 250_000 },
        }),
      };
    });
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "+ Add custom model" }));

    const modelIdInput = (await screen.findByLabelText("Model ID")) as HTMLInputElement;
    expect(document.activeElement).toBe(modelIdInput);
    // Routing & wire overrides defaults open in create mode
    expect(screen.getByText("Routing & wire overrides").closest("details")?.hasAttribute("open")).toBe(true);

    await user.type(modelIdInput, "deepseek/custom-r1");
    await user.type(screen.getByLabelText("Auto-compact threshold"), "250k");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(modelEdits[0]).toEqual({
        name: "work",
        model: "deepseek/custom-r1",
        create: true,
        compactionThreshold: 250_000,
        expectedEndpointFingerprint: "fp-1",
        originClientId: "test-tab",
      }),
    );

    // Now in edit mode for the authoredOnly model, Remove custom model is available
    const removeCustomBtn = await screen.findByRole("button", { name: "Remove custom model" });
    // Routing & wire overrides closes on create -> edit transition when no routing overrides were authored
    expect(screen.getByText("Routing & wire overrides").closest("details")?.hasAttribute("open")).toBe(false);
    await user.click(removeCustomBtn);

    // ConfirmDialog is rendered as a sibling dialog outside the Sheet dialog
    const sheetDialog = screen.getByRole("dialog", { name: "work" });
    const confirmDialog = screen.getByRole("dialog", { name: "Remove custom model" });
    expect(sheetDialog.contains(confirmDialog)).toBe(false);

    await user.click(within(confirmDialog).getByRole("button", { name: "Remove" }));

    await waitFor(() =>
      expect(modelEdits[1]).toEqual({
        name: "work",
        model: "deepseek/custom-r1",
        removeCustom: true,
        expectedEndpointFingerprint: "fp-1",
        originClientId: "test-tab",
      }),
    );
    // Returned to overview
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Remove custom model" })).toBeNull());
    expect(screen.getByRole("button", { name: "+ Add custom model" })).toBeTruthy();
  });

  test("disables model writes when writesRefused is true while keeping modelGet inspectable", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    fake.on("evener/instance/modelGet", () =>
      modelDetailFixture({
        authoredOnly: true,
        authored: { compactionThreshold: 250_000 },
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [entry()], availableProviders: [], listingFromPreviousConnection: false });

    render(<InstanceSheet name="work" {...handlers()} writesRefused />);

    expect((screen.getByRole("button", { name: "+ Add custom model" }) as HTMLButtonElement).disabled).toBe(true);

    // Configure remains readable
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    await screen.findByLabelText("Auto-compact threshold");

    expect((screen.getByRole("button", { name: "Reset all overrides" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Remove custom model" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);
  });

  test("refuses Configure and + Add custom model during the rename reconciliation gap", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    // Return an unconfirmed listing on rename so the sheet enters renameGap
    fake.on(
      "evener/instance/edit",
      (): InstanceListResponse => ({
        instances: [],
        availableProviders: [],
      }),
    );
    connectionStore.getState().connect(fake);
    const h = handlers();
    credentialsStore.setState({ instances: [entry()], availableProviders: [], listingFromPreviousConnection: false });

    const view = render(
      <>
        <Toast />
        <InstanceSheet name="work" {...h} />
      </>,
    );

    const nameInput = screen.getByLabelText("Name");
    await user.clear(nameInput);
    await user.type(nameInput, "work2");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(h.onRenamed).toHaveBeenCalledWith("work2"));

    view.rerender(
      <>
        <Toast />
        <InstanceSheet name="work2" {...h} />
      </>,
    );

    act(() => resetToastStoreForTests());
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    expect(toastKinds()).toContain("warning");
    expect(fake.calls.some((c) => c.method === "evener/instance/modelGet")).toBe(false);

    act(() => resetToastStoreForTests());
    await user.click(screen.getByRole("button", { name: "+ Add custom model" }));
    expect(toastKinds()).toContain("warning");
    expect(screen.queryByLabelText("Model ID")).toBeNull();
  });

  test("surfaces inline validation alerts and sends clearCompactionThreshold on per-field Reset", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [{ id: "gpt-5.5", customized: true, contextWindow: 1_000_000, compactionThreshold: 250_000 }],
    };
    fake.on("evener/instance/modelGet", () =>
      modelDetailFixture({
        effective: { ...modelDetailFixture().effective, compactionThreshold: 250_000 },
        authored: { compactionThreshold: 250_000 },
        provenance: { ...modelDetailFixture().provenance, CompactionThreshold: "config" },
      }),
    );
    let capturedEdit: InstanceModelEditParams | undefined;
    fake.on("evener/instance/modelEdit", (raw): InstanceModelEditResponse => {
      capturedEdit = raw as InstanceModelEditParams;
      return {
        list: {
          instances: [{ ...inst, models: [{ id: "gpt-5.5", contextWindow: 1_000_000 }] }],
          availableProviders: [],
        },
        detail: modelDetailFixture(),
      };
    });
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    expect(thresholdInput.value).toBe("250000");

    // Entering a threshold >= contextWindow (1M) raises an alert wired via aria-describedby and disables Save
    await user.clear(thresholdInput);
    await user.type(thresholdInput, "1M");
    const alertEl = screen.getByRole("alert");
    expect(thresholdInput.getAttribute("aria-describedby")).toBe(alertEl.id);
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(true);

    // Clicking per-field Reset clears the field back to inherited and enables Save with clearCompactionThreshold
    await user.click(screen.getByRole("button", { name: "Reset" }));
    expect(thresholdInput.value).toBe("");
    expect(screen.queryByRole("alert")).toBeNull();
    expect((screen.getByRole("button", { name: "Save" }) as HTMLButtonElement).disabled).toBe(false);

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(capturedEdit).toEqual({
        name: "work",
        model: "gpt-5.5",
        clearCompactionThreshold: true,
        expectedEndpointFingerprint: "fp-1",
        originClientId: "test-tab",
      }),
    );
  });

  test("does not re-anchor InstanceSheet or InstanceModelEditor when editModel is superseded by a newer listing", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const initialInst: InstanceEntry = {
      ...entry(),
      protocol: "openai-chat",
      endpointFingerprint: "fp-before",
      models: [{ id: "gpt-5.5" }],
    };
    const foreignReplacement: InstanceEntry = {
      ...initialInst,
      providerId: "anthropic",
      protocol: "anthropic",
      endpointFingerprint: "fp-foreign",
    };
    fake.on("evener/instance/modelGet", () => modelDetailFixture());
    let resolveModelEdit: ((resp: InstanceModelEditResponse) => void) | undefined;
    fake.on(
      "evener/instance/modelEdit",
      () =>
        new Promise<InstanceModelEditResponse>((resolve) => {
          resolveModelEdit = resolve;
        }),
    );
    fake.on(
      "evener/instance/list",
      (): InstanceListResponse => ({
        instances: [foreignReplacement],
        availableProviders: [],
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({
      instances: [initialInst],
      availableProviders: [],
      listingFromPreviousConnection: false,
    });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    // Make an unsaved edit on the overview connection form
    await user.type(screen.getByLabelText("API key environment variable"), "WORK_KEY_DRAFT");

    // Drill into model editor and start a save
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    await user.type(thresholdInput, "250k");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(resolveModelEdit).toBeDefined());

    // A newer listing arrives while editModel is in flight, superseding editModel's response
    await act(async () => {
      await credentialsStore.getState().fetch();
    });
    act(() => resetToastStoreForTests());
    await act(async () => {
      resolveModelEdit?.({
        list: {
          instances: [{ ...initialInst, endpointFingerprint: "fp-model-save" }],
          availableProviders: [],
        },
        detail: modelDetailFixture({
          effective: { ...modelDetailFixture().effective, compactionThreshold: 250_000 },
          authored: { compactionThreshold: 250_000 },
        }),
      });
    });

    // Superseded save warns rather than claiming success, and keeps the user's draft
    expect(toastKinds()).toContain("warning");
    expect(toastKinds()).not.toContain("success");
    expect(thresholdInput.value).toBe("250k");

    // Returning to the overview and attempting to save the connection draft must refuse
    // with instance-replaced rather than having re-anchored onto foreignReplacement
    await user.click(screen.getByRole("button", { name: "← work" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(document.querySelector('[role="alert"][data-reason="instance-replaced"]')).not.toBeNull();
    expect(fake.calls.some((c) => c.method === "evener/instance/edit")).toBe(false);
  });

  test("never sends a stale draft onto a replacement instance when Save is clicked a second time after instance-replaced", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const initialInst: InstanceEntry = {
      ...entry(),
      protocol: "openai-chat",
      endpointFingerprint: "fp-initial",
      models: [{ id: "gpt-5.5" }],
    };
    const foreignReplacement: InstanceEntry = {
      ...initialInst,
      providerId: "anthropic",
      protocol: "anthropic",
      endpointFingerprint: "fp-replacement",
      models: [{ id: "gpt-5.5" }],
    };
    let modelGetCount = 0;
    let resolveReplacementGet: ((detail: InstanceModelDetailResponse) => void) | undefined;
    fake.on("evener/instance/modelGet", () => {
      modelGetCount += 1;
      if (modelGetCount === 1) {
        return modelDetailFixture();
      }
      return new Promise<InstanceModelDetailResponse>((resolve) => {
        resolveReplacementGet = resolve;
      });
    });
    fake.on(
      "evener/instance/modelEdit",
      (): InstanceModelEditResponse => ({
        list: { instances: [foreignReplacement], availableProviders: [] },
        detail: modelDetailFixture(),
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({
      instances: [initialInst],
      availableProviders: [],
      listingFromPreviousConnection: false,
    });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    await user.type(thresholdInput, "250k");

    // Replace the instance in store under the same name before saving
    act(() => {
      credentialsStore.setState({
        instances: [foreignReplacement],
        availableProviders: [],
        listingFromPreviousConnection: false,
      });
    });

    // First Save click refuses with instance-replaced and starts re-seeding from replacement
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(document.querySelector('[role="alert"][data-reason="instance-replaced"]')).not.toBeNull();
    expect(fake.calls.some((c) => c.method === "evener/instance/modelEdit")).toBe(false);

    // Second Save click while reload is in flight is still refused
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(fake.calls.some((c) => c.method === "evener/instance/modelEdit")).toBe(false);

    // Resolve replacement getModel; draft resets to replacement's clean state
    await act(async () => {
      resolveReplacementGet?.(modelDetailFixture());
    });
    expect((screen.getByLabelText("Auto-compact threshold") as HTMLInputElement).value).toBe("");

    // Clicking Save again still does not send the old 250k edit
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(fake.calls.some((c) => c.method === "evener/instance/modelEdit")).toBe(false);
  });

  test("late custom-model creation completion does not hijack a newer model editor view", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [{ id: "gpt-5.5" }],
    };
    fake.on("evener/instance/modelGet", () => modelDetailFixture({ model: "gpt-5.5" }));
    let resolveCreate: ((resp: InstanceModelEditResponse) => void) | undefined;
    fake.on(
      "evener/instance/modelEdit",
      () =>
        new Promise<InstanceModelEditResponse>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    // 1. Start creating custom model with delayed response
    await user.click(screen.getByRole("button", { name: "+ Add custom model" }));
    await user.type(await screen.findByLabelText("Model ID"), "custom-delayed");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(resolveCreate).toBeDefined());

    // 2. Navigate back to overview and open Configure gpt-5.5, then type an unsaved threshold edit
    await user.click(screen.getByRole("button", { name: "← work" }));
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    await user.type(thresholdInput, "300k");

    // 3. Resolve the delayed custom-model creation
    await act(async () => {
      resolveCreate?.({
        list: {
          instances: [{ ...inst, models: [{ id: "gpt-5.5" }, { id: "custom-delayed", customized: true }] }],
          availableProviders: [],
        },
        detail: modelDetailFixture({ model: "custom-delayed", authoredOnly: true }),
      });
    });

    // Editor remains on gpt-5.5 with the unsaved "300k" draft intact
    expect(screen.getByRole("heading", { name: "gpt-5.5" })).toBeTruthy();
    expect((screen.getByLabelText("Auto-compact threshold") as HTMLInputElement).value).toBe("300k");
  });

  test("closes ConfirmDialog and refreshes listing when custom-model removal hits an endpoint conflict", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [{ id: "custom-r1", customized: true }],
    };
    fake.on("evener/instance/modelGet", () =>
      modelDetailFixture({
        model: "custom-r1",
        authoredOnly: true,
      }),
    );
    fake.on("evener/instance/modelEdit", () => {
      throw new WireError("endpoint conflict", -32013, { evenerErrorInfo: ErrorEndpointConflict });
    });
    fake.on(
      "evener/instance/list",
      (): InstanceListResponse => ({
        instances: [{ ...inst, endpointFingerprint: "fp-2" }],
        availableProviders: [],
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Configure custom-r1" }));
    await user.click(await screen.findByRole("button", { name: "Remove custom model" }));
    const confirmDialog = screen.getByRole("dialog", { name: "Remove custom model" });
    await user.click(within(confirmDialog).getByRole("button", { name: "Remove" }));

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Remove custom model" })).toBeNull());
    expect(toastKinds()).toContain("error");
    expect(fake.calls.some((c) => c.method === "evener/instance/list")).toBe(true);
  });

  test("does not bind an in-flight modelGet response to a same-name replacement that arrived before modelGet resolved", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const initialInst: InstanceEntry = {
      ...entry(),
      providerId: "openai",
      protocol: "openai-chat",
      endpointFingerprint: "fp-old",
      models: [{ id: "gpt-5.5" }],
    };
    const replacementInst: InstanceEntry = {
      ...initialInst,
      providerId: "anthropic",
      protocol: "anthropic",
      endpointFingerprint: "fp-new",
      models: [{ id: "gpt-5.5" }],
    };
    let modelGetCount = 0;
    let resolveFirstGet: ((detail: InstanceModelDetailResponse) => void) | undefined;
    fake.on("evener/instance/modelGet", () => {
      modelGetCount += 1;
      if (modelGetCount === 1) {
        return new Promise<InstanceModelDetailResponse>((resolve) => {
          resolveFirstGet = resolve;
        });
      }
      return new Promise<InstanceModelDetailResponse>(() => {});
    });
    fake.on(
      "evener/instance/modelEdit",
      (): InstanceModelEditResponse => ({
        list: { instances: [replacementInst], availableProviders: [] },
        detail: modelDetailFixture(),
      }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({
      instances: [initialInst],
      availableProviders: [],
      listingFromPreviousConnection: false,
    });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    // 1. Open Configure gpt-5.5 while initialInst (fp-old) is active; modelGet is delayed
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    await waitFor(() => expect(resolveFirstGet).toBeDefined());

    // 2. While modelGet is in flight, the same-name instance is replaced in store with replacementInst (fp-new)
    act(() => {
      credentialsStore.setState({
        instances: [replacementInst],
        availableProviders: [],
        listingFromPreviousConnection: false,
      });
    });

    // 3. Resolve the first modelGet (which was requested for initialInst)
    await act(async () => {
      resolveFirstGet?.(modelDetailFixture());
    });

    // 4. Edit threshold and click Save -> must refuse with instance-replaced and zero modelEdit calls
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    await user.type(thresholdInput, "250k");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(document.querySelector('[role="alert"][data-reason="instance-replaced"]')).not.toBeNull();
    expect(fake.calls.some((c) => c.method === "evener/instance/modelEdit")).toBe(false);
  });

  test("dismissing a pending custom-model removal and opening another model preserves the newer editor and draft when removal completes", async () => {
    const user = userEvent.setup();
    const fake = new FakeClient("ready");
    const inst: InstanceEntry = {
      ...entry(),
      endpointFingerprint: "fp-1",
      models: [
        { id: "custom-r1", customized: true },
        { id: "gpt-5.5", contextWindow: 1_000_000 },
      ],
    };
    fake.on("evener/instance/modelGet", (raw) => {
      const p = raw as { model: string };
      if (p.model === "custom-r1") {
        return modelDetailFixture({
          model: "custom-r1",
          authoredOnly: true,
          authored: { compactionThreshold: 200_000 },
        });
      }
      return modelDetailFixture({ model: "gpt-5.5" });
    });
    let resolveRemove: ((resp: InstanceModelEditResponse) => void) | undefined;
    fake.on(
      "evener/instance/modelEdit",
      () =>
        new Promise<InstanceModelEditResponse>((resolve) => {
          resolveRemove = resolve;
        }),
    );
    connectionStore.getState().connect(fake);
    credentialsStore.setState({ instances: [inst], availableProviders: [], listingFromPreviousConnection: false });

    render(
      <>
        <Toast />
        <InstanceSheet name="work" {...handlers()} />
      </>,
    );

    await user.click(screen.getByRole("button", { name: "Configure custom-r1" }));
    await user.click(await screen.findByRole("button", { name: "Remove custom model" }));
    const confirmDialog = screen.getByRole("dialog", { name: "Remove custom model" });
    await user.click(within(confirmDialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(resolveRemove).toBeDefined());

    // Dismiss ConfirmDialog via Close while removal is pending, return to overview, open gpt-5.5, and enter an unsaved edit
    await user.click(within(confirmDialog).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Remove custom model" })).toBeNull());
    await user.click(screen.getByRole("button", { name: "← work" }));
    await user.click(screen.getByRole("button", { name: "Configure gpt-5.5" }));
    const thresholdInput = (await screen.findByLabelText("Auto-compact threshold")) as HTMLInputElement;
    await user.type(thresholdInput, "250k");
    expect(thresholdInput.value).toBe("250k");

    // Now the earlier custom-r1 removal completes
    await act(async () => {
      resolveRemove?.({
        list: {
          instances: [{ ...inst, models: [{ id: "gpt-5.5", contextWindow: 1_000_000 }] }],
          availableProviders: [],
        },
        detail: undefined,
      });
    });

    // The newer gpt-5.5 editor must remain mounted with its unsaved "250k" draft intact
    expect(screen.getByRole("heading", { name: "gpt-5.5" })).toBeTruthy();
    expect((screen.getByLabelText("Auto-compact threshold") as HTMLInputElement).value).toBe("250k");
  });
});
