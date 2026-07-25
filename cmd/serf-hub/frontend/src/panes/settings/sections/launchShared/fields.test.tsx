import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { LaunchOption } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";
import { PromptCompositeField, ScalarField } from "./fields";

// The modelPicker kind renders the rich ModelCatalog widget, which calls the
// REST /api/models loader on open; mock it so these render tests stay hermetic.
vi.mock("../../../../widgets/modelCatalog/catalogClient", () => ({ fetchModelCatalog: vi.fn() }));

// The path kinds render PathField, whose completion loader is the extensions
// store's own completePaths - so a test that OPENS a picker panel needs a
// connected client behind it (the same convention dirListSetting.test.tsx and
// MarketplacesSection.test.tsx use).
function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  vi.mocked(fetchModelCatalog).mockReset();
  vi.mocked(fetchModelCatalog).mockResolvedValue({ models: [], recent: [], diagnostics: [] });
});
afterEach(cleanup);

function modelPickerOption(overrides: Partial<LaunchOption> = {}): LaunchOption {
  return {
    field: "model",
    wireField: "model",
    label: "Model",
    group: "Model",
    kind: "modelPicker",
    perLaunch: true,
    ...overrides,
  };
}

function textOption(overrides: Partial<LaunchOption> = {}): LaunchOption {
  return {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "Agent",
    kind: "text",
    perLaunch: true,
    ...overrides,
  };
}

function pathOption(overrides: Partial<LaunchOption> = {}): LaunchOption {
  return textOption({
    field: "trace_file",
    wireField: "traceFile",
    label: "Trace file",
    kind: "path",
    pathKind: "outputFile",
    ...overrides,
  });
}

describe("ScalarField: text kind renders as a plain labeled input", () => {
  test("text kind renders an Input with the option's label", async () => {
    const onChange = vi.fn();
    render(<ScalarField option={textOption()} layer="global" value="" onChange={onChange} />);
    const input = screen.getByLabelText("Agent");
    await userEvent.type(input, "x");
    expect(onChange).toHaveBeenCalledWith("x");
  });

  test("option.description renders as help text", () => {
    render(
      <ScalarField
        option={textOption({ description: "Name of the agent binary." })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("Name of the agent binary.")).toBeTruthy();
  });
});

describe("ScalarField: a browsable path kind renders the path picker", () => {
  test("renders the picker trigger showing the current path, not a free-text input", () => {
    render(<ScalarField option={pathOption()} layer="global" value="/tmp/trace.jsonl" onChange={() => {}} />);
    const trigger = screen.getByLabelText("Trace file");
    expect(trigger.tagName).toBe("BUTTON");
    expect(trigger.textContent).toMatch(/\/tmp\/trace\.jsonl/);
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  test("an empty value shows the layer's own default marker", () => {
    render(<ScalarField option={pathOption()} layer="project" value="" onChange={() => {}} />);
    expect(screen.getByText("(use global default)")).toBeTruthy();
  });

  test("a file-kind field browses the value's own directory with files included", async () => {
    const user = userEvent.setup();
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "/etc/", includeFiles: true });
      return { data: ["/etc/ssl/", "/etc/prompt.md"] };
    });
    render(
      <ScalarField
        option={pathOption({
          field: "system_prompt_file",
          wireField: "systemPromptFile",
          label: "System prompt file",
          pathKind: "file",
        })}
        layer="global"
        value="/etc/prompt.md"
        onChange={() => {}}
      />,
    );

    await user.click(screen.getByLabelText("System prompt file"));
    // The panel portals to document.body, so it is queried from screen.
    expect(await screen.findByRole("option", { name: /prompt\.md/ })).toBeTruthy();
  });

  // outputFile is the pathKind of 3 of the 5 scalar path fields (traceFile,
  // cpuProfile, exportATIFPath), and the kind is exactly what decides
  // includeFiles: mapped to a directory kind, none of those three could ever
  // point at an existing file to overwrite.
  test("an outputFile field lists files, so an existing target can be picked", async () => {
    const user = userEvent.setup();
    const calls: Array<{ prefix: string; includeFiles?: boolean }> = [];
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", (params) => {
      calls.push(params);
      return { data: ["/tmp/traces/", "/tmp/trace.jsonl"] };
    });
    render(<ScalarField option={pathOption()} layer="global" value="/tmp/trace.jsonl" onChange={() => {}} />);

    await user.click(screen.getByLabelText("Trace file"));
    expect(await screen.findByRole("option", { name: /trace\.jsonl/ })).toBeTruthy();
    expect(calls).toEqual([{ prefix: "/tmp/", includeFiles: true }]);
  });

  test("picking a file row reports it via onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", () => ({ data: ["/tmp/atif.json"] }));
    render(<ScalarField option={pathOption()} layer="global" value="/tmp/old.json" onChange={onChange} />);

    await user.click(screen.getByLabelText("Trace file"));
    await user.click(await screen.findByRole("option", { name: /atif\.json/ }));

    expect(onChange).toHaveBeenCalledWith("/tmp/atif.json");
  });

  test("a command-kind path option stays a plain input - a command is not a browsable path", () => {
    render(
      <ScalarField
        option={pathOption({
          field: "agent_command",
          wireField: "agentCommand",
          label: "Agent command",
          pathKind: "command",
        })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText("Agent command").tagName).toBe("INPUT");
  });

  test("an unkinded path option stays a plain input - there is nothing to browse for", () => {
    render(
      <ScalarField
        option={pathOption({ field: "future", wireField: "future", label: "Future path", pathKind: undefined })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText("Future path").tagName).toBe("INPUT");
  });

  test("a submit-time path-validation error replaces the help text (FormRow's own error-over-help contract)", () => {
    render(
      <ScalarField
        option={pathOption({ description: "Write a trace here." })}
        layer="global"
        value="/not/writable"
        onChange={() => {}}
        error="invalid path"
      />,
    );
    expect(screen.getByText("invalid path")).toBeTruthy();
    expect(screen.queryByText("Write a trace here.")).toBeNull();
  });

  test("the project-layer global-default hint renders beside the picker", () => {
    render(
      <ScalarField
        option={pathOption()}
        layer="project"
        value=""
        onChange={() => {}}
        globalDefaultHint="default: /tmp/trace.jsonl"
      />,
    );
    expect(screen.getByText("default: /tmp/trace.jsonl")).toBeTruthy();
  });
});

describe("ScalarField: modelPicker renders the rich model catalog", () => {
  test("shows the current qualified value and a Change model affordance, not a free-text input", () => {
    render(<ScalarField option={modelPickerOption()} layer="global" value="anthropic/claude" onChange={() => {}} />);
    expect(screen.getByText("Model")).toBeTruthy(); // the field label
    expect(screen.getByText("anthropic/claude")).toBeTruthy(); // current value chip
    expect(screen.getByRole("button", { name: /change model/i })).toBeTruthy();
    expect(screen.queryByRole("textbox")).toBeNull(); // no free-text input anymore
  });

  test("an empty value shows the default marker", () => {
    render(<ScalarField option={modelPickerOption()} layer="global" value="" onChange={() => {}} />);
    expect(screen.getByText("(default)")).toBeTruthy();
  });

  test("picking a model from the catalog reports its qualified id via onChange", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    vi.mocked(fetchModelCatalog).mockResolvedValue({
      models: [{ provider: "openai", model: "gpt-5", displayName: "GPT-5" }],
      recent: [],
      diagnostics: [],
    });
    render(<ScalarField option={modelPickerOption()} layer="global" value="" onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: /change model/i }));
    const combo = await screen.findByRole("combobox", { name: "Model" });
    await user.type(combo, "gpt");
    await user.click(await screen.findByText("GPT-5"));

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  });

  test("renders the field description as help text", () => {
    render(
      <ScalarField
        option={modelPickerOption({ description: "The model to launch with." })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("The model to launch with.")).toBeTruthy();
  });
});

describe("ScalarField: multilineText renders a Textarea", () => {
  test("renders a textbox that accepts multi-line input", async () => {
    const onChange = vi.fn();
    render(
      <ScalarField
        option={textOption({
          field: "system_prompt_text",
          wireField: "systemPromptText",
          label: "System prompt text",
          kind: "multilineText",
        })}
        layer="global"
        value=""
        onChange={onChange}
      />,
    );
    const textarea = screen.getByLabelText("System prompt text");
    expect(textarea.tagName).toBe("TEXTAREA");
  });
});

describe("ScalarField: integer renders a numeric Input", () => {
  test("type=number", () => {
    render(
      <ScalarField
        option={textOption({ field: "max_rounds", wireField: "maxRounds", label: "Max rounds", kind: "integer" })}
        layer="global"
        value="10"
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText("Max rounds").getAttribute("type")).toBe("number");
  });
});

describe("ScalarField: select dedups the schema's own empty choice against the generic placeholder", () => {
  const reasoningOption = textOption({
    field: "reasoning_effort",
    wireField: "reasoningEffort",
    label: "Reasoning effort",
    kind: "select",
    choices: [
      { value: "", label: "(default)" },
      { value: "high", label: "high" },
      { value: "max", label: "max" },
    ],
  });

  test("renders exactly one empty option, using the schema's own label, plus every non-empty choice", () => {
    render(<ScalarField option={reasoningOption} layer="global" value="" onChange={() => {}} />);
    const select = screen.getByLabelText("Reasoning effort") as HTMLSelectElement;
    const optionLabels = Array.from(select.options).map((o) => o.textContent);
    expect(optionLabels).toEqual(["(default)", "high", "max"]);
  });

  test("project layer uses '(use global default)' as the fallback label only when the schema doesn't supply its own", () => {
    const noOwnEmpty = textOption({
      field: "future",
      wireField: "future",
      label: "Future field",
      kind: "select",
      choices: [{ value: "x", label: "X" }],
    });
    render(<ScalarField option={noOwnEmpty} layer="project" value="" onChange={() => {}} />);
    const select = screen.getByLabelText("Future field") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual(["(use global default)", "X"]);
  });

  test("selecting a choice calls onChange with its value", async () => {
    const onChange = vi.fn();
    render(<ScalarField option={reasoningOption} layer="global" value="" onChange={onChange} />);
    await userEvent.selectOptions(screen.getByLabelText("Reasoning effort"), "high");
    expect(onChange).toHaveBeenCalledWith("high");
  });
});

describe("ScalarField: boolean renders a literal true/false 3-state select", () => {
  test("option labels are the literal strings 'true'/'false', not On/Off", () => {
    render(
      <ScalarField
        option={textOption({
          field: "sandbox_net",
          wireField: "sandboxNet",
          label: "Sandbox network egress",
          kind: "boolean",
        })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    const select = screen.getByLabelText("Sandbox network egress") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual(["(default)", "true", "false"]);
  });
});

describe("ScalarField: radio renders RadioGroup bare (no FormRow double-labeling)", () => {
  test("renders a radiogroup with the schema's own choices plus the dedup'd empty entry", () => {
    render(
      <ScalarField
        option={textOption({
          field: "sandbox",
          wireField: "sandbox",
          label: "Sandbox",
          kind: "radio",
          choices: [
            { value: "", label: "(inherit)" },
            { value: "off", label: "off" },
            { value: "workspace-write", label: "workspace-write" },
          ],
        })}
        layer="global"
        value="off"
        onChange={() => {}}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "Sandbox" });
    expect(group).toBeTruthy();
    const options = screen.getAllByRole("radio");
    expect(options.map((o) => o.textContent)).toEqual(["(inherit)", "off", "workspace-write"]);
    expect(screen.getByRole("radio", { name: "off" }).getAttribute("aria-checked")).toBe("true");
  });
});

describe("ScalarField: global default hint (project layer only)", () => {
  test("renders the precomputed hint text next to the control when given", () => {
    render(
      <ScalarField
        option={textOption()}
        layer="project"
        value=""
        onChange={() => {}}
        globalDefaultHint="default: serf"
      />,
    );
    expect(screen.getByText("default: serf")).toBeTruthy();
  });

  test("renders nothing extra when no hint is given", () => {
    render(<ScalarField option={textOption()} layer="global" value="" onChange={() => {}} />);
    expect(screen.queryByText(/default:/)).toBeNull();
  });
});

describe("PromptCompositeField", () => {
  const modeOption = textOption({
    field: "system_prompt_mode",
    wireField: "systemPromptMode",
    label: "System prompt",
    kind: "radio",
    choices: [
      { value: "", label: "Serf default" },
      { value: "file", label: "Pick a file" },
      { value: "inline", label: "Fill in text" },
    ],
  });

  test("renders the mode radio group plus both the file and text sub-fields, always, regardless of active mode", () => {
    render(
      <PromptCompositeField
        option={modeOption}
        layer="global"
        modeValue=""
        fileValue=""
        textValue=""
        onModeChange={() => {}}
        onFileChange={() => {}}
        onTextChange={() => {}}
      />,
    );
    expect(screen.getByRole("radiogroup", { name: "System prompt" })).toBeTruthy();
    expect(screen.getByLabelText("System prompt from file")).toBeTruthy();
    expect(screen.getByLabelText("System prompt text")).toBeTruthy();
  });

  test("the file sub-field is the same path picker the scalar path kind renders, not a text box", () => {
    render(
      <PromptCompositeField
        option={modeOption}
        layer="global"
        modeValue=""
        fileValue="/etc/prompt.md"
        textValue=""
        onModeChange={() => {}}
        onFileChange={() => {}}
        onTextChange={() => {}}
      />,
    );
    const trigger = screen.getByLabelText("System prompt from file");
    expect(trigger.tagName).toBe("BUTTON");
    expect(trigger.textContent).toMatch(/\/etc\/prompt\.md/);
  });

  test("picking a file reports it via onFileChange even when mode is 'inline' (inactive fields stay editable)", async () => {
    const user = userEvent.setup();
    const onFileChange = vi.fn();
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", () => ({ data: ["/etc/prompt.md"] }));
    render(
      <PromptCompositeField
        option={modeOption}
        layer="global"
        modeValue="inline"
        fileValue=""
        textValue=""
        onModeChange={() => {}}
        onFileChange={onFileChange}
        onTextChange={() => {}}
      />,
    );
    await user.click(screen.getByLabelText("System prompt from file"));
    await user.click(await screen.findByRole("option", { name: /prompt\.md/ }));
    expect(onFileChange).toHaveBeenCalledWith("/etc/prompt.md");
  });

  test("fileError shows inline on the nested file sub-field", () => {
    render(
      <PromptCompositeField
        option={modeOption}
        layer="global"
        modeValue="file"
        fileValue="/bad/path"
        textValue=""
        onModeChange={() => {}}
        onFileChange={() => {}}
        onTextChange={() => {}}
        fileError="invalid path"
      />,
    );
    expect(screen.getByText("invalid path")).toBeTruthy();
  });

  test("choosing 'file' calls onModeChange with 'file'", async () => {
    const onModeChange = vi.fn();
    render(
      <PromptCompositeField
        option={modeOption}
        layer="global"
        modeValue=""
        fileValue=""
        textValue=""
        onModeChange={onModeChange}
        onFileChange={() => {}}
        onTextChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByRole("radio", { name: "Pick a file" }));
    expect(onModeChange).toHaveBeenCalledWith("file");
  });
});
