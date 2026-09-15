// Package sshconn owns the controller hub's SSH channels to remote hosts that
// run their own hub, per docs/superpowers/specs/2026-09-14-multi-host-04-ssh-connection-manager.md.
//
// It is the only place in the hub that runs ssh. For one configured host it:
//
//   - preflights the host non-interactively (uname -> GOOS/GOARCH, HOME/XDG
//     roots, and the host binary's launch-check protocol/version/launch_flags);
//   - spawns `ssh <opts> -- <dest> <evener_path> hub attach --stdio` and wraps
//     the child's stdin/stdout in an appwire.StreamTransport, exposing an
//     initialized appwire.Client;
//   - supervises the channel, reconnecting with bounded exponential backoff when
//     the link drops, and never starting a hub on the host.
//
// This is the 04a slice: channel + preflight + lifecycle. Deploy, version
// auto-match, and hub restart are 04b and deliberately absent here.
//
// It does not map a channel onto an appsource.Source (component 05) or render
// hosts (component 06).
package sshconn
