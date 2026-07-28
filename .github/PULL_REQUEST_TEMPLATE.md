## Outcome

<!-- What user or Agent outcome does this change enable? -->

## Contract and migration

<!-- Note API/CLI/MCP/schema changes, compatibility and rollback. -->

## Security and privacy

<!-- Describe workspace/path authorization, provenance, secrets and data impact. -->

## Validation

- [ ] Relevant unit/integration tests added or updated
- [ ] API, CLI and MCP contracts remain aligned
- [ ] Idempotent replay and conflict behavior is covered when applicable
- [ ] Partial retrieval warnings are handled when recall changes
- [ ] Scope, workspace and literal path boundaries are covered
- [ ] `cd server && go test ./... && go vet ./...`
- [ ] Worker tests run when affected
- [ ] Web lint/build run when affected
- [ ] Public behavior documented and `CHANGELOG.md` updated
- [ ] No credentials, personal data or generated runtime state committed
