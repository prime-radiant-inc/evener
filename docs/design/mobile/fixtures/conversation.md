## Keep the work in progress

The reconnect path now preserves **your draft**, the selected hub, and any message whose delivery is uncertain. A new connection must never turn an unanswered request into permission to send it twice.

- Save composition under the exact hub and session.
- Keep the original text while you write a follow-up.
- Clear a delivery checkpoint only after the hub confirms acceptance.

> A visible response and a confirmed request are different facts. Recovery should explain what is known.

### The checkpoint

```typescript
const destination = { hubId: "studio", sessionRef: "local/session-42" };
await drafts.checkpoint(destination, { text: "Keep this exact message, including a deliberately long line that must remain readable and copyable on a narrow phone." });
const receipt = await hub.send(destination, input);
await drafts.confirm(destination, receipt);
```

The identifiers above are illustrative. Keep `clientMutationId` stable for the operation, and keep **newer input** separate from recovery text.

### Verification

| Situation | What you see | What remains saved |
| --- | --- | --- |
| App restarts | The draft is restored | Exact composition |
| Acknowledgement is missing | Delivery unconfirmed | Submitted text and newer draft |
| Another hub uses the same session reference | Its own conversation | Independent draft |

- [x] Cold-launch checks on iOS and Android
- [x] No automatic replay
- [ ] Physical-device performance measurements

The checkboxes report the conversation's state; they are not controls for changing server work.

Read the [mobile style guide](http://m5.local:8766/style-guide.md) or inspect the [hub source path](/Users/jesse/git/prime-radiant/evener/mobile-native/src/draftDocument.ts:1).

### A small equation

For a series of measurements, the mean is $\bar{x}=\frac{1}{n}\sum_{i=1}^{n}x_i$.

---

This reference conversation exercises native reading and copying. It makes no claim that the illustrated code is an Evener API.
