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

## CURRENT STATE: persisted, deployed, stub-only

This repo holds the `NetworkOrder` aggregate, the ACL translation
boundary, the two use cases (`ReceiveNetworkDemand`,
`SweepAcknowledgementDeadlines`), in-memory AND Postgres repositories, the
stub network gateway, a Dockerfile, a Helm chart, and the `netfulfil`
composition root. It is deployed to the kind cluster.

Persistence is opt-in by `DATABASE_URL`: unset means the in-memory repo
(zero-config local run, hermetic unit suite), set means Postgres or a
REFUSAL to boot — never a silent fallback, because the fallback costs the
acknowledgement deadlines this context owes the network.

**Startup is RETRIED (~31s budget) and gated by a `startupProbe`.** Both
are required by this cluster, not defensive padding: every injected pod's
first outbound dial is reset ~10s after the app starts (Istio native
sidecars, so `holdApplicationUntilProxyStarts` is a no-op), and this
service runs migrations before it starts listening. Without the retry that
reset is fatal; without the probe the kubelet SIGTERMs a pod that is still
booting (exit 143, which reads like an app crash and is not one). Both
were observed live as CrashLoopBackOff. The retry does not weaken
fail-closed — after the budget it still refuses to boot.

The inbound leg is WIRED: a poller (`internal/adapters/inbound/poller`)
drives `ReceiveNetworkDemand` on an interval, and a read-only REST surface
(`internal/adapters/inbound/http`, contract in `apis/openapi.yaml`)
exposes what we told the network plus whether the poller is alive.
Previously `ReceiveNetworkDemand` was constructed and DISCARDED
(`_ = receive`), so nothing deployed could create a NetworkOrder at all.

**The REST surface is read-only on purpose.** Demand arrives by polling
and by nothing else (ADR 0001 section 5), so an HTTP intake endpoint would
be a second, fictional inbound path; the only thing it could honestly do
is inject test demand, which `NETWORK_SEED_FILE` does declaratively
instead. Do not add a write endpoint here without changing that record.

Two curated files, both following the fleet's `PATH_CATALOGUE_FILE`
precedent: `PRODUCT_TRANSLATION_FILE` (the ACL dictionary — **without it
every order is rejected as untranslatable**, and the service logs a
warning at startup saying so) and `NETWORK_SEED_FILE` (stub demand, which
refuses to boot if set against a non-stub gateway).

There is still no `web/` and no live network call anywhere. Read
`docs/adr/0001-network-fulfillment-bounded-context.md` before writing any
code here — the boundary is **Accepted (2026-09-23)** and is a companion
to `order-management` ADR 0020. Neither is meaningful without the other.

**CI is ACTIVE** (`.github/workflows/ci.yml`), with eight jobs: `lint`,
`test`, `integration`, `api-lint`, `mutation-fast`, `vuln`, `arch-test`,
`helm-lint` (`helm lint` plus the chart wiring tests in
`charts/network-fulfillment/tests/`).
These are exactly the jobs whose surfaces exist in this repo today.
`integration` runs the Postgres adapter against a real Postgres started by
testcontainers INSIDE the test — never a `DATABASE_URL` service container
with a skip gate, which would report success while asserting nothing. The
template's remaining jobs were **dropped, not disabled**: `bdd` (no
`features/`), `docs-api-drift` (no docs site), `web` (no `web/`), and
`trivy-scan`/`docker-publish`/`release`/`drift`. The `Dockerfile` and chart
now exist, but no image is published and there is no `main` branch yet.
Add each job back in the PR that creates or first needs its surface. A
job that fails because a directory is missing is noise, not signal.

**Branch protection on `develop` must now require the eight active
contexts** with `strict: true`:

```
lint  test  integration  api-lint  mutation-fast  vuln  arch-test  helm-lint
```

The earlier deferral (protection live with `required_status_checks: null`,
because requiring contexts that could never report would have made every
PR permanently unmergeable) is now resolved: the contexts report, so they
are required. Add `bdd` when a `features/` directory lands.

`.gremlins.yaml` thresholds are MEASURED, not copied: `efficacy: 99`,
`mutant-coverage: 92`, set strictly below a real `gremlins unleash
./internal/domain` run of this repo's own code (100.00% efficacy, 93.33%
mutant coverage, 14 killed / 0 lived / 1 not covered). Re-measure and
re-set them when the domain grows; never copy a sibling's numbers.

`.claude/rules/*.md` describe the code as it is: `domain-model.md`
(NetworkOrder, use cases, ports), `rest-api.md` (the four read-only
routes, RFC 7807), and `integration-events.md` (no Kafka yet; the rules
for when it arrives). Keep them in sync with the code. Do not write planned
concepts into them as if they exist.

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

## Architecture (NON-NEGOTIABLE — identical shape to the fleet)

Hexagonal / Ports & Adapters, enforced by the `arch-go` fitness tests in
`internal/architecture/`. Strict dependency rule: **domain depends on
nothing; application depends on domain; adapters depend on
application/domain.**

```
cmd/netfulfil/          composition root (the only binary)
internal/
  domain/
    networkorder/       NetworkOrder aggregate, acknowledgement invariants
    shared/             NetworkRef, NetworkLineRef, NetworkProductId, SKU,
                        LocalOrderId, SiteId, validation errors
  application/
    contract/           InboundDemand, HeldOrderRequest/Result
    ports/              NetworkOrderRepo, NetworkGateway, FulfillmentPlanner,
                        ProductTranslation, EventPublisher, Clock
    usecases/           ReceiveNetworkDemand, SweepAcknowledgementDeadlines
  adapters/
    inbound/http/       read-only REST (apis/openapi.yaml)
    inbound/poller/     the inbound leg: polls the gateway, feeds Receive...
    outbound/network/   NETWORK_MODE switch + stub gateway + seed file — the
                        ONLY place network vocabulary may exist
    outbound/ordermanagement/  held order, release, cancel (OM REST)
    outbound/postgres/  NetworkOrderRepo + migrations runner
    outbound/memory/    in-memory repo + product-translation file loader
  architecture/         arch-go + fleet fitness tests
migrations/             0001_init (network_orders, network_order_lines)
charts/network-fulfillment/   Helm chart + Python wiring tests
```

Still planned (ADR 0001), not in the tree: `CapabilityOffer` and its
Kafka-fed caches, `outbound/kafka`, a credentialed `sandbox`/`live`
gateway, shipment confirmation, transaction-status reconciliation, and an
MCP adapter.

`MUTATION_FAST_PKG` is `./internal/domain/networkorder`, the only aggregate.

## Hard rules for this context

1. **The network's vocabulary stops at `adapters/outbound/network/`**
   (today the stub gateway; the credentialed SP-API client will live
   there too).
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
make check-all      # check + coverage (90% gate) + arch-test + bdd (no features/ yet)
make arch-test      # arch-go hexagonal + fleet fitness tests
make integration    # Postgres via testcontainers — never a skip-gated check
make mutation       # gremlins on MUTATION_FAST_PKG (CI job: mutation-fast)
make mutation-full  # measure real thresholds before editing .gremlins.yaml
make vuln           # govulncheck ./...
lefthook install    # pre-commit fmt/lint/vet, pre-push make check
```

## Git workflow

GitFlow: `feature/*` branches off `develop`, PR into `develop`
(`gh pr create --base develop`); `develop` promotes to `main` for release.
This repo has never been released: there is no `main` branch yet.
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

Note: `warehouse-infra` does **not** auto-discover repos. This context is
deployed by its own `terraform/network-fulfillment.tf`, not through
`local.services`. It gets its own database Secret (`network-fulfillment-db`)
and a Kong route at `/api/network-fulfillment`. That file leaves
`config.networkMode` at the chart's `stub` default on purpose.
