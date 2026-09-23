<!-- TEMPLATE (warehouse-harness-template v1): fill in for THIS repo. -->
# REST API (inbound adapter)

List every route -> use case mapping, kept in sync with `apis/openapi.yaml`
(the `docs-api-drift` CI job fails if generated docs disagree with this
file's own spec, but this file itself is documentation for an agent, not
generated -- keep it manually accurate).

- `<METHOD> <path>`    -> `<UseCaseName>`
- ...

## Conventions

- Error shape: <RFC 7807 problem+json? custom envelope? state it>.
- Auth: <this fleet's fleet-wide REST+MCP auth was deliberately reverted
  2026-09-11 and is unauthenticated pending a fresh decision -- state
  whichever is true for this repo, and keep
  `internal/architecture/fitness_test.go`'s TestNoAuthMiddlewareReintroduced
  in sync if that ever changes>.
