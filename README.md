# Network Fulfillment

> **⚠️ Study project.** This repository is an educational exercise in
> Domain-Driven Design applied to warehouse management/execution systems. It
> follows real industry-standard patterns and terminology (WMS/WES/WCS,
> CloudEvents-like envelopes, RFC 7807, hexagonal architecture) but is
> **not a production system** and is **not affiliated with, endorsed by, or
> representative of Amazon, Manhattan Associates, Blue Yonder, or any other
> company**. References to the Amazon Selling Partner API describe a public
> API this exercise integrates against as a learning target.

The anti-corruption layer between the `warehouse-systems` fleet and an
external retail fulfillment network — a **Supporting Subdomain** bounded
context that is **Conformist** to the network upstream and an
**Anti-Corruption Layer** for everything downstream of it in this fleet.

## Status

**Design stage.** This repository currently contains one Architecture
Decision Record and no implementation. ADR 0001 is `Proposed` and is a
companion to `order-management` ADR 0020 — neither is meaningful without
the other, and both are accepted or rejected as one decision.

📄 [ADR 0001 — Network Fulfillment as a bounded context](docs/adr/0001-network-fulfillment-bounded-context.md)

## Why this context exists

The fleet models a building that knows what it can do: `process-path-management`
owns per-path `cycleTimeP95` and a site-scoped CPT (Critical Pull Time)
schedule, `wes-work-planning` publishes remaining capacity per `(path, CPT)`,
and `order-management` derives a real delivery promise from both.

Offering that capability to an external retail network turns out **not** to
be a matter of publishing it. No operation in the network's API accepts a
declaration of cutoffs or cycle times. Capability reaches a network only
through three signals, all of them consequences rather than declarations:

| Signal | Meaning |
| --- | --- |
| Inventory update | what we claim we can ship |
| Order acknowledgement | what we commit to — within 24h, whole order, fill-or-kill |
| Shipment confirmation | whether we actually did |

The network infers our capability from the gap between the second and the
third. So the interesting decision is **what to advertise and what to commit
to** — and this fleet already owns every fact needed to decide both well.

## The idea worth building

Most integrations of this shape advertise physical stock on hand. This fleet
can do better, because it knows whether the building can actually *move*
those units before the next departure:

```
advertisedQuantity = min(
    physicalAvailable,                       inventory-storage
    throughputFeasibleBefore(nextCutoff)     process-path-management
                                             cycleTimeP95 + CPTSchedule,
                                             × wes-work-planning remaining
                                               PathCapacity for that (path, CPT)
)
```

400 units in the building but a path that can only carry 120 more before the
18:00 cutoff means advertising 400 sells a promise the floor cannot keep.
Advertising 120 sells capability honestly.

## Boundary

- **Conformist upstream.** The network's vocabulary — purchase orders, line
  sequence numbers, product identifiers, acknowledgement codes, selling
  parties — stops at this context's adapter and is never adopted by the
  fleet.
- **This context holds the fleet's only customer PII** (ship-to name,
  address, phone) and its only external SLA (the 24-hour acknowledgement
  clock). Both are reasons it is a separate context rather than an adapter
  inside `order-management`.
- **Promise math is not duplicated here.** Deadline feasibility is asked of
  `order-management`'s existing `PromisePolicy`, per the companion ADR.

## Planned shape

```
NetworkOrder      networkRef, requiredShipBy, lines, localOrderId,
                  NEW -> ACKNOWLEDGED | REJECTED -> CONFIRMED
                  invariant: acknowledged in full or rejected in full

CapabilityOffer   (sku, siteId) -> advertisedQuantity + basis
                  Physical | ThroughputConstrained
                  invariant: advertisedQuantity <= physicalAvailable
```

`NETWORK_MODE=live|sandbox|stub` gates every outbound call, defaulting to
`stub`. The kind cluster, `e2e-tests` and CI run in `stub` and never need a
credential.

## Related decisions elsewhere in the fleet

| Repo | ADR | Why it matters here |
| --- | --- | --- |
| `order-management` | 0020 | The companion: `releaseOnAllocation` hold, `PromisePolicy.FeasibleBy`, `Network` promise basis |
| `order-management` | 0014 | The promise is a CPT window derived from fulfillment capability |
| `order-management` | 0004 | Release is the cancellation boundary — why network demand must be held, not optimistically released |
| `order-management` | 0017 | Per-shipment-group promising — the rule network demand must *not* use |
| `process-path-management` | 0010 | The fulfillment capability contract this context advertises against |
| `fulfillment-execution` | 0025 | The sweep pattern the acknowledgement-deadline sweep mirrors |

## Git workflow

GitFlow: `feature/*` branches off `develop`, PR into `develop`
(`gh pr create --base develop`); `develop` promotes to `main` for release.
