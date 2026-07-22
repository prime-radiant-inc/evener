import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { SettingsNav } from "./SettingsNav";

afterEach(cleanup);

test("renders a nav landmark labelled Settings sections", () => {
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
});

test("renders all 16 section links with their visible labels", () => {
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  for (const label of [
    "General",
    "Theme",
    "Transcript",
    "Display",
    "Notifications",
    "Providers & credentials",
    "Agents",
    "Serf launch",
    "Codex launch",
    "In-repo config",
    "Marketplaces & Plugins",
    "Plugins",
    "Skills",
    "MCP servers",
    "Hub",
    "Storage",
  ]) {
    expect(screen.getByRole("button", { name: label })).toBeTruthy();
  }
});

test("renders the 3 cluster headers", () => {
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  expect(screen.getByText("Agents & models")).toBeTruthy();
  expect(screen.getByText("Extensions")).toBeTruthy();
  expect(screen.getByText("Daemon")).toBeTruthy();
});

test("the active section's link carries aria-current=page; others don't", () => {
  render(<SettingsNav activeId="theme" onNavigate={vi.fn()} />);
  expect(screen.getByRole("button", { name: "Theme" }).getAttribute("aria-current")).toBe("page");
  expect(screen.getByRole("button", { name: "General" }).getAttribute("aria-current")).toBeNull();
});

test("clicking a link calls onNavigate with that section's id", async () => {
  const user = userEvent.setup();
  const onNavigate = vi.fn();
  render(<SettingsNav activeId="general" onNavigate={onNavigate} />);
  await user.click(screen.getByRole("button", { name: "Hub" }));
  expect(onNavigate).toHaveBeenCalledWith("hub");
});

test("the filter input has an accessible name", () => {
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  expect(screen.getByRole("searchbox", { name: "Filter settings" })).toBeTruthy();
});

test('filtering "agents" hides General but keeps Agents', async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "agents");

  expect(screen.queryByRole("button", { name: "General" })).toBeNull();
  expect(screen.getByRole("button", { name: "Agents" })).toBeTruthy();
});

test("filtering is case-insensitive", async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "THEME");
  expect(screen.getByRole("button", { name: "Theme" })).toBeTruthy();
});

test("a cluster header hides once every one of its links is filtered out", async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  await user.type(screen.getByRole("searchbox", { name: "Filter settings" }), "storage");

  expect(screen.getByText("Daemon")).toBeTruthy(); // Storage matches, stays
  expect(screen.queryByText("Agents & models")).toBeNull(); // nothing in it matches
  expect(screen.queryByText("Extensions")).toBeNull();
});

test("clearing the filter re-shows every link", async () => {
  const user = userEvent.setup();
  render(<SettingsNav activeId="general" onNavigate={vi.fn()} />);
  const filter = screen.getByRole("searchbox", { name: "Filter settings" });
  await user.type(filter, "storage");
  expect(screen.queryByRole("button", { name: "General" })).toBeNull();

  await user.clear(filter);

  expect(screen.getByRole("button", { name: "General" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Storage" })).toBeTruthy();
});
