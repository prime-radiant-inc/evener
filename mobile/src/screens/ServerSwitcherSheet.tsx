/**
 * Server switcher sheet — lists multiple redacted profiles with reachability
 * state and actions to switch, add, edit (rename), re-pair, or remove.
 *
 * Switching clears server-scoped placeholder state and closes the sheet.
 * Removing the active profile requires confirmation and falls back to the next
 * saved profile or returns to onboarding. Server identity is always name +
 * host:port + status — never a credential.
 */
import { type JSX, useState } from "react";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { type StatusKind, StatusMark } from "../ui/StatusMark";
import type { ConnectionStore } from "./root-types";

export interface ServerSwitcherSheetProps {
  readonly connection: ConnectionStore;
  readonly onSwitch: () => void;
  readonly onAdd: () => void;
  readonly onClose: () => void;
}

export function ServerSwitcherSheet({
  connection,
  onSwitch,
  onAdd,
  onClose,
}: ServerSwitcherSheetProps): JSX.Element {
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const reachability = connection((s) => s.reachability);
  const [confirmingRemove, setConfirmingRemove] = useState<string | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState("");

  const handleSwitch = async (id: string) => {
    await connection.getState().switchTo(id);
    onSwitch();
    onClose();
  };

  const handleRemove = async (id: string) => {
    await connection.getState().remove(id);
    setConfirmingRemove(null);
    onClose();
  };

  const handleRename = async (id: string) => {
    const name = editName.trim();
    if (name === "") return;
    try {
      await connection.getState().rename(id, name);
    } catch {
      // redacted error — name must be unique
    }
    setEditingId(null);
    setEditName("");
  };

  const statusFor = (id: string): StatusKind => {
    const r = reachability[id];
    if (r === "reachable") return "reachable";
    if (r === "reconnecting") return "reconnecting";
    if (r === "unreachable") return "offline";
    return id === activeProfileId ? "reachable" : "offline";
  };

  return (
    <div>
      <div className="evener-list-group">
        {profiles.map((p: ProfileRedacted) => {
          const isActive = p.id === activeProfileId;
          const isConfirming = confirmingRemove === p.id;
          const isEditing = editingId === p.id;
          const host = hostFromOrigin(p.origin);
          return (
            <div key={p.id}>
              {isEditing ? (
                <div className="evener-form-row">
                  <Input
                    label="Server name"
                    name={`rename-${p.id}`}
                    value={editName}
                    onChange={(e) => setEditName(e.target.value)}
                    placeholder={p.name}
                  />
                  <div style={{ display: "flex", gap: "8px" }}>
                    <Button
                      variant="primary"
                      onClick={() => handleRename(p.id)}
                      disabled={editName.trim() === ""}
                    >
                      Save
                    </Button>
                    <Button
                      variant="tertiary"
                      onClick={() => {
                        setEditingId(null);
                        setEditName("");
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : isConfirming ? (
                <div className="evener-form-row">
                  <p role="alert">
                    Remove "{p.name}"?{" "}
                    {isActive
                      ? "Another saved server will be activated."
                      : "This cannot be undone."}
                  </p>
                  <div style={{ display: "flex", gap: "8px" }}>
                    <Button
                      variant="danger"
                      aria-label={`confirm remove ${p.name}`}
                      onClick={() => handleRemove(p.id)}
                    >
                      Confirm Remove
                    </Button>
                    <Button
                      variant="tertiary"
                      onClick={() => setConfirmingRemove(null)}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="evener-list-row" style={{ cursor: "default" }}>
                  <span className="evener-list-row__main">
                    <span className="evener-list-row__title">
                      {p.name}
                      {isActive ? " (active)" : ""}
                    </span>
                    <span className="evener-list-row__subtitle">{host}</span>
                  </span>
                  <StatusMark status={statusFor(p.id)} />
                  {!isActive ? (
                    <Button
                      variant="secondary"
                      aria-label={`switch ${p.name}`}
                      onClick={() => handleSwitch(p.id)}
                    >
                      Use
                    </Button>
                  ) : null}
                  <Button
                    variant="tertiary"
                    aria-label={`edit ${p.name}`}
                    onClick={() => {
                      setEditingId(p.id);
                      setEditName(p.name);
                    }}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="tertiary"
                    aria-label={`remove ${p.name}`}
                    onClick={() => setConfirmingRemove(p.id)}
                  >
                    Remove
                  </Button>
                </div>
              )}
            </div>
          );
        })}
      </div>
      <div style={{ padding: "16px" }}>
        <Button variant="primary" aria-label="add server" onClick={onAdd}>
          Add Server
        </Button>
      </div>
    </div>
  );
}

function hostFromOrigin(origin: string): string {
  try {
    const url = new URL(origin);
    const port = url.port ? `:${url.port}` : "";
    return `${url.hostname}${port}`;
  } catch {
    return origin;
  }
}
