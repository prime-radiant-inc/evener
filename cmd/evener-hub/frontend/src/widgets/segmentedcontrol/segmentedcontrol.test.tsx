import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { SegmentedControl } from "./index";

afterEach(cleanup);

const OPTIONS = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
  { value: "activity", label: "Activity" },
  { value: "full", label: "Full", accessibleLabel: "Full detail" },
  { value: "custom", label: "Custom" },
] as const;

function Harness({
  initial = "tools",
  disabled = false,
  onChange,
}: {
  initial?: string;
  disabled?: boolean;
  onChange?(value: string): void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <SegmentedControl
      id="detail-level"
      aria-describedby="detail-help"
      label="Transcript detail"
      value={value}
      options={OPTIONS}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
      disabled={disabled}
      fullWidth
    />
  );
}

function NavigationHarness({ onChange }: { onChange?: (value: string) => void }) {
  const [value, setValue] = useState("one");
  const options = [
    { value: "one", label: "One" },
    { value: "two", label: "Two", disabled: true },
    { value: "three", label: "Three" },
  ];
  return (
    <SegmentedControl
      label="Choice"
      value={value}
      options={options}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

function cssSource() {
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(join(here, "segmentedcontrol.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

function tokenSource() {
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(join(here, "../../styles/tokens.css"), "utf8");
}

describe("validation and accessible structure", () => {
  test("renders two and six options", () => {
    render(
      <SegmentedControl
        label="Theme"
        value="dark"
        options={[
          { value: "dark", label: "Dark" },
          { value: "light", label: "Light" },
        ]}
        onChange={() => {}}
      />,
    );
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    cleanup();
    render(<Harness />);
    expect(screen.getAllByRole("radio")).toHaveLength(6);
  });

  test.each([
    ["one option", [{ value: "only", label: "Only" }], "only", "two through six options"],
    [
      "seven options",
      Array.from({ length: 7 }, (_, index) => ({ value: String(index), label: String(index) })),
      "0",
      "two through six options",
    ],
    [
      "duplicate values",
      [
        { value: "same", label: "One" },
        { value: "same", label: "Two" },
      ],
      "same",
      "values must be unique",
    ],
    [
      "unmatched value",
      [
        { value: "one", label: "One" },
        { value: "two", label: "Two" },
      ],
      "missing",
      "value must match exactly one option",
    ],
  ])("throws for %s", (_case, options, value, message) => {
    expect(() =>
      render(<SegmentedControl label="Choice" value={value} options={options} onChange={() => {}} />),
    ).toThrow(message);
  });

  test.each([
    [
      "empty group label",
      "   ",
      [
        { value: "one", label: "One" },
        { value: "two", label: "Two" },
      ],
      "non-empty group label",
    ],
    [
      "empty option label",
      "Choice",
      [
        { value: "one", label: "   " },
        { value: "two", label: "Two" },
      ],
      "visible labels",
    ],
    [
      "empty accessible label",
      "Choice",
      [
        { value: "one", label: "One", accessibleLabel: "   " },
        { value: "two", label: "Two" },
      ],
      "accessible labels must be non-empty",
    ],
  ])("throws for %s", (_case, label, options, message) => {
    expect(() => render(<SegmentedControl label={label} value="one" options={options} onChange={() => {}} />)).toThrow(
      message,
    );
  });

  test("uses defaults and forwards group naming attributes", () => {
    render(<Harness />);
    const group = screen.getByRole("radiogroup", { name: "Transcript detail" });
    const label = screen.getByText("Transcript detail");
    expect(group.id).toBe("detail-level");
    expect(group.getAttribute("aria-labelledby")).toBe(label.id);
    expect(group.getAttribute("aria-describedby")).toBe("detail-help");
    expect(group.getAttribute("aria-orientation")).toBe("horizontal");
    expect(group.className).toContain("fullWidth");
    expect(screen.getByRole("radio", { name: "Tools" }).getAttribute("tabindex")).toBe("0");
    expect(screen.getAllByRole("radio").filter((radio) => radio.getAttribute("aria-checked") === "true")).toHaveLength(
      1,
    );
  });

  test("defaults to md density and intrinsic width when optional props are omitted", () => {
    render(
      <SegmentedControl
        label="Theme"
        value="dark"
        options={[
          { value: "dark", label: "Dark" },
          { value: "light", label: "Light" },
        ]}
        onChange={() => {}}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "Theme" });
    expect(group.className).not.toContain("fullWidth");
    expect(group.style.inlineSize).toBe("");
    expect(screen.getByRole("radio", { name: "Dark" }).className).toContain("md");
  });

  test("generates a group id and labels Full with its complete accessible name", () => {
    render(<SegmentedControl label="Details" value="full" options={OPTIONS} onChange={() => {}} />);
    const group = screen.getByRole("radiogroup", { name: "Details" });
    const full = screen.getByRole("radio", { name: "Full detail" });
    expect(group.id).toBeTruthy();
    expect(group.getAttribute("aria-labelledby")).toBeTruthy();
    expect(full.textContent).toBe("Full");
    expect(screen.queryByRole("radio", { name: "Full" })).toBeNull();
    expect(full.getAttribute("title")).toBeNull();
  });
});

describe("selection and roving focus", () => {
  test("clicks options, activates natively with Enter/Space, and does not duplicate current selection", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    const tools = screen.getByRole("radio", { name: "Tools" });
    await user.click(tools);
    expect(onChange).not.toHaveBeenCalled();
    await user.click(screen.getByRole("radio", { name: "Activity" }));
    expect(onChange).toHaveBeenLastCalledWith("activity");
    const full = screen.getByRole("radio", { name: "Full detail" });
    full.focus();
    await user.keyboard(" ");
    expect(onChange).toHaveBeenNthCalledWith(2, "full");
    expect(full.getAttribute("aria-checked")).toBe("true");
    const activity = screen.getByRole("radio", { name: "Activity" });
    activity.focus();
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledTimes(3);
    expect(onChange).toHaveBeenNthCalledWith(3, "activity");
    await user.keyboard(" ");
    expect(onChange).toHaveBeenCalledTimes(3);
  });

  test("Right/Down/Left/Up wrap, skip disabled options, select, and focus once", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<NavigationHarness onChange={onChange} />);
    const one = screen.getByRole("radio", { name: "One" });
    const three = screen.getByRole("radio", { name: "Three" });
    one.focus();
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(three);
    expect(three.getAttribute("aria-checked")).toBe("true");
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement).toBe(one);
    await user.keyboard("{ArrowLeft}");
    expect(document.activeElement).toBe(three);
    await user.keyboard("{ArrowUp}");
    expect(document.activeElement).toBe(one);
    expect(onChange).toHaveBeenCalledTimes(4);
  });

  test("Home and End choose first and last enabled options", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="activity" onChange={onChange} />);
    const activity = screen.getByRole("radio", { name: "Activity" });
    activity.focus();
    await user.keyboard("{End}");
    const custom = screen.getByRole("radio", { name: "Custom" });
    expect(document.activeElement).toBe(custom);
    expect(custom.getAttribute("aria-checked")).toBe("true");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenLastCalledWith("custom");
    await user.keyboard("{Home}");
    const chat = screen.getByRole("radio", { name: "Chat" });
    expect(document.activeElement).toBe(chat);
    expect(chat.getAttribute("aria-checked")).toBe("true");
    expect(onChange).toHaveBeenCalledTimes(2);
    expect(onChange).toHaveBeenLastCalledWith("chat");
  });
});

describe("disabled behavior and form safety", () => {
  test("uses the first enabled tab stop when the selected option is disabled", () => {
    render(
      <SegmentedControl
        label="Choice"
        value="two"
        options={[
          { value: "one", label: "One" },
          { value: "two", label: "Two", disabled: true },
        ]}
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("radio", { name: "Two" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("radio", { name: "Two" }).getAttribute("tabindex")).toBe("-1");
    expect(screen.getByRole("radio", { name: "One" }).getAttribute("tabindex")).toBe("0");
  });

  test("group disablement is native, semantic, and has no tab stop", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness disabled onChange={onChange} />);
    const group = screen.getByRole("radiogroup");
    expect(group.getAttribute("aria-disabled")).toBe("true");
    for (const radio of screen.getAllByRole("radio")) {
      expect((radio as HTMLButtonElement).disabled).toBe(true);
      expect(radio.getAttribute("tabindex")).toBe("-1");
    }
    const option = screen.getByRole("radio", { name: "Tools" });
    await user.click(option);
    option.focus();
    await user.keyboard("{Enter}");
    await user.keyboard(" ");
    fireEvent.click(option);
    expect(onChange).not.toHaveBeenCalled();
  });

  test("all-disabled groups have no tab stop", () => {
    render(
      <SegmentedControl
        label="Choice"
        value="one"
        options={[
          { value: "one", label: "One", disabled: true },
          { value: "two", label: "Two", disabled: true },
        ]}
        onChange={() => {}}
      />,
    );
    expect(screen.getAllByRole("radio").every((radio) => radio.getAttribute("tabindex") === "-1")).toBe(true);
  });

  test("never submits the containing form", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      <form onSubmit={onSubmit}>
        <Harness />
      </form>,
    );
    const option = screen.getByRole("radio", { name: "Activity" });
    await user.click(option);
    option.focus();
    await user.keyboard("{Enter}");
    expect(onSubmit).not.toHaveBeenCalled();
  });
});

describe("CSS contract", () => {
  test("uses the complete neutral track, option, density, responsive, and motion contract", () => {
    const css = cssSource();
    expect(css).toMatch(
      /\.root\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*align-items:\s*flex-start[^}]*min-inline-size:\s*0[^}]*gap:\s*var\(--space-2\)/s,
    );
    expect(css).toMatch(
      /\.label\s*\{[^}]*color:\s*var\(--ink-hi\)[^}]*font-family:\s*var\(--font-sans\)[^}]*font-size:\s*var\(--font-size-ui\)[^}]*line-height:\s*var\(--line-height-body\)[^}]*font-weight:\s*var\(--font-weight-medium\)/s,
    );
    expect(css).toMatch(
      /\.track\s*\{[^}]*display:\s*inline-grid[^}]*align-items:\s*stretch[^}]*box-sizing:\s*border-box[^}]*overflow:\s*visible[^}]*padding:\s*2px[^}]*gap:\s*2px[^}]*border:\s*1px solid var\(--edge\)[^}]*border-radius:\s*var\(--radius-control\)[^}]*background:\s*var\(--field\)[^}]*box-shadow:\s*var\(--shadow-inset-field\)/s,
    );
    expect(css).toMatch(
      /\.option\s*\{[^}]*display:\s*flex[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*min-inline-size:\s*0[^}]*inline-size:\s*100%[^}]*border:\s*0[^}]*border-radius:\s*var\(--radius-control\)[^}]*padding-block:\s*0[^}]*padding-inline:\s*var\(--space-1\)[^}]*background:\s*transparent[^}]*color:\s*var\(--ink-mid\)/s,
    );
    expect(css).toMatch(/\.fullWidth\s*\{[^}]*inline-size:\s*100%/s);
    expect(css).toMatch(
      /\.option\s*\{[^}]*font-family:\s*var\(--font-sans\)[^}]*font-size:\s*var\(--font-size-ui\)[^}]*line-height:\s*var\(--line-height-body\)[^}]*font-weight:\s*var\(--font-weight-medium\)[^}]*text-align:\s*center[^}]*cursor:\s*pointer/s,
    );
    expect(css).toMatch(
      /\.option\s*\{[^}]*transition:\s*background-color var\(--motion-duration-hover\) var\(--motion-easing-standard\),\s*border-color var\(--motion-duration-hover\) var\(--motion-easing-standard\),\s*color var\(--motion-duration-hover\) var\(--motion-easing-standard\),\s*box-shadow var\(--motion-duration-hover\) var\(--motion-easing-standard\)/s,
    );
    expect(css).toMatch(
      /\.option:not\(\[aria-checked="true"\]\):hover:not\(:disabled\)\s*\{[^}]*background:\s*var\(--hover-1\)/s,
    );
    expect(css).toMatch(
      /\.option\[aria-checked="true"\]\s*\{[^}]*background:\s*var\(--hover-2\)[^}]*color:\s*var\(--ink-hi\)[^}]*font-weight:\s*var\(--font-weight-semibold\)/s,
    );
    expect(css).toMatch(/\.option:focus-visible\s*\{[^}]*outline:\s*var\(--focus-ring\)[^}]*outline-offset:\s*2px/s);
    expect(css).toMatch(/\.option:disabled\s*\{[^}]*cursor:\s*not-allowed[^}]*opacity:\s*0\.5/s);
    expect(css.match(/opacity:\s*0\.5/g) ?? []).toHaveLength(1);
    expect(css).not.toMatch(/\.root\s*\{[^}]*opacity\s*:/s);
    expect(css).not.toMatch(/\.track\s*\{[^}]*opacity\s*:/s);
    expect(css).toMatch(
      /\.optionLabel\s*\{[^}]*min-inline-size:\s*0[^}]*overflow:\s*hidden[^}]*text-overflow:\s*ellipsis[^}]*white-space:\s*nowrap/s,
    );
    expect(css).toMatch(/\.sm\s*\{[^}]*block-size:\s*28px/s);
    expect(css).toMatch(/\.md\s*\{[^}]*block-size:\s*32px/s);
    expect(css).toMatch(
      /@media\s*\(max-width:\s*899px\)[^{]*\{[\s\S]*?\.option\s*\{[^}]*min-block-size:\s*var\(--tap-min\)/s,
    );
    expect(css).toMatch(
      /@media\s*\(prefers-reduced-motion:\s*reduce\)[^{]*\{[\s\S]*?\.option\s*\{[^}]*transition:\s*none/s,
    );
    const smMatch = css.match(/\.sm\s*\{[^}]*block-size:\s*(\d+)px/s);
    const mdMatch = css.match(/\.md\s*\{[^}]*block-size:\s*(\d+)px/s);
    const tapMatch = tokenSource().match(/--tap-min:\s*(\d+)px/);
    expect(smMatch).toBeTruthy();
    expect(mdMatch).toBeTruthy();
    expect(tapMatch).toBeTruthy();
    const trackEdges = 2 * 2 + 2 * 1;
    expect(Number(smMatch?.[1]) + trackEdges).toBe(34);
    expect(Number(mdMatch?.[1]) + trackEdges).toBe(38);
    expect(Number(tapMatch?.[1]) + trackEdges).toBe(50);
    expect(css).not.toMatch(/min-inline-size:\s*var\(--tap-min\)/);
    expect(css).not.toMatch(/data-theme|--accent|flex-wrap|overflow-x\s*:\s*(auto|scroll)|margin-inline\s*:\s*-/);
    expect(css).not.toMatch(/transition:\s*all/);
  });
});
