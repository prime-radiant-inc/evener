# Remote session actions

A session on a connected host is addressed by a host-qualified ref such as
`m5max:<id>`. The controller routes reads and actions to the hub on that host;
the session itself runs on the owning machine.

## Effective capability

A client sees an action on a remote session only when both sides answer for it:

1. The remote session advertises the action for its own daemon.
2. The controller can forward the action to the host that owns the session.

`forwardedRemoteThreadCapabilities` in
[remote_hub_refs.go](../../cmd/evener-hub/internal/appsource/remote_hub_refs.go)
is the policy that combines those answers. It reads the remote hub's
`FeatureSet`; attachment already enforces the controller's exact AppWire
protocol version, so a compatible peer routes every v7 mutation the controller
forwards. An explicit Connect probes the host's capability snapshot; a read
that finds no snapshot uses the client's own initialize response, which carries
the same handshake facts.

The feature set gates the five actions it names: Send (`turn/start`), Steer
(`turn/steer`), Clear (`thread/clear`), ForkFromTurn (`thread/fork`), and
Shutdown (`thread/shutdown`). Interrupt, Compact, Queue, ChangeModel,
ChangeVisionModel, Rename, Goal, SharedNotes, SkillInput, CommandInput, and
StopSubagent follow the remote daemon's own capability claim because the
controller has an implemented forwarder and the compatible peer serves the
route. PageBefore stays masked: the controller cannot yet join a remote page to
its paging window.

The mask applies to `thread/list`, `thread/read`, `thread/start`,
`thread/resume`, `thread/fork`, `thread/clear`, and the
`thread/status/changed` notification, so a status frame cannot re-enable an
action the read path masked.

## Sending to a shut-down remote session

A remote session with no running daemon reports `notLoaded`. When its host
advertises Send, the controller advertises Send too, and the first message
resumes the session on its own host, the same as a local shut-down session.
The host's hub owns that lifecycle; the controller does not spawn a daemon on
the remote machine.

## Recovery

The host's hub owns session recovery. The controller reports the host
unavailable when the SSH channel is gone and re-reads the session after
reconnection. A forwarded mutation whose response is lost is reported as
outcome unknown rather than retried blindly, because the controller cannot tell
whether the host applied it.

## Related

- [Remote operations](../evener-hub-remote-operations.md): connecting to a
  remote hub, managed hosts, deployment, and troubleshooting.
- [Hub routing](subsystems.md): S06 owns source resolution and mutation
  forwarding; S07 owns host attachment and lifecycle.
