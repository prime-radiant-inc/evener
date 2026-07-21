import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { RadioGroup, type RadioGroupOption } from "./index";

afterEach(cleanup);

const THEME_OPTIONS: RadioGroupOption[] = [
  { value: "system", label: "System" },
  { value: "dark", label: "Dark" },
  { value: "light", label: "Light" },
];

const OPTIONS_WITH_DISABLED: RadioGroupOption[] = [
  { value: "asks", label: "Questions & errors" },
  { value: "everything", label: "Everything", disabled: true },
  { value: "all", label: "Everything needing me" },
];

function ControlledRadioGroup(props: {
  options: RadioGroupOption[];
  initial: string;
  onChange?: (value: string) => void;
  disabled?: boolean;
}) {
  const [value, setValue] = useState(props.initial);
  return (
    <RadioGroup
      label="Theme"
      value={value}
      options={props.options}
      disabled={props.disabled}
      onChange={(next) => {
        setValue(next);
        props.onChange?.(next);
      }}
    />
  );
}

test("has role=radiogroup labelled by the visible legend text", () => {
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("radiogroup", { name: "Theme" })).toBeTruthy();
  expect(screen.getByText("Theme")).toBeTruthy();
});

test("renders one visible radio per option", () => {
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("radio", { name: "System" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Dark" })).toBeTruthy();
  expect(screen.getByRole("radio", { name: "Light" })).toBeTruthy();
});

test("aria-checked reflects the current value, exactly one option at a time", () => {
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("false");
  expect(screen.getByRole("radio", { name: "Dark" }).getAttribute("aria-checked")).toBe("true");
  expect(screen.getByRole("radio", { name: "Light" }).getAttribute("aria-checked")).toBe("false");
});

test("clicking an option calls onChange with its value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={onChange} />);
  await user.click(screen.getByRole("radio", { name: "Light" }));
  expect(onChange).toHaveBeenCalledWith("light");
});

test("only the checked option is a tab stop (roving tabindex)", () => {
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("tabindex")).toBe("-1");
  expect(screen.getByRole("radio", { name: "Dark" }).getAttribute("tabindex")).toBe("0");
  expect(screen.getByRole("radio", { name: "Light" }).getAttribute("tabindex")).toBe("-1");
});

test("when no option matches the current value, the first enabled option is the tab stop", () => {
  render(<RadioGroup label="Theme" value="not-a-real-option" options={THEME_OPTIONS} onChange={() => {}} />);
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("tabindex")).toBe("0");
});

test("ArrowRight moves focus to and selects the next option, wrapping past the end", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledRadioGroup options={THEME_OPTIONS} initial="light" onChange={onChange} />);
  screen.getByRole("radio", { name: "Light" }).focus();

  await user.keyboard("{ArrowRight}");

  expect(onChange).toHaveBeenCalledWith("system");
  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "System" }));
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");
});

test("ArrowLeft moves focus to and selects the previous option, wrapping before the start", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledRadioGroup options={THEME_OPTIONS} initial="system" onChange={onChange} />);
  screen.getByRole("radio", { name: "System" }).focus();

  await user.keyboard("{ArrowLeft}");

  expect(onChange).toHaveBeenCalledWith("light");
  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Light" }));
});

test("ArrowDown/ArrowUp behave the same as ArrowRight/ArrowLeft", async () => {
  const user = userEvent.setup();
  render(<ControlledRadioGroup options={THEME_OPTIONS} initial="system" />);
  screen.getByRole("radio", { name: "System" }).focus();

  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("radio", { name: "Dark" }).getAttribute("aria-checked")).toBe("true");

  await user.keyboard("{ArrowUp}");
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");
});

test("Home selects the first enabled option, End selects the last", async () => {
  const user = userEvent.setup();
  render(<ControlledRadioGroup options={THEME_OPTIONS} initial="dark" />);
  screen.getByRole("radio", { name: "Dark" }).focus();

  await user.keyboard("{End}");
  expect(screen.getByRole("radio", { name: "Light" }).getAttribute("aria-checked")).toBe("true");
  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Light" }));

  await user.keyboard("{Home}");
  expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");
  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "System" }));
});

test("a disabled option cannot be focused or clicked", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<RadioGroup label="Loud for" value="asks" options={OPTIONS_WITH_DISABLED} onChange={onChange} />);
  const disabledOption = screen.getByRole("radio", { name: "Everything" }) as HTMLButtonElement;

  expect(disabledOption.disabled).toBe(true);
  await user.click(disabledOption);
  expect(onChange).not.toHaveBeenCalled();
});

test("arrow stepping skips a disabled option in between", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledRadioGroup options={OPTIONS_WITH_DISABLED} initial="asks" onChange={onChange} />);
  screen.getByRole("radio", { name: "Questions & errors" }).focus();

  await user.keyboard("{ArrowRight}");

  expect(onChange).toHaveBeenCalledWith("all");
  expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Everything needing me" }));
});

test("disabled (whole group) blocks every option from being clicked", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<RadioGroup label="Theme" value="dark" options={THEME_OPTIONS} onChange={onChange} disabled />);
  const option = screen.getByRole("radio", { name: "System" }) as HTMLButtonElement;
  expect(option.disabled).toBe(true);
  await user.click(option);
  expect(onChange).not.toHaveBeenCalled();
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "radiogroup.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
