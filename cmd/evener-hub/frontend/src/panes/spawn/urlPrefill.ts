// ?dir=/?prompt=/?host= URL prefill (spec §5). Read once from
// window.location.search at pane mount so a deep link (e.g. from an external
// "open in evener" button) can seed the working directory and/or the prompt.
// ?host= seeds the launch host - the rail's project-copy spawn uses it to
// target the copy's host. An absent or empty-valued param yields no entry -
// the caller layers whatever is present over the sticky defaults. Values are
// URLSearchParams-decoded verbatim (whitespace/newlines in a prompt survive),
// matching the raw-untrimmed prompt contract (floor §1.12).
export interface UrlPrefill {
  dir?: string;
  prompt?: string;
  host?: string;
}

export function readUrlPrefill(search: string): UrlPrefill {
  const params = new URLSearchParams(search);
  const prefill: UrlPrefill = {};
  const dir = params.get("dir");
  if (dir !== null && dir !== "") prefill.dir = dir;
  const prompt = params.get("prompt");
  if (prompt !== null && prompt !== "") prefill.prompt = prompt;
  const host = params.get("host");
  if (host !== null && host !== "") prefill.host = host;
  return prefill;
}
