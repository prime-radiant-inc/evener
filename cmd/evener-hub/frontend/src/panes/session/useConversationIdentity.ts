import { useEffect } from "react";
import { threadsStore, useThreadsStore } from "../../stores/threads";
import { carryComposerDraft } from "./composer/draft";

/** The source owns draft precedence; each reader owns replacement and retention. */
export function useConversationIdentity(
  ref: string,
  policy: { replace(ref: string): boolean; hasOtherReaders(ref: string): boolean },
): void {
  const resumedIdentity = useThreadsStore((state) => state.resumedIdentities.get(ref));
  useEffect(() => {
    if (resumedIdentity === undefined) return;
    carryComposerDraft(ref, resumedIdentity);
    if (policy.replace(resumedIdentity) && !policy.hasOtherReaders(ref)) {
      threadsStore.getState().clearResumedIdentity(ref);
    }
  }, [ref, resumedIdentity, policy]);
}
