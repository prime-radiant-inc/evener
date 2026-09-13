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
