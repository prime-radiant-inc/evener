import type { UserEvent } from "@testing-library/user-event";

/**
 * enterText puts `text` into a form field the way a user pasting it would: a
 * click to focus the field, then one paste. `user.type` dispatches five or more
 * events per character, each re-rendering the form, which makes long strings
 * the slowest part of a form test. Use this when the test asserts only the
 * field's final value (what is saved, sent or refused); keep `user.type` for
 * key handling such as `{Enter}` and for per-keystroke behavior.
 */
export async function enterText(user: UserEvent, field: Element, text: string): Promise<void> {
  await user.click(field);
  await user.paste(text);
}
