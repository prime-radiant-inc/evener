import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
import { ListRow, type ListRowProps } from "./index";

function css(): string {
  // jsdom applies no stylesheet, so the declarations are the checkable truth
  // - comments stripped first so a doc comment cannot satisfy a match.
  return readModuleCss(import.meta.url, "listrow.module.css");
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
  const css = readModuleCss(import.meta.url, "listrow.module.css");
  const disabledRules = [...css.matchAll(/([^{}]*:disabled[^{}]*)\{/g)].map((m) => m[1] ?? "");
  expect(disabledRules.length).toBeGreaterThan(0);
  for (const selector of disabledRules) expect(selector).toContain('[aria-disabled="true"]');
});
