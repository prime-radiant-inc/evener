// Package sshconn owns the controller hub's SSH channels to remote hosts that
// run their own hub. The [[hosts]] entries it connects to are defined in
// cmd/evener-hub/config.go (HostConfig), and the failure classes it reports are
// named in errors.go.
//
// It is the only place in the hub that runs ssh. For one configured host it:
//
//   - preflights the host non-interactively (uname -> GOOS/GOARCH, HOME/XDG
//     roots, and the host binary's launch-check protocol/version/launch_flags);
//   - spawns `ssh <opts> -- <dest> <evener_path> hub attach --stdio` and wraps
//     the child's stdin/stdout in an appwire.StreamTransport, exposing an
//     initialized appwire.Client;
//   - supervises the channel, reconnecting with bounded exponential backoff when
//     the link drops, without starting a stopped hub during background reconnect.
//
// A compatible live handshake preserves the host's executable and process,
// regardless of differing build labels or deployment-source availability. An
// explicit Connect can start a stopped compatible installation. A missing or
// incompatible installation requires a suitable deployment source. Explicit
// deployment and interrupted-effect recovery retain their requested build and
// process-identity checks before attachment is published.
//
// It does not map a channel onto an appsource.Source (component 05) or render
// hosts (component 06).
package sshconn
