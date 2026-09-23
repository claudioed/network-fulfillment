<!-- TEMPLATE (warehouse-harness-template v1): fill in for THIS repo. -->
# Domain model: ubiquitous language, aggregates, events, use cases

## Ubiquitous Language (use these exact names)

- **<Aggregate>** — <one-sentence definition and the core invariant it
  protects>.
- <...more terms as needed. Keep every term's definition to 1-3 lines;
  this file is read on every task, so it must stay skimmable.>

## Aggregates

- **<Aggregate>** (`internal/domain/<pkg>`): <invariants it enforces,
  what makes it an aggregate boundary rather than a plain value object>.

## Domain events

- `<EventName>` — <when it's raised, what state change it represents>.

## Key use cases (`internal/application/usecases`)

- `<UseCaseName>` — <one line: what it orchestrates, which port(s) it
  calls>.
