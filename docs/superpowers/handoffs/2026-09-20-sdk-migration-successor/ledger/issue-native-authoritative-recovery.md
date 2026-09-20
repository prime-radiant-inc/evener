The planned native outbox host/recovery consumer must reconcile authoritative server state before replay after cold start or reconnect. The standalone runtime has no production host consumer yet; its ready-client discovery alone cannot unblock a blockedUnknown head and must not become the complete replay policy.

Acceptance: validate thread/read identity and client generation; reconcile confirmed mutation IDs before restoring proven-absent records; restore only with authoritative mutation state and no restart/not-loaded/resume gate. Preserve uncertainty when authority is unavailable, and never blindly replay a blocked record. Tests must show a blocked head recovering and later queued work advancing, plus cold-start/reconnect fencing.

Native recovery must enumerate durable recovery rows, including recovery-only targets, and retain data until edits/discard/resend settle durably. Keep hub+ref storage keys internal and raw conversation refs on the wire. This is the existing D25d-2/3/4 migration scope, not a new protocol. Notes producers remain behind #1938; #1937/#1957 stay separate.

Evidence: local RoboRev2606/2611 and the bounded web/native recovery-contract audit. The runtime/host stack remains held until concrete consumer slices satisfy this contract.
