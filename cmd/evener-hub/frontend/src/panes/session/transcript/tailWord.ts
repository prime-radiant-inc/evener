/** Splits `text` into everything up to and including the whitespace before
 * its final word, and that final word - the two pieces a glyph-bearing line
 * renders around its atomic tail unit, so the trailing chevron or control
 * can never wrap onto a line of its own (ToolRow's .intentTail/.summaryTail
 * and NotificationCard's .secondaryTail all glue on this split). A
 * single-word (or empty) text returns ["", text]: the whole text is the
 * atom. Multi-line text splits at the final word of its LAST line: the
 * earlier lines are head, so the atom is always one line's final word. */
export function splitTrailingWord(text: string): [leading: string, trailing: string] {
  const match = /^([\s\S]*\s)(\S+)$/.exec(text);
  const leading = match?.[1];
  const trailing = match?.[2];
  return leading !== undefined && trailing !== undefined ? [leading, trailing] : ["", text];
}
