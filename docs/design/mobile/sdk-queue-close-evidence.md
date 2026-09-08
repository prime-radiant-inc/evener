# SDK queue and close producer evidence

The retained fixture `/tmp/evener-sdk-producers-retry2-5qFnjm` provides scoped
real AppWire evidence for `thread/queueChanged`. An observer subscribed to the
target ref before an active scripted-provider turn was held. A real
`turn/queue` mutation returned an acknowledgment, canonical `thread/read`
showed the queued entry, and the raw observer stream contained
`thread/queueChanged`. The provider was released and the active turn completed.

The evidence is recorded in
[`sdk-queue-close-receipt.json`](assets/sdk-queue-close-receipt.json), with
hashes for the raw event stream, queue acknowledgment/readback, final read,
and provider evidence. The packaged SDK comparison covered all 139 tarball
regular files byte-for-byte against the installed consumer.

The same journey does not qualify `thread/closed`: after an empty
`thread/shutdown` acknowledgment, the observer remained subscribed through a
bounded 30-second wait and received no matching event. This is an evidence
gap; it does not establish that the producer is unsupported or identify a
backend defect. No `thread/closed` count has been added.
