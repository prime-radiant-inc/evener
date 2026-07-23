import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { LaunchOption } from "../../../../protocol/types.gen";
import { fetchModelCatalog } from "../../../../widgets/modelCatalog/catalogClient";
import { PromptCompositeField, ScalarField } from "./fields";

// The modelPicker kind renders the rich ModelCatalog widget, which calls the
// REST /api/models loader on open; mock it so these render tests stay hermetic.
vi.mock("../../../../widgets/modelCatalog/catalogClient", () => ({ fetchModelCatalog: vi.fn() }));

beforeEach(() => {
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

describe("ScalarField: text/path kinds render as a plain labeled input", () => {
  test("text kind renders an Input with the option's label", async () => {
    const onChange = vi.fn();
    render(<ScalarField option={textOption()} layer="global" value="" onChange={onChange} />);
    const input = screen.getByLabelText("Agent");
    await userEvent.type(input, "x");
    expect(onChange).toHaveBeenCalledWith("x");
  });

  test("path kind renders as a free-text input (PathPicker only supports dir listing, not file/outputFile)", () => {
    render(
      <ScalarField
        option={textOption({
          field: "trace_file",
          wireField: "traceFile",
          label: "Trace file",
          kind: "path",
          pathKind: "outputFile",
        })}
        layer="global"
        value=""
        onChange={() => {}}
      />,
    );
    expect(screen.getByLabelText("Trace file")).toBeTruthy();
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

  test("a submit-time path-validation error replaces the help text (FormRow's own error-over-help contract)", () => {
    render(
      <ScalarField
        option={textOption({
          field: "trace_file",
          wireField: "traceFile",
          label: "Trace file",
          kind: "path",
          pathKind: "outputFile",
          description: "Write a trace here.",
        })}
        layer="global"
        value="/not/writable"
        onChange={() => {}}
        error="invalid path"
      />,
    );
    expect(screen.getByText("invalid path")).toBeTruthy();
    expect(screen.queryByText("Write a trace here.")).toBeNull();
  });
});

describe("ScalarField: modelPicker renders the rich model catalog", () => {
  test("shows the current qualified value and a Change model affordance, not a free-text input", () => {
    render(<ScalarField option={modelPickerOption()} layer="global" value="anthropic/claude" onChange={() => {}} />);
    expect(screen.getByText("Model")).toBeTruthy(); // the field label
    expect(screen.getByText("anthropic/claude")).toBeTruthy(); // current value chip
    expect(screen.getByRole("button", { name: "Change model" })).toBeTruthy();
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

    await user.click(screen.getByRole("button", { name: "Change model" }));
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

  test("typing into the file field calls onFileChange even when mode is 'inline' (inactive fields stay editable)", async () => {
    const onFileChange = vi.fn();
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
    await userEvent.type(screen.getByLabelText("System prompt from file"), "x");
    expect(onFileChange).toHaveBeenCalledWith("x");
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
