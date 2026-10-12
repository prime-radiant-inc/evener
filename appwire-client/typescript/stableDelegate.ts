export interface StableDelegateState {
  status?: string;
  outcome?: string;
  terminal?: boolean;
}

// A stable delegate's status is its reusable resource lifecycle. Only a
// terminal snapshot describes the last run through outcome; a resumed run can
// retain that old outcome while its current lifecycle is running again.
export function stableDelegateDisplayStatus(delegate: StableDelegateState): string | undefined {
  return delegate.terminal ? (delegate.outcome ?? delegate.status) : delegate.status;
}

/** A working row's non-empty stated intent: the line that stands in for the
 * status word. Empty whenever the row is terminal or states nothing. */
export function delegateStatedIntent(delegate: StableDelegateState & { latestIntent?: string }): string {
  return !delegate.terminal && delegate.latestIntent ? delegate.latestIntent : "";
}

// A working row's newest tool intent stands in for the status word; every
// other row state keeps the word the surface passed in. Each client owns its
// own status vocabulary, and this one helper owns the substitution rule, so
// the surfaces cannot drift. The intent is display-only: the status still
// drives sorting, aggregation, and accessibility.
export function delegateWorkText(delegate: StableDelegateState & { latestIntent?: string }, status: string): string {
  const intent = delegateStatedIntent(delegate);
  return intent !== "" ? intent : status;
}
