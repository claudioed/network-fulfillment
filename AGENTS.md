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

## CURRENT STATE: Phase 3 skeleton, stub-only

This repo now holds the Phase 3 skeleton: the `NetworkOrder` aggregate,
the ACL translation boundary, the two use cases (`ReceiveNetworkDemand`,
`SweepAcknowledgementDeadlines`), in-memory and stub adapters, and the
`netfulfil` composition root. There is still no `apis/`, no chart, no
`web/`, no Postgres adapter and no live network call anywhere. Read
`docs/adr/0001-network-fulfillment-bounded-context.md` before writing any
code here — the boundary is **Accepted (2026-09-23)** and is a companion
to `order-management` ADR 0020. Neither is meaningful without the other.

**CI is ACTIVE** (`.github/workflows/ci.yml`), with five jobs: `lint`,
`test`, `mutation-fast`, `vuln`, `arch-test`. These are exactly the jobs
whose surfaces exist in this repo today. The template's other jobs —
`bdd`, `integration`, `api-lint`, `docs-api-drift`, `helm-lint`, `web`,
`trivy-scan`, `docker-publish`, `release`, `drift` — were **dropped, not
disabled**, because there is no `features/`, `apis/`, `charts/`, `web/`,
`migrations/` or Dockerfile yet. Add each job back in the PR that creates
its surface; a job that fails for want of a directory is noise, not
signal.

**Branch protection on `develop` must now require the five active
contexts** with `strict: true`:

```
lint  test  mutation-fast  vuln  arch-test
```

The earlier deferral (protection live with `required_status_checks: null`,
because requiring contexts that could never report would have made every
PR permanently unmergeable) is now resolved: the contexts report, so they
are required. Add the remaining four of `process-path-management`'s eight
(`bdd`, `integration`, `api-lint`) as each surface lands.

`.gremlins.yaml` thresholds are MEASURED, not copied: `efficacy: 99`,
`mutant-coverage: 92`, set strictly below a real `gremlins unleash
./internal/domain` run of this repo's own code (100.00% efficacy, 93.33%
mutant coverage, 14 killed / 0 lived / 1 not covered). Re-measure and
re-set them when the domain grows; never copy a sibling's numbers.

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
