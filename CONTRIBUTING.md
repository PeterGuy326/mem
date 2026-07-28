# Contributing to mem

Thank you for helping build `mem`. This project accepts changes through a
traceable, issue-first workflow so that intent, implementation, and validation
remain connected.

By contributing, you agree that your contribution is licensed under the
[Apache License 2.0](LICENSE).

## Required change flow

Every change follows this sequence:

1. **Open or find an issue.** Search existing issues before creating a new one.
   Use a private security report instead of a public issue for vulnerabilities.
2. **Make the issue ready.** A maintainer confirms the problem or outcome,
   acceptance criteria, scope, labels, and `status:ready`.
3. **Create an independent branch.** Branch from the latest `main`; never
   develop directly on `main`.
4. **Implement the smallest complete change.** Keep unrelated refactors out of
   the branch and update tests, docs, and the changelog when applicable.
5. **Open a pull request.** Link the issue, complete the PR template, and
   include reproducible validation evidence.
6. **Pass CI and independent review.** The author cannot be the approving
   reviewer. Resolve all review conversations and rerun affected checks.
7. **Squash merge.** A maintainer squash-merges the approved PR and deletes the
   branch.

Do not start implementation while an issue is still being triaged. See
[Issue triage](docs/maintainers/triage.md) for the label taxonomy, readiness
gate, severity scale, and evidence levels.

## Product and data contract

Preserve the product boundary: `mem` stores, consolidates, and recalls
evidence; the calling Agent owns planning, reasoning, and final answers.

For a public contract, migration, or architectural change, describe
compatibility and security impact before implementation. Add an ADR under
`docs/adr/` when the decision will constrain future implementations.

Database migrations are append-only after release. New migrations need a safe
rollback story, must preserve existing user data, and should include a
fresh-database test. API, CLI, and MCP adapters must share the server's
canonical semantics rather than creating parallel implementations.

PostgreSQL integration tests require an explicitly disposable database whose
name ends in `_test`. Never point tests at a development or production
database.

## Branches and commits

Start from an up-to-date default branch:

```bash
git switch main
git pull --ff-only
git switch -c fix/123-short-description
```

Use `<type>/<issue>-<description>` for branch names. Common types are `feat`,
`fix`, `docs`, `test`, `refactor`, `build`, `ci`, and `chore`.

Use a concise conventional title for commits and pull requests:

```text
fix(server): reject expired access tokens
feat(cli): add JSON output for search
docs: explain local worker setup
```

Each pull request should address one primary issue. If a larger issue needs
several PRs, state the dependency order in the issue and keep every PR
independently reviewable.

## Development checks

Follow the setup instructions in the [README](README.md). Before requesting
review, run the checks relevant to the changed components.

```bash
# Build the three Go executables from the repository root.
make build
```

For server changes, run:

```bash
cd server
test -z "$(gofmt -l .)"
go vet ./...
go test -race -p 1 -coverpkg=./... -covermode=atomic ./...
```

For worker changes, run:

```bash
cd worker
uv sync --locked --extra test --extra dev
make proto
uv run pytest --cov=mem_worker --cov-report=term-missing
uv build
```

For web changes, run:

```bash
cd web
npm ci
npm run lint
npm run typecheck
npm run build
```

The worker has historical Ruff findings that are not yet a required check.
Do not add new violations in changed code, and record any cleanup separately
rather than hiding it inside an unrelated pull request.

Add or update tests for changed behavior. A bug fix should include a regression
test whenever practical. If a check cannot be run locally, explain why in the
PR validation ledger and point to the CI result that covers it.

Do not reduce coverage merely to make a check pass. A justified coverage
decrease must be explicit in the PR, including the uncovered behavior and a
follow-up plan.

## Pull requests

A review-ready PR must:

- link its primary issue with `Closes #<number>` (or `Refs #<number>` when the
  PR is only one part of the issue);
- map the implementation to the issue's acceptance criteria;
- record commands, expected results, actual results, and evidence;
- describe tests and the coverage impact;
- identify compatibility, security, migration, and operational risks;
- include a rollback plan;
- update `CHANGELOG.md` under `[Unreleased]` for user-visible changes; and
- contain no secrets, credentials, private data, generated caches, or unrelated
  edits.

Draft PRs are welcome for early feedback, but they are not eligible for merge.
Mark a PR ready only after the template is complete and local checks pass.

Reviewers independently validate the claims that matter for the change. An
approval from the sole author does not count. New commits after approval may
require another review.

## AI-assisted changes

The human submitter remains accountable for the design, licensing, security,
tests, and correctness of every contribution. Review all generated output
before submitting it and disclose material tool assistance when it helps a
reviewer assess risk.

Do not add an automated tool or model as a Git author or co-author. Tool
attribution, when useful, belongs in the pull request description rather than
commit authorship trailers.

## Reporting security and conduct concerns

- Report vulnerabilities through [SECURITY.md](SECURITY.md), never in a public
  issue.
- Report unacceptable community behavior through
  the organization-wide
  [Code of Conduct](https://github.com/fullstack-ai-infra/.github/blob/main/CODE_OF_CONDUCT.md).
