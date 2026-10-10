# Federated Domain Operations Case

This fixture exercises R7 federation and the View-driven evolution loop against
a running checkout system. Optional selection mechanisms remain outside this
non-binary incident. This is a real service world, not a transcript fixture:
requests cross HTTP service
boundaries, state changes in the services, and an independent probe judges the
result.

```text
                         checkout traffic
                                |
                                v
                         +-------------+
                         |   gateway   |       edge domain
                         +------+------+
                                |
                         selected region
                         +------+------+
                         |             |
                         v             v
                   +-----------+ +-----------+
                   | payment E | | payment W |   payment domain
                   +-----+-----+ +-----+-----+
                         |             |
                         v             v
                   +-----------+ +-----------+
                   | callback E| | callback W|   platform domain
                   +-----+-----+ +-----+-----+
                         |             |
                         +------+------+
                                v
                          +-----------+
                          |  ledger   |          data domain
                          +-----------+

                     +----------------+
                     | SLO monitor    |          lead domain
                     +----------------+
```

## Why the domains are separate

The five workspaces model teams that already exist for ordinary operational
reasons. Each has different local knowledge, credentials, and tools:

| Domain | Knows and owns |
| --- | --- |
| `lead` | End-to-end symptoms and a bounded public probe; no service configuration mutation |
| `edge` | Gateway routing, counters, and a bounded read-only request/receipt history |
| `payment` | Payment behavior and bounded payment configuration |
| `platform` | Callback delivery behavior and bounded callback configuration |
| `data` | Ledger captures and explicitly audited void operations |

Service administration is available only on the owning domain network. A
shared memoryd network carries governed collaboration Events; it does not grant
service credentials or merge the five Runtime contexts.

The lead may ask its existing monitor for one server-shaped synthetic checkout
at a time. The monitor chooses the identity, submits exactly one request through
the public gateway, and returns the exact gateway receipt plus aggregate ledger
observations before and after reconciliation. It serializes calls and enforces
global effect bounds; the Agent cannot select a route, request count, timeout,
retry policy, or repair. The monitor owns only its server-generated synthetic
identity: it preserves the exact capture acknowledged by a successful receipt,
explicitly voids its other captures, and verifies that postcondition before
returning. It never reconciles a production identity. This is an environmental
observation affordance, not service-administration authority.

The runner uses the same physical monitor for a different, closed purpose. It
first checks the historical incident tuple without side effects; only after that
tuple is repaired does it issue one server-named canary to decide whether the
current service path is safe. Runner canaries and Agent-chosen probes share the
same global bound and integrity audit, but Agent calls remain separately visible
in per-turn operation evidence. The aggregate `synthetic-*` count must therefore
not be interpreted as Agent behavior.

## What is deliberately not scripted

The service faults are created outside the Agent workspaces. Episode 1 starts
with the East path affected. After independent recovery checks pass, the runner
records a pre-consolidation sequence, offers one neutral attention opportunity,
then captures only References created or updated in that interval. It restores
the East path to a healthy baseline and injects the same fault family into the
West path. The second fault is not projected into any Agent workspace. The domain
documents describe stable architecture and authority only. They do not reveal
either active incident, prescribe a diagnosis, name an Event choreography,
choose which expert to contact first, or require one repair path.

Agents receive normal attention opportunities and may inspect their own domain,
change state within their own authority, or use whatever opaque Event labels
and collaboration structure fit the evidence. A remote Event is still a
candidate at the receiving memoryd; it is never remote authority.

Each episode passes on the same external outcomes: historical customer receipts
still point to one active capture, extra captures remain as explicit void audit
records, and two fresh evaluation batches complete without duplicate active
captures. Accepted collaboration effects retain their R7 causality and receipt.
Any optional `synthetic-*` checkout is audited separately: successful public
receipts retain exactly their acknowledged active capture, failed receipts leave
no active capture, and all other side effects remain explicit void records. Its
pre-reconciliation observation remains visible to the caller, while independent
fresh production traffic—not probe cleanup—decides whether the incident is fixed.
Probe attempts remain visible in the turn evidence, but do not retroactively
invalidate an otherwise valid Event. The Monitor rejects work beyond its global
128-probe physical budget before another checkout begins; this is the resource
authority, rather than a prompt convention or a post-effect per-turn predicate.
Agent and runner observations share that single budget; no hidden reserve exists,
and exhaustion is a closed scenario failure rather than an inferred success.
Pi runs with a fresh process and no session for every attention opportunity,
while the five memoryd authority stores and their References survive both
episodes. Before Episode 2, all five Runtime containers and writable workspaces
are replaced; only the immutable domain projection and captured memoryd
authority/CAS are restored. After Episode 1 passes its external outcome oracle,
only Lead gets one additional neutral attention opportunity. It receives no
diagnosis or instruction to publish; it can only re-observe its own now-updated
service and decide whether anything is worth retaining before the authority
boundary is captured.

The paid runner pins Pi 0.83.0 and defaults to DeepSeek V4 Flash with its
bounded `high` reasoning mode. `DOMAIN_OPS_PI_MODEL` and
`DOMAIN_OPS_PI_THINKING` may select another explicit cohort, but the runner
validates the model label and the closed Pi reasoning-level vocabulary before
starting any container. Reasoning remains Runtime-private and cannot alter the
View, Event, admission, or Receipt contract.

Every Runtime prompt also states the generic attention contract: one
opportunity does not own the whole workflow, may commit at most one accepted
contribution, and should stop so later turns can continue. This is a resource
boundary, not case choreography or a completion condition.

Evolution is an optional structural observation, not a scripted requirement.
If Episode 1 leaves an active Reference and Episode 2 claims to use, supersede,
or retract it, the oracle requires the exact head and accepted Event edge. Zero
Reference heads or zero later uses is valid and is reported as not applicable.
The oracle never inspects Reference bytes, Event kind, diagnosis wording, peer
order, or repair configuration, and it does not claim that a retained Reference
improved the later diagnosis or recovery.

The gateway retains only its 192 most recent completed request observations.
This edge-owned surface records the business ID, selected route, outcome, and
the capture ID actually returned to the caller. It does not expose downstream
attempts, infer a root cause, prescribe a repair, or grant ledger authority.

An open Handling left by Episode 1 remains eligible during Episode 2 and shares
that episode's 16-turn attention budget. This is deliberate pressure on durable
continuity, not a cohort scheduler: the runner does not inspect Event kind,
payload, or episode ancestry to prioritize work. A stale responsibility may
therefore cause a bounded budget failure, and the report preserves that result
instead of silently draining or cancelling it.

## Evidence interpretation

This fixture separately reports accepted peer observations, observations
projected into bounded Views, Event citations submitted by Agents, and the
independent service outcome. The first three prove protocol transport,
projection, and provenance; none proves that a model read, understood, or
considered every peer contribution. A rotated View fence proves issuance, not
model consumption, and `truncated` means only that the current View omitted
related evidence. It is not an unread-message counter or an all-peer barrier.

Agent summaries remain untrusted semantic content, and forwarding an Artifact
does not imply local adoption by its receiver. Therefore only the independent
service postcondition can close the global recovery claim. The current protocol
supports bounded fan-in reasoning with direct-outcome closure; exhaustive
review coverage would require a separate explicit capability and is not inferred
from Event causation or reply delivery.

## Fixture boundary

Files under `domains/` are projected into the corresponding Agent workspaces.
They may teach the Agent how to observe and safely operate its own domain. They
must remain independent of the incident seed. Removing these instructions must
not change memoryd Core, Event physics, or peer delivery.
