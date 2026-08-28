// InstanceDetailSheet: the provider-instance inspector for the detail-sheet
// redesign. Opens from an InstanceRow tap and owns everything the old row
// carried: the layered credential display (the effective source, plus an
// environment variable shadowed behind it), the meta table, and every
// per-instance ACTION (test, set/replace key, sign in/refresh OAuth, edit,
// make default, clear, clear stored key, remove). A right side Sheet on
// desktop, a bottom Sheet on mobile (useIsMobile, the shell's own source).
// The instance is read from the store by name so cross-client changes land
// live, and the sheet closes itself when the instance disappears (its own
// Remove completing, or another client's) - an inspector is only as alive
// as its subject.
// Presentation only - the section owns what each action DOES (opening an
// editor, a confirm dialog, or calling the store), same division of labor
// as the old InstanceRow.
import { useEffect } from "react";
import type { AuthTestResponse } from "../../../../protocol/types.gen";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { useCredentialsStore } from "../../../../stores/credentials";
import { Button, Chip, Sheet, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  credentialLayers,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  styleInfoText,
  unconfiguredLabel,
} from "./credentialLabels";
import styles from "./InstanceDetailSheet.module.css";

const CLASS = {
  headingRow: requireClass(styles.headingRow, "InstanceDetailSheet.module.css", "headingRow"),
  layers: requireClass(styles.layers, "InstanceDetailSheet.module.css", "layers"),
  layer: requireClass(styles.layer, "InstanceDetailSheet.module.css", "layer"),
  unconfigured: requireClass(styles.unconfigured, "InstanceDetailSheet.module.css", "unconfigured"),
  metaList: requireClass(styles.metaList, "InstanceDetailSheet.module.css", "metaList"),
  metaRow: requireClass(styles.metaRow, "InstanceDetailSheet.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "InstanceDetailSheet.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "InstanceDetailSheet.module.css", "metaValue"),
  metaMono: requireClass(styles.metaMono, "InstanceDetailSheet.module.css", "metaMono"),
  actionRows: requireClass(styles.actionRows, "InstanceDetailSheet.module.css", "actionRows"),
  fullRow: requireClass(styles.fullRow, "InstanceDetailSheet.module.css", "fullRow"),
  divider: requireClass(styles.divider, "InstanceDetailSheet.module.css", "divider"),
  testResult: requireClass(styles.testResult, "InstanceDetailSheet.module.css", "testResult"),
};

export interface InstanceDetailSheetProps {
  name: string | null;
  onClose: () => void;
  onSetApiKey: () => void;
  onOAuthStart: () => void;
  onEdit: () => void;
  onClear: () => void;
  onClearStoredKey: () => void;
  onRemove: () => void;
  onSetDefault: () => void;
  onTestCredentials: () => void;
  testCredentialsPending?: boolean;
  testCredentialsResult?: AuthTestResponse;
  /** Disables Edit/Remove/make default while providers.toml cannot be
   * written (InstanceListResponse.writesRefused, spec §11.3) - Set key/Sign
   * in/Clear/Clear stored key/Test credentials are unaffected: they write
   * the credentials store or an OAuth record, never providers.toml. */
  writesRefused?: boolean;
}

export function InstanceDetailSheet({
  name,
  onClose,
  onSetApiKey,
  onOAuthStart,
  onEdit,
  onClear,
  onClearStoredKey,
  onRemove,
  onSetDefault,
  onTestCredentials,
  testCredentialsPending = false,
  testCredentialsResult,
  writesRefused = false,
}: InstanceDetailSheetProps) {
  const instances = useCredentialsStore((s) => s.instances);
  const isMobile = useIsMobile();

  const instance = name === null ? undefined : instances.find((i) => i.name === name);

  // An inspector is only as alive as its subject: the instance can vanish
  // under an open sheet (its own Remove completing, or another client's
  // change), and an inspector for a thing that no longer exists closes
  // itself rather than offering actions on a ghost.
  useEffect(() => {
    if (name !== null && instance === undefined) onClose();
  }, [name, instance, onClose]);

  const open = name !== null && instance !== undefined;

  const supportsApiKey = instance !== undefined && (instance.authModes ?? []).includes("apiKey");
  const supportsOAuth = instance !== undefined && (instance.authModes ?? []).includes("oauth");
  const showClear = instance !== undefined && (instance.activeSource === "store" || instance.activeSource === "oauth");
  // showClearStoredKey: a stray stored key sits shadowed behind whatever IS
  // active (the same condition credentialLayers uses to render that second,
  // non-effective layer above) - true for an oauth/adc login with a leftover
  // credentials.toml entry, and just as much for a signed-out Codex row a
  // previous Clear left stranded (Clear's Codex branch removes the OAuth
  // record, not the file, when one is active; issue #713). Clear alone
  // cannot reach this state: on a Codex row it would drop the active login
  // instead of the stray key, and on an adc/api_key/env row it never shows
  // at all. This action always targets the store layer only, so it is safe
  // to offer regardless of what is effective.
  const showClearStoredKey = instance?.hasStoredFile && instance.activeSource !== "store";
  // The danger zone is Clear + Clear stored key + Remove under a divider; an
  // implicit instance with nothing stored offers none of them, and a divider
  // over nothing reads as a rendering bug.
  const showDangerZone = instance !== undefined && (showClear || showClearStoredKey || !instance.implicit);
  const layers = instance === undefined ? [] : credentialLayers(instance);
  const unconfigured = instance === undefined ? null : unconfiguredLabel(instance);
  const safeTestResult = testCredentialsResult
    ? safeCredentialTestResult(name ?? "", testCredentialsResult)
    : undefined;

  return (
    <Sheet open={open} onClose={onClose} title={instance?.name ?? ""} side={isMobile ? "bottom" : "right"}>
      {instance !== undefined && (
        <>
          <div className={CLASS.headingRow}>
            <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
            {instance.isDefault && <Chip>★ default</Chip>}
            {instance.implicit && <Chip>from environment</Chip>}
          </div>
          {unconfigured !== null ? (
            <p className={CLASS.unconfigured}>{unconfigured}</p>
          ) : (
            <div className={CLASS.layers}>
              {layers.map((layer) => (
                <div key={layer.source} className={CLASS.layer}>
                  <span>↳ {layer.label}</span>
                  <Chip tone={layer.effective ? "alive" : "neutral"}>{layer.effective ? "effective" : "shadowed"}</Chip>
                </div>
              ))}
            </div>
          )}
          <div className={CLASS.metaList}>
            <div className={CLASS.metaRow}>
              <span className={CLASS.metaLabel}>Provider</span>
              <span className={CLASS.metaValue}>{instance.providerId}</span>
            </div>
            <div className={CLASS.metaRow}>
              <span className={CLASS.metaLabel}>API</span>
              <span className={`${CLASS.metaValue} ${CLASS.metaMono}`}>{styleInfoText(instance)}</span>
            </div>
          </div>
          <div className={CLASS.actionRows}>
            <div className={CLASS.fullRow}>
              <Button variant="quiet" onClick={onTestCredentials} disabled={testCredentialsPending}>
                {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
              </Button>
            </div>
            {safeTestResult && (
              <p className={CLASS.testResult} role="status">
                {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
              </p>
            )}
            {supportsApiKey && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetApiKey}>
                  {instance.hasStoredFile ? "Replace key" : "Set key"}
                </Button>
              </div>
            )}
            {supportsOAuth && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onOAuthStart}>
                  {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
                </Button>
              </div>
            )}
            {!instance.isDefault && (
              <div className={CLASS.fullRow}>
                <Button variant="quiet" onClick={onSetDefault} disabled={writesRefused}>
                  ★ make default
                </Button>
              </div>
            )}
            <div className={CLASS.fullRow}>
              <Button variant="quiet" onClick={onEdit} disabled={writesRefused}>
                Edit
              </Button>
            </div>
          </div>
          {showDangerZone && (
            <>
              <hr className={CLASS.divider} />
              <div className={CLASS.actionRows}>
                {showClearStoredKey && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClearStoredKey}>
                      Clear stored key
                    </Button>
                  </div>
                )}
                {showClear && (
                  <div className={CLASS.fullRow}>
                    <Button variant="dangerQuiet" onClick={onClear}>
                      Clear
                    </Button>
                  </div>
                )}
                {!instance.implicit && (
                  <div className={CLASS.fullRow}>
                    <Button variant="danger" onClick={onRemove} disabled={writesRefused}>
                      Remove
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </>
      )}
    </Sheet>
  );
}
