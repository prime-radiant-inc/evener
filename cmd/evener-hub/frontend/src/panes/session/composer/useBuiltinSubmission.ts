import { type BuiltinMatch, matchBuiltinInvocation } from "@evener/appwire-client";
import { type PaletteRunContext, type ScopedCommand, sessionBuiltinCommands } from "../../../shell/palette/commands";
import { liveThreadModel, pressLocalRecoveryFenced } from "../../../stores/liveControls";
import { useToasts } from "../../../widgets";
import { runBuiltinCommand } from "./builtinCommand";
import { readDraftRevision } from "./draft";
import type { ComposerSourceState } from "./sourceState";
import { useComposerActions } from "./useComposerActions";

/** Both presentations execute the same session commands and retain the same draft ownership. */
export function useBuiltinSubmission(source: ComposerSourceState, paneId?: string) {
  const ref = source.ref;
  const toasts = useToasts();
  const actions = useComposerActions(source);
  const sessionBuiltins = sessionBuiltinCommands({ sessionRef: ref, onPage: "session", sessionPaneId: paneId ?? null });

  async function run(match: BuiltinMatch<ScopedCommand>): Promise<void> {
    // Re-read the recovery fence at the press, before a command can mint durable intent.
    if (match.command.recoveryFenced && pressLocalRecoveryFenced(ref)) {
      toasts.push("error", `/${match.command.id} isn't available until this session is resumed`);
      return;
    }
    // These commands submit their parsed body through the existing recovery owner,
    // but clear only the unchanged literal invocation.
    if ((match.command.id === "queue" || match.command.id === "steer") && match.argsText.trim()) {
      if (match.command.unavailableReason) {
        toasts.push("error", `/${match.command.id} is ${match.command.unavailableReason}`);
        return;
      }
      const model = liveThreadModel(ref);
      const unavailable = model && match.command.available?.(model);
      if (unavailable !== undefined) {
        toasts.push("error", unavailable);
        return;
      }
      await actions.submit(match.command.id, { text: match.argsText, clearDraftText: source.getSnapshot().text });
      return;
    }
    const submitted = source.getSnapshot();
    const submittedRevision = source.continuity.draftEditRevisionRef.current;
    const submittedDraftRevision = readDraftRevision(ref);
    const release = source.tryAcquireBuiltinOperation();
    if (!release) return;
    const ctx: PaletteRunContext = {
      sessionRef: ref,
      sessionPaneId: paneId ?? null,
      onPage: "session",
      toasts,
      // App-global palette commands never belong to sessionBuiltins.
      ui: { clearToSearch: () => {}, showHelp: () => {} },
    };
    try {
      const outcome = await runBuiltinCommand(match, ctx);
      if (outcome.ok) {
        source.clearIfUnchanged(
          submitted.text,
          submittedRevision,
          submittedDraftRevision,
          submitted.skillNames,
          submitted.commandNames,
        );
      }
    } finally {
      release();
    }
  }

  /** True means handled, including refusal. Unknown/plugin commands and structured input stay literal. */
  function submitBuiltin(): boolean {
    const draft = source.getSnapshot();
    if (source.attachments.getState().items.length || draft.skillNames.length || draft.commandNames.length) {
      return false;
    }
    const match = matchBuiltinInvocation(draft.text, sessionBuiltins);
    if (!match) return false;
    if (!source.alive || draft.builtinOperationBusy || actions.busy) return true;
    void run(match);
    return true;
  }

  return { actions, sessionBuiltins, submitBuiltin };
}
