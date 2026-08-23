import { type JSX, useState } from "react";
import type { MobileCapabilities } from "../../conversation/model";
import type { ConversationService } from "../../services/conversation";
import { Button } from "../../ui/Button";
import { Input } from "../../ui/Input";

export interface ControlsSectionProps {
  readonly capabilities: MobileCapabilities;
  readonly reasoningEffort?: string;
  readonly conversationService: ConversationService;
}

/**
 * Controls section — each control checks its capability before allowing the
 * action. A false capability removes the control and leaves an "Unavailable
 * for this source" explanation. Destructive shutdown requires confirmation.
 */
export function ControlsSection({
  capabilities,
  reasoningEffort,
  conversationService,
}: ControlsSectionProps): JSX.Element {
  const [pendingShutdown, setPendingShutdown] = useState(false);
  const [shutdownError, setShutdownError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");

  async function handleCompact() {
    setActionError(null);
    try {
      await conversationService.compact();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleInterrupt() {
    setActionError(null);
    try {
      await conversationService.interrupt();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleRename() {
    if (renameValue.trim() === "") return;
    setActionError(null);
    try {
      await conversationService.rename(renameValue.trim());
      setRenameValue("");
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleConfirmShutdown() {
    setShutdownError(null);
    try {
      await conversationService.shutdown();
      setPendingShutdown(false);
    } catch (err) {
      setShutdownError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <section
      className="evener-activity-section"
      data-testid="activity-controls"
    >
      <div className="evener-activity-section__header">
        <span className="evener-activity-section__label">Controls</span>
      </div>
      <div className="evener-activity-section__detail evener-activity-controls">
        {/* Compact */}
        {capabilities.compact ? (
          <Button variant="secondary" onClick={handleCompact}>
            Compact
          </Button>
        ) : (
          <Unavailable explanation="Compact" />
        )}

        {/* Interrupt */}
        {capabilities.interrupt ? (
          <Button variant="secondary" onClick={handleInterrupt}>
            Interrupt
          </Button>
        ) : (
          <Unavailable explanation="Interrupt" />
        )}

        {/* Model / effort (V1 placeholder — display only) */}
        {capabilities.changeModel ? (
          <div className="evener-activity-controls__model">
            {reasoningEffort !== undefined ? (
              <span className="evener-activity-controls__effort">
                Effort: {reasoningEffort}
              </span>
            ) : null}
          </div>
        ) : null}

        {/* Rename */}
        {capabilities.rename ? (
          <div className="evener-activity-controls__rename">
            <Input
              label="Rename"
              name="rename"
              placeholder="New name"
              value={renameValue}
              onChange={(e) => setRenameValue(e.target.value)}
              onBlur={handleRename}
            />
          </div>
        ) : (
          <Unavailable explanation="Rename" />
        )}

        {/* Shutdown — destructive, requires confirmation */}
        {capabilities.shutdown ? (
          <div className="evener-activity-controls__shutdown">
            <Button variant="danger" onClick={() => setPendingShutdown(true)}>
              Shutdown
            </Button>
            {pendingShutdown ? (
              <div
                className="evener-activity-controls__confirm"
                role="alertdialog"
              >
                <p>Confirm shutdown — this will end the session.</p>
                <Button variant="danger" onClick={handleConfirmShutdown}>
                  Confirm
                </Button>
                <Button
                  variant="secondary"
                  onClick={() => {
                    setPendingShutdown(false);
                    setShutdownError(null);
                  }}
                >
                  Cancel
                </Button>
                {shutdownError !== null ? (
                  <p className="evener-activity-controls__error" role="alert">
                    {shutdownError}
                  </p>
                ) : null}
              </div>
            ) : null}
          </div>
        ) : (
          <Unavailable explanation="Shutdown" />
        )}

        {actionError !== null ? (
          <p className="evener-activity-controls__error" role="alert">
            {actionError}
          </p>
        ) : null}
      </div>
    </section>
  );
}

function Unavailable({ explanation }: { explanation: string }): JSX.Element {
  return (
    <div className="evener-activity-controls__unavailable">
      <span className="evener-activity-controls__unavailable-label">
        {explanation}
      </span>
      <span className="evener-activity-controls__unavailable-reason">
        Unavailable for this source
      </span>
    </div>
  );
}
