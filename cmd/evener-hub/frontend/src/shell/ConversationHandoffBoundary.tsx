import { type ReactNode, useEffect } from "react";
import { completeConversationHandoffPane } from "../stores/conversationClaimHandoff";

/** Inside Suspense, so this passive acknowledgment follows descendant claims. */
export function ConversationHandoffBoundary({
  generation,
  paneId,
  children,
}: {
  generation: number | undefined;
  paneId: string;
  children: ReactNode;
}) {
  useEffect(() => {
    completeConversationHandoffPane(generation, paneId);
  }, [generation, paneId]);
  return children;
}
