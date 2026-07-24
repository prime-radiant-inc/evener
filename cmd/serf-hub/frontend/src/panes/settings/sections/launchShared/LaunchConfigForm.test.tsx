import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
  PathValidateResponse,
} from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { Toast } from "../../../../widgets";
import { LaunchConfigForm } from "./LaunchConfigForm";

afterEach(cleanup);

// The path-kind rows render PathField, whose completion loader is the
// extensions store's own completePaths - so opening one needs a connected
// client behind it.
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  vi.useRealTimers();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

const OPTIONS: LaunchOption[] = [
  {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "Agent",
    kind: "text",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "reasoning_effort",
    wireField: "reasoningEffort",
    label: "Reasoning effort",
    group: "Model",
    kind: "select",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
    choices: [
      { value: "", label: "(default)" },
      { value: "high", label: "high" },
    ],
  },
  {
    field: "trace_file",
    wireField: "traceFile",
    label: "Trace file",
    group: "Debug Logging",
    kind: "path",
    pathKind: "outputFile",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "skills_dirs",
    wireField: "skillsDirs",
    label: "Skill directories",
    group: "Resources",
    kind: "pathList",
    pathKind: "dir",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "model_fallbacks",
    wireField: "modelFallbacks",
    label: "Model fallbacks",
    group: "Resources",
    kind: "modelList",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "env",
    wireField: "env",
    label: "Environment variables",
    group: "Environment",
    kind: "envMap",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "mcps",
    wireField: "mcps",
    label: "MCP servers",
    group: "Resources",
    kind: "mcpServerList",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "system_prompt_mode",
    wireField: "systemPromptMode",
    label: "System prompt",
    group: "System Prompt",
    kind: "radio",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
    choices: [
      { value: "", label: "Serf default" },
      { value: "file", label: "Pick a file" },
      { value: "inline", label: "Fill in text" },
    ],
  },
  {
    field: "system_prompt_file",
    wireField: "systemPromptFile",
    label: "unused",
    group: "System Prompt",
    kind: "path",
    pathKind: "file",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  {
    field: "system_prompt_text",
    wireField: "systemPromptText",
    label: "unused",
    group: "System Prompt",
    kind: "multilineText",
    perLaunch: true,
    defaultableLayers: ["global", "project"],
  },
  // Excluded from BOTH pages by defaultableLayers: [] - proves layer
  // filtering happens inside the form, not just by the caller.
  {
    field: "verbose",
    wireField: "verbose",
    label: "Verbose",
    group: "Debug Logging",
    kind: "boolean",
    perLaunch: true,
    defaultableLayers: [],
  },
];

const OK_VALIDATE: (path: string, kind: string) => Promise<PathValidateResponse> = async (path) => ({
  path,
  valid: true,
});

const RESOLVED: LaunchConfigResolved = { effective: {}, layers: {}, provenance: {} };

// handleSubmit's own validate -> save chain is all plain promises (no
// timers), so under fake timers the only way to let it fully resolve
// before advancing the clock is draining microtask turns directly -
// waitFor/userEvent's own internal polling is timer-based and would hang
// once vi.useFakeTimers() is active. Mirrors threads.test.ts's own
// flushUntil helper, unconditional turn count since there's no polled
// condition here, only "let everything pending settle".
async function flushMicrotasks(turns = 20): Promise<void> {
  for (let i = 0; i < turns; i++) await Promise.resolve();
}

describe("rendering", () => {
  test("renders every layer-supported option, grouped, in schema order - and excludes an option with no layer support", () => {
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => RESOLVED}
      />,
    );
    // "Agent" is both this schema's group name and its one field's label -
    // both should render (the field via its own FormRow label, the group via
    // the section header), hence getAllByText rather than getByText here.
    expect(screen.getAllByText("Agent").length).toBe(2);
    expect(screen.getByText("Model")).toBeTruthy(); // group header
    expect(screen.getByLabelText("Reasoning effort")).toBeTruthy();
    expect(screen.queryByText("Verbose")).toBeNull(); // defaultableLayers: [] excludes it entirely
  });

  test("folds the 2 prompt-composite leaf fields out of the main loop into their own composite control", () => {
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => RESOLVED}
      />,
    );
    // "unused" labels never render bare - only the composite's own fixed labels do.
    expect(screen.queryByText("unused")).toBeNull();
    expect(screen.getByRole("radiogroup", { name: "System prompt" })).toBeTruthy();
    expect(screen.getByLabelText("System prompt from file")).toBeTruthy();
    expect(screen.getByLabelText("System prompt text")).toBeTruthy();
  });

  test("seeds every field from `current`", () => {
    const current: LaunchConfigLayer = { agent: "custom", skillsDirs: ["/opt/skills"], env: { FOO: "bar" } };
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={current}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => RESOLVED}
      />,
    );
    expect(screen.getByLabelText("Agent")).toHaveProperty("value", "custom");
    expect(screen.getByText("/opt/skills")).toBeTruthy();
    expect(screen.getByText("FOO=bar")).toBeTruthy();
  });

  test("shows a project-layer global-default hint next to a field when globalDefaults supplies one", () => {
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="project"
        current={{}}
        globalDefaults={{ agent: "serf" }}
        successToast="Project launch settings saved"
        validatePath={OK_VALIDATE}
        onSave={async () => RESOLVED}
      />,
    );
    expect(screen.getByText("default: serf")).toBeTruthy();
  });
});

describe("validation blocks save", () => {
  // The path row is a picker now, so the value gets there by picking a row -
  // and serf/path/validate still gates the save exactly as it did when the
  // same value was typed into a text box.
  test("an invalid path-kind scalar shows an inline error and does not call onSave", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", () => ({ data: ["/tmp/not-real.jsonl"] }));
    const validatePath = vi.fn().mockResolvedValue({ path: "", valid: false, error: "invalid path" });
    const onSave = vi.fn().mockResolvedValue(RESOLVED);
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={validatePath}
        onSave={onSave}
      />,
    );
    await user.click(screen.getByLabelText("Trace file"));
    // The panel portals to document.body, so its rows are queried from screen.
    await user.click(await screen.findByRole("option", { name: /not-real\.jsonl/ }));
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText("invalid path")).toBeTruthy());
    expect(validatePath).toHaveBeenCalledWith("/tmp/not-real.jsonl", "output-file");
    expect(onSave).not.toHaveBeenCalled();
  });

  test("a picked path-kind value reaches onSave once it validates", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", () => ({ data: ["/tmp/trace.jsonl"] }));
    const onSave = vi.fn().mockResolvedValue(RESOLVED);
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={onSave}
      />,
    );
    await user.click(screen.getByLabelText("Trace file"));
    await user.click(await screen.findByRole("option", { name: /trace\.jsonl/ }));
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ traceFile: "/tmp/trace.jsonl" })),
    );
  });
});

describe("successful save", () => {
  test("collects the form and calls onSave with it", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(RESOLVED);
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={onSave}
      />,
    );
    await user.type(screen.getByLabelText("Agent"), "custom-agent");
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ agent: "custom-agent" })));
  });

  test("shows a 'Saved at <time>' status and fires the success toast", async () => {
    const user = userEvent.setup();
    render(
      <>
        <LaunchConfigForm
          options={OPTIONS}
          layer="global"
          current={{}}
          successToast="Launch defaults saved"
          validatePath={OK_VALIDATE}
          onSave={async () => RESOLVED}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText(/^Saved at /)).toBeTruthy());
    // Toast is a module-singleton queue that outlives unmount between tests
    // (see widgets/rail/Rail.test.tsx's identical convention) - assert
    // presence, not exclusivity.
    expect(screen.getAllByText("Launch defaults saved").length).toBeGreaterThan(0);
  });

  test("calls onSaved with the resolved config the store returned", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const resolved: LaunchConfigResolved = {
      effective: {},
      layers: {},
      provenance: {},
      diagnostics: [{ layer: "global", field: "sandbox", message: "hint" }],
    };
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => resolved}
        onSaved={onSaved}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(resolved));
  });
});

describe("failed save", () => {
  test("status shows 'Error: {message}' and fires the failure toast", async () => {
    const user = userEvent.setup();
    render(
      <>
        <LaunchConfigForm
          options={OPTIONS}
          layer="global"
          current={{}}
          successToast="Launch defaults saved"
          validatePath={OK_VALIDATE}
          onSave={async () => {
            throw new Error("disk full");
          }}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText("Error: disk full")).toBeTruthy());
    expect(screen.getByText("Save failed")).toBeTruthy();
  });

  test("an env-credential-shaped backend error additionally attaches inline to the env field", async () => {
    const user = userEvent.setup();
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => {
          throw new Error('env key "FOO" looks like a credential; route through serf/auth/apiKey/set');
        }}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await waitFor(() => expect(screen.getByText(/looks like a credential/)).toBeTruthy());
  });
});

describe("status self-clear", () => {
  test("a success status self-clears after 5000ms", async () => {
    vi.useFakeTimers();
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => RESOLVED}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await act(() => flushMicrotasks());
    expect(screen.getByText(/^Saved at /)).toBeTruthy();

    act(() => {
      vi.advanceTimersByTime(5001);
    });
    expect(screen.queryByText(/^Saved at /)).toBeNull();
  });

  test("an error status does not self-clear", async () => {
    vi.useFakeTimers();
    render(
      <LaunchConfigForm
        options={OPTIONS}
        layer="global"
        current={{}}
        successToast="Launch defaults saved"
        validatePath={OK_VALIDATE}
        onSave={async () => {
          throw new Error("boom");
        }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Save launch defaults" }));
    await act(() => flushMicrotasks());
    expect(screen.getByText("Error: boom")).toBeTruthy();

    act(() => {
      vi.advanceTimersByTime(10000);
    });
    expect(screen.getByText("Error: boom")).toBeTruthy();
  });
});
