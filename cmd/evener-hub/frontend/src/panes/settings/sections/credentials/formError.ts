// formError.ts: an inline refusal a credentials form shows. The reason is what
// the form renders as its alert's data-reason, so callers and tests can tell
// one refusal from another without reading the message a person reads.
export interface FormError<Reason extends string> {
  reason: Reason;
  message: string;
}

const LITERAL_HEADER = "Credential header must reference a $VARIABLE or run a $(command), never a literal secret.";

/** Blank (after trimming) is allowed; anything else must reference a variable or command. */
export function checkCredentialHeader(value: string | undefined): FormError<"literal-header"> | null {
  if (!value?.trim() || value.includes("$")) return null;
  return { reason: "literal-header", message: LITERAL_HEADER };
}
