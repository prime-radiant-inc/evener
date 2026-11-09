import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { ListRow, type ListRowProps } from "./index";

function css(): string {
  // jsdom applies no stylesheet, so the declarations are the checkable truth
  // - comments stripped first so a doc comment cannot satisfy a match.
  return readFileSync(join(dirname(fileURLToPath(import.meta.url)), "listrow.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
}

function rule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|\\n|\\}|,)\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(css())?.[1] ?? "";
}

test("with onSelect the whole row is one button that runs it", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(<ListRow label="superpowers" meta="@ obra · v1.2.0" onSelect={onSelect} />);
  const row = screen.getByRole("button", { name: /superpowers/ });
  expect(row.getAttribute("type")).toBe("button");
  expect(row.textContent).toContain("@ obra · v1.2.0");
  await user.click(row);
  expect(onSelect).toHaveBeenCalledOnce();
});

test("without onSelect the row is read-only: its facts show and nothing is a control", () => {
  render(<ListRow label="filesystem — stdio" chips={<span>available</span>} meta="spawned from config" />);
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.getByText("filesystem — stdio")).toBeTruthy();
  expect(screen.getByText("available")).toBeTruthy();
  expect(screen.getByText("spawned from config")).toBeTruthy();
});

test("a read-only row carries its one trailing action as the only control", async () => {
  const user = userEvent.setup();
  const onSignIn = vi.fn();
  render(
    <ListRow
      label="codex-work"
      action={
        <button type="button" onClick={onSignIn}>
          Sign in on host
        </button>
      }
    />,
  );
  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(1);
  await user.click(screen.getByRole("button", { name: "Sign in on host" }));
  expect(onSignIn).toHaveBeenCalledOnce();
});

test("an interactive row cannot also carry a trailing action", () => {
  // A control nested in the row's own button is invalid HTML and two targets
  // in one; the typecheck is the test.
  // @ts-expect-error action is for read-only rows
  const props: ListRowProps = { label: "x", onSelect: () => {}, action: <span /> };
  expect(props.label).toBe("x");
});

test("disabled blocks onSelect and marks the native button inert", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(<ListRow label="Pinned" onSelect={onSelect} disabled />);
  const row = screen.getByRole("button", { name: "Pinned" }) as HTMLButtonElement;
  expect(row.disabled).toBe(true);
  await user.click(row);
  expect(onSelect).not.toHaveBeenCalled();
});

test("forwards its ref to the row's button", () => {
  const ref = createRef<HTMLElement>();
  render(<ListRow ref={ref} label="Pinned" onSelect={() => {}} />);
  expect(ref.current).toBe(screen.getByRole("button", { name: "Pinned" }));
});

test("spreads native attributes onto the row's button, but onSelect stays the click", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(
    <ListRow
      label="Working directory"
      onSelect={onSelect}
      aria-haspopup="dialog"
      aria-expanded={false}
      data-activity-anchor="job:7"
      title="~/git/evener"
    />,
  );
  const row = screen.getByRole("button", { name: /Working directory/ });
  expect(row.getAttribute("aria-haspopup")).toBe("dialog");
  expect(row.getAttribute("aria-expanded")).toBe("false");
  expect(row.getAttribute("data-activity-anchor")).toBe("job:7");
  expect(row.getAttribute("title")).toBe("~/git/evener");
  await user.click(row);
  expect(onSelect).toHaveBeenCalledOnce();
});

test("spreads native attributes onto a read-only row too", () => {
  render(<ListRow label="reviewer" data-activity-anchor="delegate:3" />);
  expect(screen.getByText("reviewer").closest("[data-activity-anchor]")?.getAttribute("data-activity-anchor")).toBe(
    "delegate:3",
  );
});

test("the leading slot renders before the label", () => {
  render(<ListRow label="anthropic" leading={<span>dot</span>} onSelect={() => {}} />);
  expect(screen.getByRole("button").textContent?.startsWith("dotanthropic")).toBe(true);
});

test("an interactive collection row ends in a decorative drill-in chevron; a plain row does not", () => {
  const { rerender } = render(<ListRow label="superpowers" onSelect={() => {}} />);
  const chevron = () => screen.getByRole("button").querySelector('[aria-hidden="true"] svg');
  expect(chevron()).not.toBeNull();
  expect(screen.getByRole("button", { name: "superpowers" })).toBeTruthy();
  rerender(<ListRow label="superpowers" variant="plain" onSelect={() => {}} />);
  expect(chevron()).toBeNull();
});

test("with trailingAction the row splits: the root is a container, the main area is its button", () => {
  render(
    <ListRow
      label="superpowers"
      meta="@ obra · v1.2.0"
      onSelect={() => {}}
      trailingAction={<button type="button">Install</button>}
    />,
  );
  const main = screen.getByRole("button", { name: /superpowers/ });
  const control = screen.getByRole("button", { name: "Install" });
  // No nested buttons, and the row root is not itself one: each control's
  // closest button is itself.
  expect(main.closest("button")).toBe(main);
  expect(control.closest("button")).toBe(control);
  expect(main.parentElement?.tagName).toBe("DIV");
  // The main area carries the facts; the drill-in chevron rides the
  // trailing slot, after the control.
  expect(main.textContent).toContain("@ obra · v1.2.0");
  expect(main.querySelector('[aria-hidden="true"] svg')).toBeNull();
  const slot = control.parentElement;
  expect(slot?.contains(main)).toBe(false);
  expect(slot?.children[0]).toBe(control);
  const chevron = slot?.children[1];
  expect(chevron?.getAttribute("aria-hidden")).toBe("true");
  expect(chevron?.querySelector("svg")).not.toBeNull();
});

test("with trailingAction the main area runs onSelect; the control's click is not a select", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  const onInstall = vi.fn();
  render(
    <ListRow
      label="superpowers"
      meta="@ obra · v1.2.0"
      onSelect={onSelect}
      trailingAction={
        <button type="button" onClick={onInstall}>
          Install
        </button>
      }
    />,
  );
  await user.click(screen.getByRole("button", { name: /superpowers/ }));
  expect(onSelect).toHaveBeenCalledOnce();
  await user.click(screen.getByRole("button", { name: "Install" }));
  expect(onInstall).toHaveBeenCalledOnce();
  expect(onSelect).toHaveBeenCalledOnce();
});

test("without trailingAction an interactive row is still one whole-row button", () => {
  render(<ListRow label="superpowers" meta="@ obra · v1.2.0" onSelect={() => {}} />);
  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(1);
  const row = buttons[0] as HTMLElement;
  expect(row.tagName).toBe("BUTTON");
  expect(row.textContent).toContain("@ obra · v1.2.0");
  // Today's shape: the drill-in chevron lives inside the row button.
  expect(row.querySelector('[aria-hidden="true"] svg')).not.toBeNull();
});

test("keyboard: tab reaches the main button, then the control; Enter on the main runs onSelect", async () => {
  const user = userEvent.setup();
  const onSelect = vi.fn();
  render(<ListRow label="superpowers" onSelect={onSelect} trailingAction={<button type="button">Install</button>} />);
  const main = screen.getByRole("button", { name: /superpowers/ });
  const control = screen.getByRole("button", { name: "Install" });
  await user.tab();
  expect(document.activeElement).toBe(main);
  await user.tab();
  expect(document.activeElement).toBe(control);
  await user.tab({ shift: true });
  expect(document.activeElement).toBe(main);
  await user.keyboard("{Enter}");
  expect(onSelect).toHaveBeenCalledOnce();
});

test("the collection row is a framed surface; the plain row is not", () => {
  const collection = rule(".collection");
  expect(collection).toContain("border: 1px solid var(--edge)");
  expect(collection).toContain("background: var(--surface-1)");
  const plain = rule(".plain");
  expect(plain).toContain("background: transparent");
  expect(plain).not.toMatch(/var\(--edge/);
});

test("an interactive row washes on hover and rings inset on keyboard focus", () => {
  expect(rule('.interactive:hover:not(:disabled):not([aria-disabled="true"])')).toContain("background: var(--hover-1)");
  const focus = rule(".interactive:focus-visible");
  expect(focus).toContain("outline: var(--focus-ring)");
  expect(focus).toContain("outline-offset: -2px");
});

test("the split shape's CSS: the container washes on focus-within, the main button is chromeless", () => {
  expect(rule('.split:focus-within:not([aria-disabled="true"])')).toContain("background: var(--hover-1)");
  const main = rule(".splitMain");
  expect(main).toContain("padding: 0");
  expect(main).toContain("border: 0");
  expect(main).toContain("background: transparent");
  expect(rule(".splitTrailing")).toContain("gap: var(--space-2)");
});

test("the collection meta line is mono and truncates", () => {
  expect(rule(".collection .meta")).toContain("font-family: var(--font-mono)");
  const meta = rule(".meta");
  expect(meta).toContain("text-overflow: ellipsis");
  expect(meta).toContain("white-space: nowrap");
});

test("mobile: interactive and collection rows reach the 44px tap floor", () => {
  const mobile = /@media \(max-width: 899px\), \(any-pointer: coarse\) \{([\s\S]*?)\n\}/.exec(css())?.[1] ?? "";
  expect(mobile).toMatch(/\.collection,\s*\.interactive\s*\{[^}]*min-height: var\(--tap-min\)/);
});

test("an aria-disabled interactive row refuses activation but keeps focus", async () => {
  const onSelect = vi.fn();
  render(<ListRow label="Host one" onSelect={onSelect} aria-disabled="true" />);
  const row = screen.getByRole("button", { name: /Host one/ });
  const user = userEvent.setup();
  await user.click(row);
  await user.tab();
  await user.keyboard("{Enter}");
  expect(onSelect).not.toHaveBeenCalled();
  expect(row).toHaveProperty("disabled", false);
});

test("an aria-disabled interactive row is styled like a disabled one", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "listrow.module.css"), "utf8");
  const disabledRules = [...css.matchAll(/([^{}]*:disabled[^{}]*)\{/g)].map((m) => m[1] ?? "");
  expect(disabledRules.length).toBeGreaterThan(0);
  for (const selector of disabledRules) expect(selector).toContain('[aria-disabled="true"]');
});
