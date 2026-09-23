<!-- TEMPLATE (warehouse-harness-template v1): fill in for THIS repo, or
     delete this file if the repo publishes/consumes no Kafka events. -->
# Cross-service integration events (Kafka)

State whether this service PUBLISHES, CONSUMES, or both, and to/from which
topic(s) (`warehouse.<context>.events` is this fleet's naming convention).

## CloudEvents envelope

State the envelope shape this repo uses and the type-naming convention,
e.g. `com.warehouse.wms.<context>.<aggregate>.<PastTenseEvent>`.

## Consumer group id

If this service consumes Kafka: state where the consumer group id comes
from. It MUST be env-configurable, never a hardcoded string literal --
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces this (a real incident:
wes-work-planning's hardcoded group id let a locally-run e2e-tests process
silently collide with the live in-cluster Deployment's consumer group on
the shared fleet Kafka broker).

If this consumer replays from FirstOffset on every start to build an
in-memory read model (rather than resuming from a committed offset), the
group id must additionally be UNIQUE PER PROCESS INSTANCE (hostname+PID+
timestamp), not just configurable -- see HARNESS.md's Kafka section for
why a shared group breaks that pattern specifically.
