// The tool bodies that display JSON (a tool's raw output, an MCP call's
// arguments) share one display rule: pretty-print when the whole text parses
// as a JSON object or array, and keep the tool's original bytes in the copy
// control. Only the copy label and readability preferences differ, so they
// live here rather than in each body.

import { prettyJSON } from "@evener/appwire-client";
import { CodeBlock } from "../../../widgets";

export function JSONCodeBlock({
  text,
  copyLabel,
  fold,
  language,
}: {
  text: string;
  copyLabel: string;
  fold?: boolean;
  language?: string;
}) {
  const pretty = prettyJSON(text);
  if (pretty === undefined || pretty === text) {
    return <CodeBlock text={text} copyLabel={copyLabel} fold={fold} />;
  }
  return <CodeBlock text={pretty} copyText={text} language={language} copyLabel={copyLabel} fold={fold} />;
}
