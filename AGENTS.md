# Network Fulfillment

harness-template: v1

Supporting bounded context: the anti-corruption layer between the
`warehouse-systems` fleet and an external retail fulfillment network
(Amazon's Selling Partner API, Vendor Direct Fulfillment). **Conformist**
to the network upstream, **Anti-Corruption Layer** for everything
downstream of it in this fleet. Owns **NetworkOrder** and
**CapabilityOffer** as first-class aggregates.

Study project — not a production system, not affiliated with Amazon or any
company (see README.md banner).

## CURRENT STATE: design stage, no implementation

This repo contains **one ADR, the fleet harness, and nothing else**. There
is no Go service code, no `cmd/`, no `apis/`, no chart, no `web/`. Read
`docs/adr/0001-network-fulfillment-bounded-context.md` before writing any
code here — the boundary is still Proposed and is a companion to
`order-management` ADR 0020. Neither is meaningful without the other.

**`.github/workflows/ci.yml` is deliberately still `ci.yml.template`.**
GitHub only executes files literally named `*.yml`/`*.yaml` under
`.github/workflows/`, so CI does not run here yet. This mirrors the
`warehouse-harness-template` repo's own decision for the same reason: a
repo with no Dockerfile, no `apis/openapi.yaml`, no charts and no `web/`
fails almost every job, and that red is meaningless noise rather than a
useful signal. **Rename it to `ci.yml` in the same PR that lands the first
real service code** (rollout Phase 3 in the ADR) — not before, not later.

Placeholders remaining by design: `.gremlins.yaml`'s
`{{MEASURED_EFFICACY_MINUS_1}}` / `{{MEASURED_MUTANT_COVERAGE_MINUS_1}}`.
These MUST be measured against real code via `make mutation-full` and set
strictly below the measured numbers — never copied from a sibling repo.

`.claude/rules/*.md` are the template's structural skeletons with
`<!-- fill in -->` markers. Fill them in for real once the domain exists;
do not fabricate domain content into them ahead of the code.

## Why this context exists

`process-path-management` owns per-path `cycleTimeP95` and a site-scoped
CPT schedule; `wes-work-planning` publishes remaining capacity per
`(path, CPT)`; `order-management` derives a real delivery promise from
both. Offering that capability to an external retail network turns out
**not** to be a matter of publishing it:

> **No Selling Partner API operation accepts a declaration of capability.**
> Cutoffs and lead times are Vendor Central configuration, not an API
> surface.

Capability reaches a network only through three *consequence* signals —
inventory update, order acknowledgement, shipment confirmation — and the
network infers our capability from the gap between the last two. So the
domain decision is **what to advertise and what to commit to**.

The one genuinely new concept here is **throughput-constrained advertised
availability**:

```
advertisedQuantity = min(
    physicalAvailable,                       inventory-storage
    throughputFeasibleBefore(nextCutoff)     PPM cycleTimeP95 + CPTSchedule
                                             × WES remaining PathCapacity
)
```

Everything else in this context is plumbing that makes that deliverable
and honest.

## Planned architecture (NON-NEGOTIABLE — identical shape to the fleet)

Hexagonal / Ports & Adapters, enforced by the `arch-go` fitness test in
`internal/architecture/` (already present and passing trivially on the
empty tree). Strict dependency rule: **domain depends on nothing;
application depends on domain; adapters depend on application/domain.**

```
internal/
  domain/
    networkorder/       NetworkOrder aggregate, acknowledgement invariants
    capabilityoffer/    CapabilityOffer, advertised-availability basis
    shared/             NetworkRef, value objects, domain events
  application/
    ports/              outbound interfaces (network gateway, OM client,
                        inventory/schedule/capacity caches)
    usecases/           IngestNetworkDemand, AcknowledgeNetworkOrder,
                        RecomputeCapabilityOffer, ConfirmShipment
  adapters/
    inbound/            poller, HTTP, (later) MCP
    outbound/spapi/     the ACL — the ONLY place network vocabulary exists
    outbound/kafka/     fleet integration events
    outbound/postgres/  persistence
```

`MUTATION_FAST_PKG` is set to `./internal/domain/networkorder` — create
that package or update the Makefile when the real richest aggregate is
known.

## Hard rules for this context

1. **The network's vocabulary stops at `adapters/outbound/spapi/`.**
   `purchaseOrderNumber`, `itemSequenceNumber`, `buyerProductIdentifier`
   (ASIN), `acknowledgementStatus` codes, `sellingParty`/`shipFromParty`,
   `ShippingSpeedCategory` must never appear in `internal/domain` or in
   anything published to the fleet. `arch-go` cannot catch a *vocabulary*
   leak — that check is human.
2. **Customer PII stops here.** Ship-to name/address/phone live in this
   context and nowhere else in the fleet. `order-management` receives SKUs,
   quantities and a deadline. This makes this repo the first in the fleet
   that genuinely cannot run unauthenticated.
3. **Never recompute promise math here.** Deadline feasibility is asked of
   `order-management`'s `PromisePolicy.FeasibleBy` (its ADR 0020). This
   context has the deadline and *could* consume the same caches — doing so
   guarantees the two copies drift.
4. **Correlation is a persisted mapping, never a string convention.**
   `NetworkOrder.localOrderId` is stored explicitly. The fleet already got
   burned once: FE's `order_ref` carries WES's per-LINE `WorkUnitId`, not
   OM's `OrderId` (OM ADR 0018).
5. **Network demand is ship-complete.** The network confirms or rejects a
   purchase order in its entirety; partial acknowledgements are rejected.
   OM ADR 0017's per-shipment-group promising must not apply to it.
6. **`NETWORK_MODE=live|sandbox|stub`, default `stub`.** The kind cluster,
   `e2e-tests` and CI must never need a credential. Log the chosen mode at
   startup so the running value can be verified, not assumed — every
   `*_MODE` in this fleet defaults permissive and several sat wrong in the
   cluster for weeks.
7. **Any FirstOffset-replay Kafka cache needs a consumer group id unique
   per process instance** (hostname+PID+timestamp). A shared group means a
   fresh process resumes from an earlier instance's committed offset and
   is marked ready holding an empty cache. This has bitten this fleet in
   two separate services.
8. **Submissions are asynchronous.** `submitAcknowledgement` /
   `submitShipmentConfirmations` return accepted-for-processing; reconcile
   via `getTransactionStatus` (~15 min for acks, ~10 for shipments). A 200
   is not a completed commitment — model submitted-but-unreconciled as its
   own state.

## Key commands (harness v1 — see HARNESS.md for what each sensor costs)

```bash
make check          # fmt-check + vet + build + lint + test
make check-all      # check + coverage (90% gate) + arch-test + bdd
make arch-test      # arch-go hexagonal fitness tests  (passes today)
make integration    # testcontainers — never a skip-gated KAFKA_BROKERS check
make mutation-fast  # gremlins on MUTATION_FAST_PKG
make mutation-full  # measure real thresholds before editing .gremlins.yaml
make vuln           # govulncheck ./...
lefthook install    # pre-commit fmt/lint/vet, pre-push make check
```

## Git workflow

GitFlow: `feature/*` branches off `develop`, PR into `develop`
(`gh pr create --base develop`); `develop` promotes to `main` for release.
Do not merge your own PR — leave it open for independent review.

## Related decisions elsewhere in the fleet

| Repo | ADR | Why it matters here |
| --- | --- | --- |
| `order-management` | 0020 | The companion: `releaseOnAllocation` hold, `PromisePolicy.FeasibleBy`, `Network` promise basis |
| `order-management` | 0014 | The promise is a CPT window derived from fulfillment capability |
| `order-management` | 0004 | Release is the cancellation boundary — why network demand is held, not optimistically released |
| `order-management` | 0017 | Per-shipment-group promising — the rule network demand must *not* use |
| `process-path-management` | 0010 | The fulfillment capability contract this context advertises against |
| `fulfillment-execution` | 0025 | The sweep pattern the acknowledgement-deadline sweep mirrors |

Note: `warehouse-infra`'s `local.services` does **not** auto-discover
repos. This context needs an explicit entry there or it will never deploy,
no matter how complete its chart is.
