// ?dir=/?prompt= URL prefill (spec §5). Read once from window.location.search
// at pane mount so a deep link (e.g. from an external "open in serf" button)
// can seed the working directory and/or the prompt. An absent or empty-valued
// param yields no entry - the caller layers whatever is present over the sticky
// defaults. Values are URLSearchParams-decoded verbatim (whitespace/newlines in
// a prompt survive), matching the raw-untrimmed prompt contract (floor §1.12).
export interface UrlPrefill {
  dir?: string;
  prompt?: string;
}

export function readUrlPrefill(search: string): UrlPrefill {
  const params = new URLSearchParams(search);
  const prefill: UrlPrefill = {};
  const dir = params.get("dir");
  if (dir !== null && dir !== "") prefill.dir = dir;
  const prompt = params.get("prompt");
  if (prompt !== null && prompt !== "") prefill.prompt = prompt;
  return prefill;
}
