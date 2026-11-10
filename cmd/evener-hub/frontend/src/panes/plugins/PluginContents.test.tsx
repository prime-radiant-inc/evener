import type { PluginComponents } from "@evener/appwire-client";
import type { PluginComponentsEntry } from "@evener/appwire-client/state/extensions";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { expect, test } from "vitest";
import { Sheet } from "../../widgets";
import { PluginContents, PluginContentsBlock } from "./PluginContents";
import { PluginDetail } from "./PluginDetail";

const NINE_SERVERS: string[] = Array.from({ length: 9 }, (_, i) => `server-0${i + 1}`);

/** The one grid row for `kind`, by its structural identity - never by its
 * label copy. */
function rowFor(kind: string): HTMLElement {
  const row = document.querySelector(`[data-kind="${kind}"]`);
  if (!(row instanceof HTMLElement)) throw new Error(`no contents row for ${kind}`);
  return row;
}

/** The row's names segment: the joined mono names, as one text node. */
function namesOf(row: HTMLElement): string {
  const names = row.children[2];
  return names?.textContent ?? "";
}

test("a kind with more than eight names shows the first eight and expands the rest", async () => {
  const user = userEvent.setup();
  render(<PluginContents components={{ mcpServers: NINE_SERVERS }} />);
  expect(document.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  // Only the present kind renders a row.
  expect(document.querySelectorAll('[data-kind="plugin-contents"] > div > [data-kind]')).toHaveLength(1);

  const row = rowFor("mcpServers");
  // The badge carries the whole count, not the truncated one.
  expect(screen.getByText("9")).toBeTruthy();
  const visible = namesOf(row);
  for (const name of NINE_SERVERS.slice(0, 8)) expect(visible).toContain(name);
  expect(visible).not.toContain(NINE_SERVERS[8]);

  await user.click(screen.getByRole("button", { name: "+1 more" }));
  const expanded = namesOf(row);
  for (const name of NINE_SERVERS) expect(expanded).toContain(name);
  // The expansion is one-way: the affordance goes away with the truncation.
  expect(screen.queryByRole("button", { name: "+1 more" })).toBeNull();
});

test("keyboard expansion keeps focus inside the sheet and Escape dismisses it", async () => {
  const user = userEvent.setup();
  function Harness() {
    const [open, setOpen] = useState(true);
    return (
      <Sheet open={open} onClose={() => setOpen(false)} title="plugin-focus-sentinel" side="right">
        <PluginDetail
          mode="launcher"
          metadata={[]}
          contents={<PluginContents components={{ mcpServers: NINE_SERVERS }} />}
        />
      </Sheet>
    );
  }
  render(<Harness />);
  const sheet = screen.getByRole("dialog");
  const expander = within(rowFor("mcpServers")).getByRole("button");
  expect(document.activeElement).toBe(expander);
  await user.keyboard("{Enter}");
  expect(expander.isConnected).toBe(false);
  for (const name of NINE_SERVERS) expect(namesOf(rowFor("mcpServers"))).toContain(name);
  expect(sheet.contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
});

test.each([undefined, { status: "loading" as const }])(
  "the shared block rehomes keyboard Retry without a sheet owner rerender: %j",
  async (loading) => {
    const user = userEvent.setup();
    function Contents() {
      const [entry, setEntry] = useState<PluginComponentsEntry | undefined>({
        status: "error",
        source: "request",
        error: "retry-error-sentinel",
      });
      return <PluginContentsBlock entry={entry} onRetry={() => setEntry(loading)} />;
    }
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Sheet open={open} onClose={() => setOpen(false)} title="retry-focus-sentinel" side="right">
          <PluginDetail mode="launcher" metadata={[]} contents={<Contents />} />
        </Sheet>
      );
    }
    render(<Harness />);
    const sheet = screen.getByRole("dialog");
    const retry = within(within(sheet).getByRole("status")).getByRole("button");
    for (let i = 0; i < 5 && document.activeElement !== retry; i++) await user.tab();
    expect(document.activeElement).toBe(retry);
    await user.keyboard("{Enter}");
    expect(retry.isConnected).toBe(false);
    expect(sheet.contains(document.activeElement)).toBe(true);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  },
);

test("a manifest-only object reads as the empty case, never as an error or empty-styled list", () => {
  const components: PluginComponents = {};
  render(<PluginContents components={components} />);
  expect(document.querySelector('[data-kind="plugin-contents"]')).toBeTruthy();
  expect(document.querySelector('[data-reason="manifest-only"]')).toBeTruthy();
  expect(document.querySelectorAll('[data-kind="plugin-contents"] > div > [data-kind]')).toHaveLength(0);
});

test("a hooks list renders one event-with-count name per declared event", () => {
  render(
    <PluginContents
      components={{
        hooks: [
          { event: "PreToolUse", count: 3 },
          { event: "PostToolUse", count: 2 },
        ],
      }}
    />,
  );
  const row = rowFor("hooks");
  // The badge counts registered hooks, not events.
  expect(screen.getByText("5")).toBeTruthy();
  const names = namesOf(row);
  expect(names).toContain("PreToolUse (3)");
  expect(names).toContain("PostToolUse (2)");
});

test.each([{}, { skills: ["retained-skill"] }])(
  "load warnings render even with an empty inventory: %j",
  (inventory) => {
    const warnings = ["warning-sentinel-1", "warning-sentinel-2"];
    render(<PluginContents components={{ ...inventory, warnings }} />);
    const notice = screen.getByRole("alert");
    for (const warning of warnings) expect(notice.textContent).toContain(warning);
    expect(document.querySelector('[data-kind="skills"]') !== null).toBe("skills" in inventory);
  },
);

test("each present kind renders its own row with its count", () => {
  render(<PluginContents components={{ skills: ["run"], commands: ["fmt", "lint"] }} />);
  const skills = rowFor("skills");
  expect(namesOf(skills)).toContain("run");
  const commands = rowFor("commands");
  expect(namesOf(commands)).toContain("fmt");
  expect(namesOf(commands)).toContain("lint");
  expect(screen.getByText("2")).toBeTruthy();
});
