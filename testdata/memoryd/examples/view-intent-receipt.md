# View, Intent, Receipt syntax

This is a non-executable, pattern-neutral notation example. It is never read by
a runner or oracle. Angle-bracketed values are placeholders, not valid handles.

```text
View
  current: <optional bounded responsibility>
  targets: <offered target aliases>
  allowed_intents: <closed consequence shapes>
  artifacts/references: <opaque offered handles>

Intent
  kind: <bounded open semantic label>
  payload: <bounded semantic content>
  consequence: <one consequence offered by this View>
  authority handles: <only exact handles offered by this View>

Receipt
  outcome: accepted | rejected
  replayed: <response metadata only>
```

Semantic labels remain open. Identity, time, digests, fences, operation keys,
and accepted state remain machine-owned. An accepted Receipt says that the
closed effect committed; it does not say that the semantic content is true.
