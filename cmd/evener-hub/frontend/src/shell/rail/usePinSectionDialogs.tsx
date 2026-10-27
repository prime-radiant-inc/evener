import { errorText } from "@evener/appwire-client";
import { type ChangeEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { selectPinSectionSummaries } from "../../stores/navigation/selectors";
import { navigationStore } from "../../stores/navigation/store";
import { Button, ConfirmDialog, Dialog, FormRow, Input, useToasts } from "../../widgets";
import { useClient } from "../clientContext";
import { deletePinSection, renamePinSection } from "./actions";
import type { RailPinSection } from "./railNodes";
import type { PendingOp } from "./railPending";

export type RailRunAction = <T>(
  fn: () => Promise<T>,
  failure: string,
  optimistic?: PendingOp | ((result: T) => PendingOp),
  propagate?: boolean,
) => Promise<void>;

/** The pin-section rename and delete flows: their dialog state, the guards
 * that make a late reply inert once the flow is superseded or the rail
 * unmounts, and the dialogs to render. */
export function usePinSectionDialogs(runAction: RailRunAction): {
  openRename: (section: RailPinSection) => void;
  requestDelete: (section: RailPinSection) => Promise<void>;
  dialogs: ReactNode;
} {
  const client = useClient();
  const toasts = useToasts();
  const [sectionRenameTarget, setSectionRenameTarget] = useState<RailPinSection | null>(null);
  const [sectionRenameValue, setSectionRenameValue] = useState("");
  const [sectionRenameError, setSectionRenameError] = useState("");
  const [sectionRenameSubmitting, setSectionRenameSubmitting] = useState(false);
  const sectionRenameInputID = useId();
  const sectionRenameSubmission = useRef<{ token: number; sectionID: string } | null>(null);
  const sectionRenameToken = useRef(0);
  const sectionDeleteRequestToken = useRef(0);
  const [sectionDeleteTarget, setSectionDeleteTarget] = useState<{
    section: RailPinSection;
    memberCount: number;
  } | null>(null);

  useEffect(
    () => () => {
      sectionRenameToken.current += 1;
      sectionRenameSubmission.current = null;
      sectionDeleteRequestToken.current += 1;
    },
    [],
  );
  function openSectionRename(section: RailPinSection) {
    sectionRenameToken.current += 1;
    sectionRenameSubmission.current = null;
    setSectionRenameTarget(section);
    setSectionRenameValue(section.name);
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }
  function closeSectionRename() {
    if (sectionRenameSubmission.current) return;
    sectionRenameToken.current += 1;
    setSectionRenameTarget(null);
    setSectionRenameValue("");
    setSectionRenameError("");
    setSectionRenameSubmitting(false);
  }
  async function confirmSectionRename() {
    if (sectionRenameSubmission.current) return;
    const target = sectionRenameTarget;
    const name = sectionRenameValue.trim();
    if (!target) return;
    if (!name) {
      setSectionRenameError("Section name is required");
      return;
    }
    if ([...name].length > 80) {
      setSectionRenameError("Section names must be 80 characters or fewer");
      return;
    }
    const submission = { token: sectionRenameToken.current + 1, sectionID: target.id };
    sectionRenameToken.current = submission.token;
    sectionRenameSubmission.current = submission;
    setSectionRenameSubmitting(true);
    try {
      await runAction(
        () => renamePinSection(client, target.id, name),
        "Couldn't rename pin section",
        (section) => ({ kind: "pinSectionRename", id: target.id, name: section.section.name }),
        true,
      );
      if (sectionRenameSubmission.current !== submission) return;
      sectionRenameSubmission.current = null;
      setSectionRenameTarget(null);
      setSectionRenameValue("");
      setSectionRenameSubmitting(false);
    } catch (error) {
      if (sectionRenameSubmission.current !== submission) return;
      sectionRenameSubmission.current = null;
      setSectionRenameError(errorText(error));
      setSectionRenameSubmitting(false);
    }
  }
  async function requestSectionDelete(section: RailPinSection) {
    const token = ++sectionDeleteRequestToken.current;
    try {
      await navigationStore.getState().loadPinCatalogPages(true);
      if (token !== sectionDeleteRequestToken.current) return;
      const summaries = selectPinSectionSummaries(navigationStore.getState());
      const durable = summaries.find((candidate) => candidate.id === section.id);
      if (!durable) throw new Error("pin section not found");
      setSectionDeleteTarget({ section, memberCount: durable.member_count });
    } catch (error) {
      if (token === sectionDeleteRequestToken.current)
        toasts.push("error", `Couldn't load pin section details: ${errorText(error)}`);
    }
  }
  async function confirmSectionDelete() {
    const target = sectionDeleteTarget;
    if (!target) return;
    setSectionDeleteTarget(null);
    await runAction(() => deletePinSection(client, target.section.id), "Couldn't delete pin section", {
      kind: "pinSectionDelete",
      id: target.section.id,
    });
  }
  return {
    openRename: openSectionRename,
    requestDelete: requestSectionDelete,
    dialogs: (
      <>
        {sectionRenameTarget && (
          <Dialog
            open
            onClose={closeSectionRename}
            title="Rename pin section"
            footer={
              <>
                <Button variant="quiet" onClick={closeSectionRename} disabled={sectionRenameSubmitting}>
                  Cancel
                </Button>
                <Button onClick={() => void confirmSectionRename()} disabled={sectionRenameSubmitting}>
                  Rename section
                </Button>
              </>
            }
          >
            <FormRow label="Section name" htmlFor={sectionRenameInputID} error={sectionRenameError || undefined}>
              <Input
                id={sectionRenameInputID}
                value={sectionRenameValue}
                onChange={(event: ChangeEvent<HTMLInputElement>) => {
                  setSectionRenameValue(event.target.value);
                  setSectionRenameError("");
                }}
                disabled={sectionRenameSubmitting}
                aria-describedby={sectionRenameError ? `${sectionRenameInputID}-error` : undefined}
              />
            </FormRow>
          </Dialog>
        )}
        <ConfirmDialog
          open={sectionDeleteTarget !== null}
          title="Delete pin section?"
          confirmLabel="Delete section"
          onConfirm={() => void confirmSectionDelete()}
          onCancel={() => setSectionDeleteTarget(null)}
        >
          {sectionDeleteTarget &&
            `Delete “${sectionDeleteTarget.section.name}”? This will unpin ${sectionDeleteTarget.memberCount} session${sectionDeleteTarget.memberCount === 1 ? "" : "s"}.`}
        </ConfirmDialog>
      </>
    ),
  };
}
