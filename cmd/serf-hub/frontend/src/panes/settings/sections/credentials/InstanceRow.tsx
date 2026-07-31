// InstanceRow.tsx: one provider instance's display + row actions
// (parity-m7-settings.md §7c) - pure presentational component, no store
// access of its own. CredentialsSection supplies every action as a plain
// callback and owns what happens next (opening an editor, a confirm
// dialog, or calling the store directly).
import type { AuthTestResponse, InstanceEntry } from "../../../../protocol/types.gen";
import { Button, Chip, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  credentialLayers,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  unconfiguredLabel,
} from "./credentialLabels";
import styles from "./InstanceRow.module.css";

const CLASS = {
  row: requireClass(styles.row, "InstanceRow.module.css", "row"),
  heading: requireClass(styles.heading, "InstanceRow.module.css", "heading"),
  name: requireClass(styles.name, "InstanceRow.module.css", "name"),
  styleInfo: requireClass(styles.styleInfo, "InstanceRow.module.css", "styleInfo"),
  layers: requireClass(styles.layers, "InstanceRow.module.css", "layers"),
  layer: requireClass(styles.layer, "InstanceRow.module.css", "layer"),
  unconfigured: requireClass(styles.unconfigured, "InstanceRow.module.css", "unconfigured"),
  actions: requireClass(styles.actions, "InstanceRow.module.css", "actions"),
  testResult: requireClass(styles.testResult, "InstanceRow.module.css", "testResult"),
};

function styleInfoText(instance: InstanceEntry): string | null {
  if (instance.apiStyle)
    return instance.baseUrl ? `${instance.apiStyle} · base ${instance.baseUrl}` : instance.apiStyle;
  if (instance.baseUrl) return `base ${instance.baseUrl}`;
  return null;
}

export interface InstanceRowProps {
  instance: InstanceEntry;
  onSetApiKey: () => void;
  onOAuthStart: () => void;
  onEdit: () => void;
  onClear: () => void;
  onRemove: () => void;
  onSetDefault: () => void;
  onTestCredentials: () => void;
  testCredentialsPending?: boolean;
  testCredentialsResult?: AuthTestResponse;
}

export function InstanceRow({
  instance,
  onSetApiKey,
  onOAuthStart,
  onEdit,
  onClear,
  onRemove,
  onSetDefault,
  onTestCredentials,
  testCredentialsPending = false,
  testCredentialsResult,
}: InstanceRowProps) {
  const supportsApiKey = (instance.authModes ?? []).includes("apiKey");
  const supportsOAuth = (instance.authModes ?? []).includes("oauth");
  const showClear = instance.activeSource === "file" || instance.activeSource === "oauth";
  const layers = credentialLayers(instance);
  const unconfigured = unconfiguredLabel(instance);
  const styleInfo = styleInfoText(instance);
  const safeTestResult = testCredentialsResult
    ? safeCredentialTestResult(instance.name, testCredentialsResult)
    : undefined;

  return (
    <li className={CLASS.row}>
      <div className={CLASS.heading}>
        <StatusDot state={layers.length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
        <span className={CLASS.name}>{instance.name}</span>
        {instance.isDefault && <Chip>★ default</Chip>}
        {styleInfo && <span className={CLASS.styleInfo}>{styleInfo}</span>}
      </div>
      {unconfigured ? (
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
      <div className={CLASS.actions}>
        <Button variant="quiet" size="sm" onClick={onTestCredentials} disabled={testCredentialsPending}>
          {testCredentialsPending ? "Testing credentials…" : "Test credentials"}
        </Button>
        {supportsApiKey && (
          <Button variant="quiet" size="sm" onClick={onSetApiKey}>
            {instance.hasStoredFile ? "Replace key" : "Set key"}
          </Button>
        )}
        {supportsOAuth && (
          <Button variant="quiet" size="sm" onClick={onOAuthStart}>
            {instance.hasStoredOAuth ? "Refresh OAuth" : "Sign in…"}
          </Button>
        )}
        {showClear && (
          <Button variant="danger" size="sm" onClick={onClear}>
            Clear
          </Button>
        )}
        <Button variant="quiet" size="sm" onClick={onEdit}>
          Edit
        </Button>
        <Button variant="danger" size="sm" onClick={onRemove}>
          Remove
        </Button>
        {!instance.isDefault && (
          <Button variant="quiet" size="sm" onClick={onSetDefault}>
            ★ make default
          </Button>
        )}
      </div>
      {safeTestResult && (
        <p className={CLASS.testResult} role="status">
          {safeTestResult.status}: {safeCredentialTestMessage(safeTestResult.status)}
        </p>
      )}
    </li>
  );
}
