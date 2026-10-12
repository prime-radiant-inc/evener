# Device pairing

Evener Hubs are self-hosted. Pairing starts in an already authenticated Hub Web
UI; there is no global directory, rendezvous service, or shared pairing server.
The Hub owns invitations and approvals. Clients own their saved Hub profiles,
network identities and unsent work. This document is the wire contract; the
[Playdate guide](product/playdate-client.md) covers that client's behavior.

## Trust and transports

An invitation carries a Hub locator and a random short-lived secret. It carries
neither the Hub's durable credential nor Tailscale credentials. A Playdate hears
it as sound. The native mobile app reads the same bytes from a QR code. The Web
UI only plays audio and displays codes: it requires no camera, microphone, or
secure-context browser API, so it works on HTTP over a tailnet.

After receiving the invitation, the client generates an ephemeral X25519 key.
It independently computes and displays the comparison code below, then claims
the invitation at the advertised Hub. The authenticated Web UI lists claims and
requires the operator to choose the one whose code matches the device. **No
credential is released before approval.** Approval selects one public key for
the invitation and cannot be moved to another key. A different device needs a
new invitation. A party hearing the sound can submit a competing claim, but
cannot decrypt the selected device's result or silently replace that claim.

Closing setup cancels an untouched pending invitation. Once the operator submits
approval, closing the browser leaves the invitation retrievable until expiry:
an acknowledgement lost on the way back must not revoke a committed approval.
The explicit cancel method still revokes the named invitation.

The fingerprint authenticates the operator's choice; it is not an encryption
key. The encrypted response is bound to the selected X25519 key. HTTP over
Tailscale relies on the tailnet for transport authentication/encryption; a
phone uses its installed Tailscale connection and Playdate uses its native
control/DERP/WireGuard implementation. Tailscale enrollment remains its own
official authorization flow. Direct HTTPS uses platform certificate validation.
Do not infer trust from the sound itself: the operator compares the Hub shown
on the receiving device and the code before approving.

## Hub authority

Authenticated AppWire methods:

These methods act on the connected Hub's own invitations and credential. They
are not forwarded through the host administration proxy.

| Method | Parameters | Result |
| --- | --- | --- |
| `evener/devicePairing/create` | `origin`, `tailnet` | `invitation`, canonical `origin`, `tailnet`, `expiresAt` |
| `evener/devicePairing/read` | `invitation` | `state`, `claims`, optional `approvedKey` |
| `evener/devicePairing/approve` | `invitation`, exact `publicKey` | same status |
| `evener/devicePairing/cancel` | `invitation` | expired status |

`invitation` is 24 cryptographically random bytes encoded as 48 lowercase hex
characters. `expiresAt` is Unix milliseconds. Invitations expire ten minutes
after creation, including approved invitations. There are at most 16 live
invitations per Hub and eight distinct keys per invitation. Expired entries
are pruned on access. A Hub restart discards all pending invitations. Existing
saved client credentials remain valid.

The Hub supplies the pairing destination automatically. An optional
`mobile_base_url` supplies the default; otherwise the Hub uses the browser's
usable origin, a concrete network listener address, or an interface
address when listening on all interfaces. Interface selection prefers Tailscale
IPv4, then the OS's outbound IPv4/IPv6 addresses ahead of container bridges,
then other private and public addresses, respecting an IPv4-only listener.
The Hub queries outbound routes with UDP sockets without sending packets or
depending on a remote service. Loopback and link-local addresses are not advertised
by discovery. A loopback-only listener still works behind a reachable reverse
proxy when the browser supplies that proxy's origin.

The Web UI uses the discovered origin for both the direct credential QR and
the invitation form. The operator can edit the Hub address in **Pair a device**;
that explicit destination takes precedence when creating an invitation. An
empty creation origin uses the configured or discovered default. Ordinary
LAN/tailnet setup needs no configuration-file edit. Discovery is repeated when
the settings section connects; it does not change saved client destinations.

Creating an invitation requires a Hub credential of 1–511 non-space printable
ASCII bytes, matching the envelope receivers. Unsupported credentials are
reported before an invitation is issued; ordinary Hub authentication is unchanged.

`tailnet` marks a destination reached through Tailscale. A tailnet locator may
use MagicDNS. Both direct HTTP and HTTPS accept a reachable DNS or IP origin,
including IPv6; the Hub does not require DNS resolution during creation.
Loopback, unspecified listener addresses, and interface-scoped IPv6 addresses
cannot be pairing destinations.
Origins contain no credentials, path, query or
fragment. IPv4 uses canonical dotted-decimal spelling; malformed numeric hosts
and legacy numeric spellings are rejected before an invitation is created.
Port range is 1–65535. Clients report unsupported
transports locally: Playdate supports DNS/IPv4 and HTTP inside its native
tailnet stream; the native phone app also supports IPv6 and HTTPS over Tailscale.

A status has `state` equal to `pending`, `approved`, or `expired`. `claims` is
an array of `{publicKey, client, comparison}` in arrival order. Claimed device
labels are 1–48 printable ASCII bytes; they are not proof of identity. An
approved status names `approvedKey`. Unknown or cancelled invitations read as
expired. Repeating approval of the chosen key is idempotent; approving another
key after selection fails with an AppWire conflict.

## Device bootstrap HTTP

A device has no Hub token yet, so this exact route is outside the ordinary Hub
authentication guard:

```http
POST /api/device-pairing
Content-Type: application/json

{"invitation":"<48 lowercase hex>","publicKey":"<64 lowercase hex>","client":"My device"}
```

This route accepts at most 1024 request bytes within five seconds. An unfinished
body times out with HTTP 408 and closes the connection. It rejects unknown fields
or trailing JSON, requires JSON content type, rejects browser Origin headers, allows only
POST, and does not enable CORS. It is excluded from optional inbound request
recording so invitation capabilities cannot enter HTTP recordings. Other Hub
routes retain their authentication guard.

A valid request gets JSON with `state` and optionally `comparison` and
`envelope`. States:

| State | Meaning and client action |
| --- | --- |
| `pending` | Show independently computed comparison code; poll with the same key. |
| `approved` | Decrypt `envelope`; validate destination against invitation before saving. |
| `rejected` | Another key was approved; leave saved settings untouched and start again. |
| `expired` | Unknown, cancelled, restarted or expired invitation; listen/scan again. |

New malformed or low-order X25519 keys return 400. Claim capacity returns 429;
clients can retry an already registered key even at capacity. Repeated claims
with the same key recover the same comparison and, after approval, the exact
same ciphertext until expiry. Reading the result does not consume it: a lost
HTTP reply must not strand an approved client. Responses use `Cache-Control:
no-store`. Devices accept at most 4096 response bytes, cancel oversized replies
while reading, poll no faster than once per second, and bound request time;
network failures preserve the pending key for retry. Cancellation stops polling
and clears the ephemeral key; it never deletes a saved Hub or draft.

The code is SHA-512 over these concatenated bytes:

1. ASCII `evener-pairing-v1` (no terminator).
2. The 24 raw invitation bytes.
3. The client's 32 raw X25519 public key bytes.

Encode the first 16 digest bytes as 32 lowercase hexadecimal digits, grouped in
eight groups of four separated by hyphens, for example
`2be9-271d-6222-d455-d2d1-c217-62b2-3b82`. Display and compare every group. The
128-bit fingerprint resists offline searches for a key matching an observed
claim; a short decimal reduction does not. The Hub refuses approval when two
distinct claims have the same fingerprint, and the operator starts a new
invitation. Clients must not substitute a fingerprint supplied by a network
response for their own result.

## Invitation bytes (EVI1)

Integers are unsigned. The invitation is bounded by the acoustic modem's 1152
byte maximum. Strings are ASCII, without NUL terminators.

| Offset | Length | Meaning |
| --- | --- | --- |
| 0 | 4 | ASCII `EVI1` |
| 4 | 1 | flags: bit 0 tailnet, bit 1 TLS; other bits zero |
| 5 | 2 | port, big endian |
| 7 | 1 | host length |
| 8 | 1 | RPC path length |
| 9 | 24 | invitation secret |
| 33 | host length | lowercase DNS name, IPv4 address, or unbracketed IPv6 address |
| following | path length | `/rpc` |

Host length is 1–255. Current Hub endpoints are rooted at the origin; RPC path
is exactly `/rpc`. Tailnet and TLS flags may be combined. Reject surplus
bytes, unsupported flags, invalid host characters or an invalid port. QR text
is `EVENER-INVITE1:` followed by lowercase hex of the complete EVI1 bytes. QR
and sound therefore name exactly the same invitation and destination.

## Approved credential envelope (EVP1)

The HTTP response's `envelope` is lowercase hex of the following bytes. The
envelope is independent of the invitation delivery medium or receiving device.

| Offset | Length | Meaning |
| --- | --- | --- |
| 0 | 4 | ASCII `EVP1` |
| 4 | 8 | first eight invitation bytes |
| 12 | 32 | fresh sender X25519 public key |
| 44 | 24 | random NaCl box nonce |
| 68 | remaining | NaCl box ciphertext, including 16-byte authenticator |

Encryption is X25519/XSalsa20-Poly1305 (`nacl.box` / Go `nacl/box`), not a sealed
box with a derived nonce. Reject low-order sender keys and authentication
failure. The plaintext settings are:

| Offset | Length | Meaning |
| --- | --- | --- |
| 0 | 1 | version 1 |
| 1 | 1 | same flags as EVI1 |
| 2 | 2 | port, big endian |
| 4 | 1 | host length |
| 5 | 1 | path length |
| 6 | 2 | credential length, big endian |
| 8 | variable | host, path, credential in that order |

Credential is 1–511 printable non-space ASCII bytes. Host/path/flags/port must
match the invitation. The full envelope is at most 1152 bytes. Credentials stay
in memory until the receiving client's existing settings/secure-store commit;
neither invitation nor approval changes ownership of old saved work.

## Acoustic framing

Mono PCM is 44,100 samples/second. Each symbol is 441 samples (100 symbols/sec)
and selects 1500, 2100, 2700 or 3300 Hz for values 0–3. A Hamming(12,8) codeword
encodes each byte: parity bits at one-based positions 1,2,4,8; data in the other
positions, least significant bit first. Six symbols carry pairs of codeword
bits, least significant pair first.

Each frame starts with sixteen value-0 symbols, sixteen value-3 symbols, then
sync values `0,1,3,2,1,0,2,3`. A frame is 59 bytes: fragment index (1), total
message length (2, little endian), message CRC32 (4, little endian), payload
(48, zero-padded last fragment), frame CRC32 (4, little endian). CRC is reflected
IEEE CRC32 with polynomial 0xedb88320, initial/final XOR 0xffffffff; the frame
CRC covers its first 55 bytes. Twelve silent symbols separate frames. Complete
messages require every fragment and a matching message CRC. Replaying the same
invitation fills missing fragments; a different length or message CRC resets
the assembly. Frame checksums detect corruption, not malicious senders.

## Cross-client qualification

The Playdate implementation lives in
[evener-client-playdate](https://github.com/prime-radiant-inc/evener-client-playdate).
For the optional acoustic C/browser check and the full isolated-Hub pairing
check, set `EVENER_PLAYDATE_CLIENT` to that checkout's absolute path and
`PLAYDATE_SDK_PATH` to the SDK directory:

```sh
# From cmd/evener-hub/frontend:
node scripts/check-playdate-pairing.mjs

# From the Evener repository root:
EVENER_PAIRING_E2E=1 go test ./cmd/evener-hub -run TestDevicePairingBrowserRoundTrip -v
```

These checks use synthetic credentials and an isolated local Hub. Ordinary
Evener tests do not require the standalone client or Playdate SDK.
