import type { MarketplaceCatalogPlugin, PluginComponents, PluginEntry } from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { chipKinds } from "../settings/sections/marketplacesPlugins/marketplacesTestUtils";
import { CatalogPluginRow, InstalledPluginRow, LauncherPluginRow } from "./PluginRow";

const INSTALLED: PluginEntry = {
  plugin: "superpowers",
  marketplace: "obra-plugins",
  version: "1.2.0",
  enabled: true,
  autoUpgrade: false,
  broken: false,
  installPath: "/plugins/superpowers",
  installedAt: 1,
  lastUpdated: 1,
};

const COMPONENTS: PluginComponents = {
  skills: ["brainstorm", "debug", "plan"],
  commands: ["commit"],
  hooks: [
    { event: "PreToolUse", count: 3 },
    { event: "PostToolUse", count: 2 },
  ],
  mcpServers: ["github", "sentry"],
};

const CATALOG: MarketplaceCatalogPlugin = {
  name: "deskol",
  description: "Session desks for every project",
  category: "productivity",
};

test("an installed row with a broken entry shows the failed dot and its status chips", () => {
  render(
    <InstalledPluginRow entry={{ ...INSTALLED, broken: true, enabled: false, autoUpgrade: true }} onOpen={() => {}} />,
  );
  expect(document.querySelector('[role="img"][data-state="failed"]')).toBeTruthy();
  expect(chipKinds()).toEqual(["broken", "disabled", "auto-upgrade"]);
});

test("a loaded components cache appends the plugin's counts to its identity meta", () => {
  render(
    <InstalledPluginRow
      entry={INSTALLED}
      components={{ status: "loaded", components: COMPONENTS }}
      onOpen={() => {}}
    />,
  );
  const meta = screen.getByRole("button", { name: /superpowers/ }).textContent ?? "";
  expect(meta).toContain(`@ ${INSTALLED.marketplace}`);
  expect(meta).toContain(`v${INSTALLED.version}`);
  expect(document.querySelector('[data-kind="plugin-counts"]')?.textContent?.match(/\d+/g)).toEqual([
    "3",
    "1",
    "5",
    "2",
  ]);
});

test("an absent, loading, or error cache entry keeps today's identity-only meta", () => {
  const identity = `@ ${INSTALLED.marketplace} · v${INSTALLED.version}`;
  const { rerender } = render(<InstalledPluginRow entry={INSTALLED} onOpen={() => {}} />);
  const row = () => screen.getByRole("button", { name: /superpowers/ });
  expect(row().textContent).toBe(`${INSTALLED.plugin}${identity}`);
  rerender(<InstalledPluginRow entry={INSTALLED} components={{ status: "loading" }} onOpen={() => {}} />);
  expect(row().textContent).toBe(`${INSTALLED.plugin}${identity}`);
  rerender(
    <InstalledPluginRow
      entry={INSTALLED}
      components={{ status: "error", source: "request", error: "directory unreadable" }}
      onOpen={() => {}}
    />,
  );
  expect(row().textContent).toBe(`${INSTALLED.plugin}${identity}`);
});

test("a catalog row that is not installed carries Install in its trailing slot beside the chevron", () => {
  render(
    <CatalogPluginRow
      plugin={CATALOG}
      installed={false}
      installDisabled={false}
      onOpen={() => {}}
      onInstall={() => {}}
    />,
  );
  const main = screen.getByRole("button", { name: /deskol/ });
  const install = screen.getByRole("button", { name: "Install" });
  // No nested buttons: each control's closest button is itself.
  expect(main.closest("button")).toBe(main);
  expect(install.closest("button")).toBe(install);
  // The control rides the trailing slot; the drill-in chevron follows it.
  const slot = install.parentElement;
  expect(slot?.contains(main)).toBe(false);
  expect(slot?.children[0]).toBe(install);
  expect(slot?.children[1]?.getAttribute("aria-hidden")).toBe("true");
  // The meta line is the catalog description with its category.
  expect(main.textContent).toContain(CATALOG.description ?? "");
  expect(main.textContent).toContain(CATALOG.category ?? "");
  expect(main.closest('[data-kind="plugin-row"]')?.getAttribute("data-mode")).toBe("catalog");
});

test("Install installs without opening; the main area opens; both are keyboard targets", async () => {
  const user = userEvent.setup();
  const onOpen = vi.fn();
  const onInstall = vi.fn();
  render(
    <CatalogPluginRow
      plugin={CATALOG}
      installed={false}
      installDisabled={false}
      onOpen={onOpen}
      onInstall={onInstall}
    />,
  );
  const main = screen.getByRole("button", { name: /deskol/ });
  const install = screen.getByRole("button", { name: "Install" });
  // Two tab stops, main first: the split row's accepted keyboard shape.
  await user.tab();
  expect(document.activeElement).toBe(main);
  await user.tab();
  expect(document.activeElement).toBe(install);
  // Each control's click is its own: Install installs, the main area opens.
  await user.click(install);
  expect(onInstall).toHaveBeenCalledOnce();
  expect(onOpen).not.toHaveBeenCalled();
  await user.click(main);
  expect(onOpen).toHaveBeenCalledOnce();
  expect(onInstall).toHaveBeenCalledOnce();
});

test("a catalog row for an installed plugin shows the installed chip and no Install control", () => {
  render(
    <CatalogPluginRow plugin={CATALOG} installed installDisabled={false} onOpen={() => {}} onInstall={() => {}} />,
  );
  expect(chipKinds()).toEqual(["installed"]);
  expect(screen.queryByRole("button", { name: "Install" })).toBeNull();
  // No trailing control: the whole row is one button.
  expect(screen.getAllByRole("button")).toHaveLength(1);
});

test("the Install control carries installDisabled", () => {
  render(
    <CatalogPluginRow plugin={CATALOG} installed={false} installDisabled onOpen={() => {}} onInstall={() => {}} />,
  );
  expect((screen.getByRole("button", { name: "Install" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a category without a description has no leading metadata separator", () => {
  render(
    <CatalogPluginRow
      plugin={{ name: CATALOG.name, category: CATALOG.category }}
      installed={false}
      installDisabled={false}
      onOpen={() => {}}
      onInstall={() => {}}
    />,
  );
  expect(document.querySelector('[data-kind="plugin-meta"]')?.textContent).toBe(CATALOG.category);
});

test("launcher mode preserves Switch selection and separate Details with registered-hook totals", async () => {
  const user = userEvent.setup();
  const onToggleSelected = vi.fn();
  const onOpen = vi.fn();
  render(
    <LauncherPluginRow
      candidate={{
        name: "launch-fixture",
        source: "directory",
        path: "/fixture",
        selected: false,
        skillCount: 3,
        agentCount: 0,
        commandCount: 1,
        hookCount: 5,
        mcpCount: 2,
      }}
      selected={false}
      removeOnly={false}
      onToggleSelected={onToggleSelected}
      onOpen={onOpen}
    />,
  );
  expect(document.querySelector('[data-kind="plugin-row"]')?.getAttribute("data-mode")).toBe("launcher");
  expect(document.querySelector('[data-kind="plugin-counts"]')?.textContent?.match(/\d+/g)).toEqual([
    "3",
    "1",
    "5",
    "2",
  ]);
  await user.click(screen.getByRole("button", { name: "Details" }));
  expect(onOpen).toHaveBeenCalledOnce();
  expect(onToggleSelected).not.toHaveBeenCalled();
  await user.click(screen.getByRole("switch", { name: "launch-fixture" }));
  expect(onToggleSelected).toHaveBeenCalledWith(true);
});
