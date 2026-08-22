/**
 * Server switcher sheet — lists multiple redacted profiles with reachability
 * state and actions to switch, add, edit (rename), re-pair, or remove.
 *
 * Switching clears server-scoped placeholder state and closes the sheet.
 * Removing the active profile requires confirmation and falls back to the next
 * saved profile or returns to onboarding. Server identity is always name +
 * full origin (including scheme) + status — never a credential.
 *
 * Re-pair uses `previewRepair` → origin confirmation → `confirmPairing` for the
 * same profile, preserving the old profile on failure. Rename preserves the
 * draft and shows inline errors.
 */
import { type ChangeEvent, type JSX, useState } from "react";
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

type RowMode = "view" | "rename" | "repair" | "confirm-remove";

export function ServerSwitcherSheet({
  connection,
  onSwitch,
  onAdd,
  onClose,
}: ServerSwitcherSheetProps): JSX.Element {
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const reachability = connection((s) => s.reachability);
  const preview = connection((s) => s.preview);
  const previewError = connection((s) => s.previewError);
  const [mode, setMode] = useState<{ id: string; mode: RowMode } | null>(null);
  const [editName, setEditName] = useState("");
  const [renameError, setRenameError] = useState<string | null>(null);
  const [repairUrl, setRepairUrl] = useState("");
  const [repairError, setRepairError] = useState<string | null>(null);
  const [pendingRepair, setPendingRepair] = useState(false);

  const handleSwitch = async (id: string) => {
    await connection.getState().switchTo(id);
    onSwitch();
    onClose();
  };

  const handleRemove = async (id: string) => {
    await connection.getState().remove(id);
    setMode(null);
    onClose();
  };

  const handleRename = async (id: string) => {
    const name = editName.trim();
    if (name === "") return;
    setRenameError(null);
    try {
      await connection.getState().rename(id, name);
      setMode(null);
      setEditName("");
    } catch {
      setRenameError("server name must be unique");
    }
  };

  const handleRepairPreview = async (id: string) => {
    const raw = repairUrl;
    setRepairUrl(""); // clear transient input immediately
    setRepairError(null);
    setPendingRepair(true);
    try {
      await connection.getState().previewRepair({ profileId: id, raw });
    } catch {
      setRepairError("preview failed");
    } finally {
      setPendingRepair(false);
    }
  };

  const handleRepairConfirm = async (id: string) => {
    if (preview === null) return;
    setRepairError(null);
    setPendingRepair(true);
    try {
      const repairName =
        editName.trim() || (profiles.find((p) => p.id === id)?.name ?? "");
      await connection
        .getState()
        .rePair(id, preview.previewId, repairName, false);
      setMode(null);
      setRepairUrl("");
      setEditName("");
    } catch {
      setRepairError("re-pair failed — old server is unchanged");
    } finally {
      setPendingRepair(false);
    }
  };

  const statusFor = (id: string): StatusKind => {
    const r = reachability[id];
    if (r === "reachable") return "reachable";
    if (r === "reconnecting") return "reconnecting";
    if (r === "unreachable") return "offline";
    // Unknown — show a neutral loading/unknown state, not fabricated Connected.
    return "reconnecting";
  };

  return (
    <div>
      <div className="evener-list-group">
        {profiles.map((p: ProfileRedacted) => {
          const isActive = p.id === activeProfileId;
          const rowMode = mode?.id === p.id ? mode.mode : "view";
          return (
            <div key={p.id}>
              {rowMode === "rename" ? (
                <div className="evener-form-row">
                  <Input
                    label="Server name"
                    name={`rename-${p.id}`}
                    value={editName}
                    onChange={(e: ChangeEvent<HTMLInputElement>) =>
                      setEditName(e.target.value)
                    }
                    placeholder={p.name}
                  />
                  {renameError !== null ? (
                    <p role="alert">{renameError}</p>
                  ) : null}
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
                        setMode(null);
                        setEditName("");
                        setRenameError(null);
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : rowMode === "repair" ? (
                <div className="evener-form-row">
                  <p>
                    Re-pair <strong>{p.name}</strong> ({p.origin}). The old
                    server stays usable until the new credential is confirmed.
                  </p>
                  {preview !== null ? (
                    <>
                      <p>
                        Confirm new connection to{" "}
                        <strong>{preview.origin}</strong>
                      </p>
                      {preview.isPrivateNetwork ? (
                        <p role="alert">HTTP origin — private network only.</p>
                      ) : null}
                      <Input
                        label="Server name"
                        name={`repair-name-${p.id}`}
                        value={editName}
                        onChange={(e: ChangeEvent<HTMLInputElement>) =>
                          setEditName(e.target.value)
                        }
                        placeholder={p.name}
                      />
                      <div style={{ display: "flex", gap: "8px" }}>
                        <Button
                          variant="primary"
                          onClick={() => handleRepairConfirm(p.id)}
                          disabled={pendingRepair}
                        >
                          Confirm Re-pair
                        </Button>
                        <Button
                          variant="tertiary"
                          onClick={() => {
                            setMode(null);
                            void connection.getState().cancelPreview();
                            setRepairUrl("");
                            setRepairError(null);
                          }}
                        >
                          Cancel
                        </Button>
                      </div>
                    </>
                  ) : (
                    <>
                      <Input
                        label="Paste new authorization URL"
                        name={`repair-url-${p.id}`}
                        value={repairUrl}
                        onChange={(e: ChangeEvent<HTMLInputElement>) =>
                          setRepairUrl(e.target.value)
                        }
                        autoCapitalize="off"
                        autoCorrect="off"
                        spellCheck={false}
                        placeholder="https://hub.example.com/auth?token=…"
                      />
                      <div style={{ display: "flex", gap: "8px" }}>
                        <Button
                          variant="secondary"
                          onClick={() => handleRepairPreview(p.id)}
                          disabled={repairUrl.trim() === "" || pendingRepair}
                        >
                          Preview
                        </Button>
                        <Button
                          variant="tertiary"
                          onClick={() => {
                            setMode(null);
                            setRepairUrl("");
                            setRepairError(null);
                          }}
                        >
                          Cancel
                        </Button>
                      </div>
                    </>
                  )}
                  {previewError !== null ? (
                    <p role="alert">{previewError}</p>
                  ) : null}
                  {repairError !== null ? (
                    <p role="alert">{repairError}</p>
                  ) : null}
                </div>
              ) : rowMode === "confirm-remove" ? (
                <div className="evener-form-row">
                  <p role="alert">
                    Remove &ldquo;{p.name}&rdquo;?{" "}
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
                    <Button variant="tertiary" onClick={() => setMode(null)}>
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
                    <span className="evener-list-row__subtitle">
                      {p.origin}
                    </span>
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
                      setMode({ id: p.id, mode: "rename" });
                      setEditName(p.name);
                      setRenameError(null);
                    }}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="tertiary"
                    aria-label={`re-pair ${p.name}`}
                    onClick={() => {
                      setMode({ id: p.id, mode: "repair" });
                      setEditName(p.name);
                      setRepairUrl("");
                      setRepairError(null);
                      void connection.getState().cancelPreview();
                    }}
                  >
                    Re-pair
                  </Button>
                  <Button
                    variant="tertiary"
                    aria-label={`remove ${p.name}`}
                    onClick={() =>
                      setMode({ id: p.id, mode: "confirm-remove" })
                    }
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
