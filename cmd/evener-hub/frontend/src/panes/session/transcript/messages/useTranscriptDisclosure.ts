import { scopedDisclosureId } from "@evener/appwire-client";
import { disclosureDefault, isDisclosureOpen } from "../../../../widgets/disclosure/disclosureStore";
export function useTranscriptDisclosure(options: { scope: string; id: string; defaultOpen?: boolean }) {
  const key = scopedDisclosureId(options.scope, options.id);
  const fallback = (options.defaultOpen ?? false) || disclosureDefault(options.scope, options.id, false);
  return { key, fallback, open: isDisclosureOpen(key, fallback) };
}
