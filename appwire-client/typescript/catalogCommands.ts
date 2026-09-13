import type { CommandDescriptor } from "./types.gen";

// slashCommandInvocation is the one place that decides what a user actually
// types to invoke a catalog command: a plugin-sourced command with a known
// pluginName needs the qualified "/plugin:name" form (unqualified "/name"
// only resolves the FIRST plugin registering that name - see app_rpc.go's
// own dispatch), everything else (user commands, and plugin commands
// without a pluginName, e.g. a stub catalog entry in a test) is unambiguous
// as bare "/name". Shared verbatim by the palette's catalogCommands (what
// its activateCommand inserts, via the stored field on Command) and
// composer/Composer.tsx's commitSlashCompletion (the inline "/" menu's
// insert) - a single source of truth for the qualification rule, so the two
// insertion paths can never drift back out of sync the way they did before
// this fix (the inline menu inserted bare "/name" even for plugin
// commands, which the hub dispatch cannot resolve for anything but the
// FIRST-registered plugin using that name).
export function slashCommandInvocation(command: Pick<CommandDescriptor, "name" | "source" | "pluginName">): string {
  return command.source === "plugin" && command.pluginName
    ? `/${command.pluginName}:${command.name}`
    : `/${command.name}`;
}

// The command catalog is global, but plugin commands are only valid in a
// session that loaded their plugin. Keep this filter at the palette boundary:
// the store remains the complete catalog for other consumers, and the
// no-session state deliberately keeps its global view.
export function visibleCatalogCommands(
  commands: CommandDescriptor[],
  activePluginNames: ReadonlySet<string> | null | undefined,
): CommandDescriptor[] {
  if (activePluginNames === undefined) return commands;
  return commands.filter(
    (command) =>
      command.source !== "plugin" ||
      (activePluginNames !== null && command.pluginName !== undefined && activePluginNames.has(command.pluginName)),
  );
}
