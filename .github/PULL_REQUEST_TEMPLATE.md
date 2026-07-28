## Tracking issue

<!-- Use `Closes` when this PR completes the issue; use `Refs` for one part of a larger issue. -->

Closes #

## Summary

<!-- What changed, why this approach, and what is intentionally out of scope? -->

## Acceptance criteria

<!-- Copy the issue's acceptance criteria and link each one to implementation or evidence. -->

| Acceptance criterion | Status | Implementation / evidence |
| --- | --- | --- |
|  |  |  |

## Validation ledger

<!-- Evidence may be a concise output excerpt, CI job URL, screenshot, trace, or test path. Never include secrets or personal data. -->

| Command or check | Expected | Actual | Evidence |
| --- | --- | --- | --- |
|  |  |  |  |

## Tests and coverage

- Tests added or changed:
- Coverage before / after:
- Intentionally uncovered behavior and reason:

## Change classification

- [ ] User-visible behavior
- [ ] Internal refactor or maintenance
- [ ] Documentation only
- [ ] Build, CI, dependency, or packaging
- [ ] Breaking change
- [ ] Security-sensitive change

## Risk and rollback

- Risk level and affected components:
- Compatibility, migration, privacy, performance, or operational impact:
- Rollback procedure:

## Breaking or security notes

<!-- Write "None" when neither applies. Do not disclose an embargoed vulnerability in a public PR. -->

## Author checklist

- [ ] The linked issue was `status:ready` before implementation began.
- [ ] This branch was created from an up-to-date `main` and contains no unrelated changes.
- [ ] I ran the applicable tests, lint, type checks, builds, and coverage checks.
- [ ] I added a regression test for a bug fix, or explained why one is impractical.
- [ ] I updated documentation and `CHANGELOG.md` under `[Unreleased]` when applicable.
- [ ] I reviewed the diff for secrets, private data, generated artifacts, and dependency risk.
- [ ] The PR is ready for independent review and all reported results are reproducible.

## mem-specific contract checks

<!-- Check the applicable items; write "N/A" with a reason for the rest. -->

- [ ] API, CLI, and MCP behavior remains aligned with the canonical server semantics.
- [ ] Workspace, authorization, literal-path, provenance, and secret boundaries are covered.
- [ ] Idempotent replay and conflict behavior is covered where state can be retried.
- [ ] Partial-retrieval warnings remain explicit when recall behavior changes.
- [ ] Database migrations preserve existing data and include a disposable `_test` database path.

## Reviewer notes

<!-- Areas that deserve extra scrutiny, and any validation the reviewer should reproduce independently. -->
