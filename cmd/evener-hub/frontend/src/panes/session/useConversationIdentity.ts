import { useEffect } from "react";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { carryComposerDraft, carryRecoverySelection } from "./composer/draft";

/** The source owns draft precedence; each reader owns replacement and retention. */
export function useConversationIdentity(
  ref: string,
  policy: { replace(ref: string): boolean; hasOtherReaders(ref: string): boolean },
): void {
  const resumedIdentity = useThreadsStore((state) => state.resumedIdentities.get(ref));
  useEffect(() => {
    if (resumedIdentity === undefined) return;
    carryComposerDraft(ref, resumedIdentity);
    // The pick is keyed by conversation: it follows the identity rewrite
    // here, before any reader is replaced, so the replacement composer
    // restores the conversation's preserved work.
    carryRecoverySelection(ref, resumedIdentity);
    if (policy.replace(resumedIdentity) && !policy.hasOtherReaders(ref)) {
      threadsStore.getState().clearResumedIdentity(ref);
    }
  }, [ref, resumedIdentity, policy]);
}
