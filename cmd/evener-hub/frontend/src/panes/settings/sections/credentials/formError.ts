// formError.ts: an inline refusal a credentials form shows. The reason is what
// the form renders as its alert's data-reason, so callers and tests can tell
// one refusal from another without reading the message a person reads.
export interface FormError<Reason extends string> {
  reason: Reason;
  message: string;
}
