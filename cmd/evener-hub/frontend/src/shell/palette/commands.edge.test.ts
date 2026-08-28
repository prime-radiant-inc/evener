// Edge cases for commands.ts that close the remaining uncovered lines:
// - copyToClipboard with clipboard API and execCommand fallback
// - slashCommandInvocation for plugin and non-plugin commands
// - rememberableId for stayOpen and non-stayOpen commands

import { afterEach, expect, test, vi } from "vitest";
import type { CommandDescriptor } from "../../protocol/types.gen";
import type { Command } from "./commands";
import {
  copyToClipboard,
  rememberableId,
  sessionScopedHandoffMatch,
  slashCommandInvocation,
  visibleCatalogCommands,
} from "./commands";

// --- copyToClipboard ---

afterEach(() => {
  vi.unstubAllGlobals();
});

test("copyToClipboard uses navigator.clipboard.writeText when available", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  await copyToClipboard("test text");
  expect(writeText).toHaveBeenCalledExactlyOnceWith("test text");
  expect(document.querySelector("textarea")).toBeNull();
});

test("copyToClipboard falls back to execCommand when clipboard API rejects", async () => {
  const writeText = vi.fn().mockRejectedValue(new Error("not allowed"));
  const execCommand = vi.fn().mockImplementation(() => {
    expect(document.querySelector("textarea")?.value).toBe("fallback text");
    return true;
  });
  // jsdom already has document.execCommand, but we need to control it
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const originalExecCommand = document.execCommand;
  document.execCommand = execCommand;
  try {
    await copyToClipboard("fallback text");
    expect(writeText).toHaveBeenCalledExactlyOnceWith("fallback text");
    expect(execCommand).toHaveBeenCalledExactlyOnceWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  } finally {
    document.execCommand = originalExecCommand;
  }
});

test("copyToClipboard uses execCommand fallback when clipboard API is unavailable", async () => {
  const execCommand = vi.fn().mockImplementation(() => {
    expect(document.querySelector("textarea")?.value).toBe("fallback text");
    return true;
  });
  vi.stubGlobal("navigator", {});
  const originalExecCommand = document.execCommand;
  document.execCommand = execCommand;
  try {
    await copyToClipboard("fallback text");
    expect(execCommand).toHaveBeenCalledExactlyOnceWith("copy");
    expect(document.querySelector("textarea")).toBeNull();
  } finally {
    document.execCommand = originalExecCommand;
  }
});

test("copyToClipboard rejects when execCommand returns false", async () => {
  const execCommand = vi.fn().mockReturnValue(false);
  vi.stubGlobal("navigator", {});
  const originalExecCommand = document.execCommand;
  document.execCommand = execCommand;
  try {
    await expect(copyToClipboard("text")).rejects.toThrow("execCommand copy returned false");
    expect(document.querySelector("textarea")).toBeNull();
  } finally {
    document.execCommand = originalExecCommand;
  }
});

// --- rememberableId ---

test("rememberableId returns empty string for stayOpen command", () => {
  const cmd: Command = { id: "search", title: "Search", scope: "global", stayOpen: true, hint: "", keywords: [] };
  expect(rememberableId(cmd)).toBe("");
});

test("rememberableId returns the id for non-stayOpen command", () => {
  const cmd: Command = { id: "upgrade", title: "Upgrade", scope: "global", hint: "", keywords: [] };
  expect(rememberableId(cmd)).toBe("upgrade");
});

// --- slashCommandInvocation ---

test("slashCommandInvocation returns qualified path for plugin command", () => {
  const cmd: Pick<CommandDescriptor, "name" | "source" | "pluginName"> = {
    name: "test",
    source: "plugin",
    pluginName: "myplugin",
  };
  expect(slashCommandInvocation(cmd)).toBe("/myplugin:test");
});

test("slashCommandInvocation returns bare path for non-plugin command", () => {
  const cmd: Pick<CommandDescriptor, "name" | "source" | "pluginName"> = {
    name: "test",
    source: "user",
    pluginName: undefined,
  };
  expect(slashCommandInvocation(cmd)).toBe("/test");
});

test("slashCommandInvocation returns bare path for plugin command without pluginName", () => {
  const cmd: Pick<CommandDescriptor, "name" | "source" | "pluginName"> = {
    name: "test",
    source: "plugin",
    pluginName: undefined,
  };
  expect(slashCommandInvocation(cmd)).toBe("/test");
});

// --- sessionScopedHandoffMatch ---

test("sessionScopedHandoffMatch returns false for empty query", () => {
  expect(sessionScopedHandoffMatch("", [])).toBe(false);
  expect(sessionScopedHandoffMatch("/", [])).toBe(false);
  expect(sessionScopedHandoffMatch("  ", [])).toBe(false);
});

test("sessionScopedHandoffMatch returns true when first token matches a builtin command id", () => {
  // "clear" is a builtin session command
  expect(sessionScopedHandoffMatch("/clear", [])).toBe(true);
  expect(sessionScopedHandoffMatch("/cl", [])).toBe(true);
});

test("sessionScopedHandoffMatch returns true when first token matches a catalog command name", () => {
  const catalog: CommandDescriptor[] = [{ name: "custom-cmd", source: "plugin", pluginName: "test", description: "" }];
  expect(sessionScopedHandoffMatch("/custom", catalog)).toBe(true);
  expect(sessionScopedHandoffMatch("/custom-c", catalog)).toBe(true);
});

test("sessionScopedHandoffMatch returns false when no command matches", () => {
  expect(sessionScopedHandoffMatch("/nonexistent", [])).toBe(false);
});

// --- visibleCatalogCommands ---

const CATALOG: CommandDescriptor[] = [
  { name: "review", source: "plugin", pluginName: "enabled" },
  { name: "review", source: "plugin", pluginName: "excluded" },
  { name: "status", source: "builtin" },
  { name: "whoami", source: "user" },
];

test("visibleCatalogCommands keeps the global catalog when there is no active session", () => {
  expect(visibleCatalogCommands(CATALOG, undefined)).toEqual(CATALOG);
});

test("visibleCatalogCommands filters plugin commands to loaded names and preserves duplicates and globals", () => {
  expect(visibleCatalogCommands(CATALOG, new Set(["enabled"]))).toEqual([CATALOG[0], CATALOG[2], CATALOG[3]]);
});

test("visibleCatalogCommands fails closed for unavailable diagnostics", () => {
  expect(visibleCatalogCommands(CATALOG, null)).toEqual([CATALOG[2], CATALOG[3]]);
});

test("visibleCatalogCommands excludes plugin commands for an explicit empty inventory while keeping built-ins", () => {
  expect(visibleCatalogCommands(CATALOG, new Set())).toEqual([CATALOG[2], CATALOG[3]]);
});

test("visibleCatalogCommands does not mutate the command catalog or its command descriptors", () => {
  const input = CATALOG.map((command) => ({ ...command }));
  const before = input.map((command) => ({ ...command }));

  visibleCatalogCommands(input, new Set(["enabled"]));

  expect(input).toEqual(before);
});
