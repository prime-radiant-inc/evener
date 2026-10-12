# Playdate client

The native C client lives in the separate
[evener-client-playdate repository](https://github.com/prime-radiant-inc/evener-client-playdate).
Its [product guide](https://github.com/prime-radiant-inc/evener-client-playdate/blob/main/docs/client.md)
owns navigation, chat rendering, reviewed voice input, saved drafts/audio and
native network recovery. Its [README](https://github.com/prime-radiant-inc/evener-client-playdate#readme)
owns build instructions, controls, limits and qualification commands.

Evener owns the existing [AppWire protocol](../appwire-protocol.md), session
authority and generic [device-pairing contract](../device-pairing.md). The same
invitations and approvals serve the native mobile app. The Playdate repository
implements that client contract; it adds no device-specific Hub endpoint.

The optional browser/C interoperability checks run from Evener with
`EVENER_PLAYDATE_CLIENT` set to the absolute path of the standalone checkout.
The ordinary Evener test suite does not require that checkout or Playdate SDK.
