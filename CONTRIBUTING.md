# Contributing to mem

Thanks for helping build a user-owned Memory Plane for Agents. Contributions
should preserve the product boundary: mem stores, consolidates and recalls
evidence; the calling Agent owns planning, reasoning and final answers.

## Before coding

- Search existing issues and discussions before opening overlapping work.
- For a public contract, migration or architectural change, describe the
  compatibility and security impact first. Add an ADR under `docs/adr/` when
  the decision will constrain future implementations.
- Keep credentials, personal data and generated runtime state out of commits.

## Development workflow

1. Branch from the current default branch.
2. Keep each change focused and use the repository's conventional commit style,
   for example `feat(memory): ...` or `fix(auth): ...`.
3. Add tests at the narrowest useful level, then run the relevant full suite.
4. Update the specification, user documentation and `CHANGELOG.md` when a
   public behavior changes.
5. Open a pull request; do not push feature work directly to the default branch.

Database migrations are append-only after release. New migrations must have a
safe rollback story, preserve existing user data and include a fresh-database
test. API, CLI and MCP adapters must share the server's canonical semantics
rather than creating parallel implementations.

## Verification

Run the suites affected by your change:

```bash
cd server
go test ./...
go vet ./...

cd ../worker
uv run pytest

cd ../web
npm run lint
npm run build
```

Integration tests that use PostgreSQL require an explicitly disposable database
whose name ends in `_test`; never point them at a development or production
database.

## Pull requests

A pull request should explain the user outcome, contract changes, security or
privacy impact, migration/rollback plan and validation performed. Keep
format-only or unrelated cleanup out of functional patches so reviewers can
audit behavior.

By contributing, you agree that your contribution is licensed under the
repository's [Apache-2.0 License](LICENSE).
