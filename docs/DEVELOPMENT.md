# Developing mem

This document contains rules that are specific to the `mem` product and code
base. The active organization-wide contribution lifecycle, issue and pull
request forms, review rules, conduct policy, and support defaults come from
[`fullstack-ai-infra/.github`](https://github.com/fullstack-ai-infra/.github).
This repository owns only `mem`-specific product, validation, security,
ownership, triage, and release rules. Do not copy organization defaults back
into this repository: GitHub treats local community files as whole-file or
whole-directory overrides rather than composing them with organization
defaults.

## Product boundary

`mem` is a user-owned Memory Plane and portable drive for Agents. It stores,
consolidates and recalls evidence. The calling Agent owns planning, tool use,
reasoning and final answers.

A change belongs in this repository when it materially improves at least one
of these capabilities:

- durable files, memories, provenance or relationships;
- evidence-backed recall and search;
- task checkpoint, handoff or resume;
- workspace export, validation, import or recovery;
- user visibility, authorization or lifecycle control; or
- an adapter that exposes those canonical capabilities to an Agent.

Generic Agent runtimes, chat products, model-hosting gateways, unrelated
connectors and organization-wide governance do not belong in `mem`.

## Canonical contracts

The HTTP server owns the canonical semantics. CLI, MCP and Web adapters must
delegate to those semantics rather than implement a second memory model.

When a public contract changes:

1. update `SPEC.md` and the relevant schema or ADR;
2. keep API, typed client, CLI, MCP and Web behavior aligned;
3. document compatibility, security and privacy impact;
4. add the narrowest regression test that observes the changed behavior; and
5. add a user-visible entry under `CHANGELOG.md` → `[Unreleased]`.

The product must keep model-independent memory operations usable without a
cloud model, local LLM, embedding service or VLM. Models may enrich indexing;
they must not become a hidden requirement for `remember`, lexical memory
recall, lifecycle control, checkpoint or deterministic resume.

## Database changes

Migrations under `server/internal/db/migrations/` are append-only after
release. A migration must:

- preserve existing user data or explicitly document why that is impossible;
- have a credible rollback or fail-closed downgrade story;
- migrate a fresh PostgreSQL database successfully;
- be exercised against a disposable database whose name ends in `_test`; and
- update export/import schemas when portable data changes.

Never point integration tests at a development or production database.

## Pull-request contract evidence

In addition to the organization pull request template, every `mem` change must
record evidence for each applicable product contract:

- API, CLI, MCP, and Web behavior delegates to the canonical server semantics;
- workspace, authorization, literal-path, provenance, and secret boundaries
  remain covered;
- idempotent replay and conflict behavior is covered wherever state can be
  retried;
- partial-retrieval warnings remain explicit when recall behavior changes; and
- database migrations preserve existing data and use an explicitly disposable
  database whose name ends in `_test`.

Use `N/A` with a reason when a contract is genuinely unaffected. The
repository's issue labels and area taxonomy remain local; organization issue
forms do not apply them automatically, so maintainers assign them during
initial triage.

## Required validation

The exact environment, commands, expected results and cleanup or retained
artifact locations are in [TESTING.md](TESTING.md). A review-ready pull request
must copy the relevant rows into its validation ledger and report skipped or
unavailable checks as `NOT VERIFIED`, never as passed.

At minimum, a change must pass the tests for every affected deployable unit:

- Go server, CLI and MCP;
- Python indexing Worker;
- React Web application; and
- PostgreSQL integration tests when storage, authorization, migration,
  memory, handoff or workspace-transfer behavior changes.

Repository-specific baseline CI and Agent-memory lifecycle gates are defined
under `.github/workflows/`. CI, CODEOWNERS, release automation and
required-check configuration remain owned by this repository; they are not
community-health defaults.
