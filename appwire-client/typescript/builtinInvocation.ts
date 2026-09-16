// Does a composer draft invoke a built-in session command? matchBuiltinInvocation
// parses a leading "/<name>" against the host's resolved built-in list and
// splits off the argument text; findBuiltinArgument resolves that text to one
// of an enum command's items by id or label. Pure and framework-free, shared
// by the web composer, spawn form and native composer.

export interface BuiltinMatch<T> {
  command: T;
  argsText: string;
}

export function findBuiltinArgument<T extends { id: string; label: string }>(
  items: readonly T[],
  text: string,
): T | undefined {
  const needle = text.trim().toLowerCase();
  return items.find((item) => item.id.toLowerCase() === needle || item.label.toLowerCase() === needle);
}

// A leading "/<token>" optionally followed by whitespace and the rest of the
// message - `<token>` may not itself contain whitespace (a command name
// never does; slashCommandInvocation only ever produces "name" or
// "plugin:name" shapes, neither one with a space in it). The argument
// capture includes newlines because /goal and /steer accept multiline text.
const INVOCATION_RE = /^\/(\S+)(?:[ \t]+([\s\S]*))?$/;

// matchBuiltinInvocation: does `text` parse as a known BUILT-IN session
// command? `builtins` must be the FULL resolved built-in list, not the
// availability-filtered one the inline menu shows (which drops an unavailable
// command so there is nothing to pick): matching here still finds an
// unavailable command, so the host's command runner can answer with its real
// reason instead of the draft silently being sent as a literal chat message.
// A command with no `args` only matches when nothing follows the name -
// "/compact extra text" is not a known invocation of the argless /compact, so
// it falls through to being sent as an ordinary message instead of ignoring
// the trailing text.
export function matchBuiltinInvocation<T extends { id: string; args?: unknown }>(
  text: string,
  builtins: readonly T[],
): BuiltinMatch<T> | null {
  const m = INVOCATION_RE.exec(text);
  if (!m) return null;
  const token = m[1] ?? "";
  const argsText = m[2] ?? "";
  const command = builtins.find((c) => c.id === token);
  if (!command) return null;
  if (!command.args && argsText.trim() !== "") return null;
  return { command, argsText };
}
