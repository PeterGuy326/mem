# Issue Triage

This document defines the initial issue taxonomy and the gate between a report
and implementation. Labels describe different dimensions; do not use severity
as a substitute for evidence or workflow status.

## Required lifecycle

```text
needs-triage → waiting-feedback ─┐
       │                         │
       └────────────────────→ ready → in-progress → PR review → closed
                                    ↘ blocked ↗
```

Every public change begins with an issue. A maintainer moves an issue to
`status:ready` only after it passes the readiness gate below. Security reports
follow the private process in `SECURITY.md`.

## Label taxonomy

Use exactly one type, exactly one status, at least one area, and the strongest
honest evidence level. Bugs also require exactly one severity.

The public issue forms are inherited from the organization and intentionally
do not own repository labels. During initial triage, a `mem` maintainer applies
the type, status, area, evidence, and—when applicable—severity labels defined
below.

### Type

| Label | Use |
| --- | --- |
| `type:bug` | Existing behavior is incorrect or has regressed |
| `type:feature` | New user-visible behavior or capability |
| `type:docs` | Documentation-only work |
| `type:maintenance` | Maintenance, refactoring, tests, infrastructure, or research |
| `type:question` | A usage or design question that needs a maintainer answer |

Change the type when new evidence shows the issue was misclassified; do not
stack several type labels.

Questions can move from `status:needs-triage` to
`status:waiting-feedback` and then close when answered; they need the full
development lifecycle only if the answer becomes a proposed repository change.

### Severity

Severity measures impact, not implementation urgency or effort. It is required
for bugs and incidents and is not normally applied to features or maintenance
tasks.

| Label | Definition | Typical examples |
| --- | --- | --- |
| `severity:s0-critical` | Critical, catastrophic, or broadly unsafe | Active exploitation, severe data loss, broad production outage |
| `severity:s1-high` | High impact with no practical workaround | Core capability unusable for many users, serious integrity failure |
| `severity:s2-medium` | Meaningful but limited impact, or a workaround exists | Component failure in a bounded configuration |
| `severity:s3-low` | Low or cosmetic impact | Minor UI defect, confusing but non-destructive behavior |

Suspected security vulnerabilities must be moved to private reporting before
details are triaged. Maintainers may raise or lower severity as impact becomes
clear. Severity does not promise a delivery date.

### Evidence

Evidence is progressive. Update the label as claims become independently
verifiable; never select a higher level based only on confidence.

| Label | Definition | Minimum evidence |
| --- | --- | --- |
| `evidence:e0-unreviewed` | New intake, not assessed | The issue has not completed initial triage |
| `evidence:e1-report` | A reviewable report exists | Clear claim plus sanitized logs, screenshots, traces, examples, or user reports |
| `evidence:e2-source` | Source-level support exists | Code, configuration, specification, or trace identifies the relevant path or likely cause |
| `evidence:e3-reproduced` | Independently reproduced | Deterministic steps and environment reproduce the behavior |
| `evidence:e4-e2e` | Resolution independently verified | A non-author verifies the acceptance criteria end to end, including the original failure when applicable |

Feature and maintenance issues can begin at E0. For them, E2–E4 describe
source-level support for the limitation, independent reproduction, and
end-to-end confirmation of the delivered outcome.

Organization issue forms collect the reporter's supporting evidence. A
maintainer assigns E0–E2 during triage, E3 after someone independently
reproduces the claim, and E4 only after a non-author verifies the delivered
outcome end to end.

### Status

| Label | Meaning |
| --- | --- |
| `status:needs-triage` | Classification or scope has not been confirmed |
| `status:ready` | Acceptance criteria and scope are approved for implementation |
| `status:in-progress` | An assignee is actively implementing the issue |
| `status:waiting-feedback` | Progress needs a specific response from the reporter, maintainer, or reviewer |
| `status:blocked` | Progress depends on a named issue, decision, or external event |

Closing an issue is the terminal state; no additional closed-status label is
needed. PR review is represented by the linked pull request, not another issue
status label.

### Area

Start with the smallest accurate set:

- `area:server`
- `area:worker`
- `area:web`
- `area:cli`
- `area:mcp`
- `area:docs`
- `area:infra`

Multiple area labels are acceptable only when work genuinely crosses
components. Add a new area label only when recurring ownership or routing needs
justify it.

## Readiness gate

Before applying `status:ready`, a maintainer confirms:

- the issue is not a duplicate and belongs in this repository;
- sensitive or security details are not exposed publicly;
- type, area, status, and evidence labels are accurate;
- bugs have a defensible severity and expected versus actual behavior;
- the problem or desired outcome is clear;
- acceptance criteria are objective and testable;
- scope and explicit non-goals are recorded;
- dependencies, compatibility constraints, and material risks are identified;
- a validation approach exists; and
- the issue is small enough for a reviewable PR, or it has been split into
  linked issues.

If information is missing, apply `status:waiting-feedback` and ask concrete
questions. If the work should not proceed, close it with a short rationale
rather than leaving it indefinitely in triage.

## Assignment and development

When implementation begins:

1. Assign an owner and set `status:in-progress`.
2. Link the branch or draft PR to the issue.
3. Keep decisions and acceptance-criteria changes in the issue.
4. If scope materially changes, return the issue to `status:needs-triage` or
   split follow-up work instead of silently expanding the PR.
5. When the PR is review-ready, link it from the issue; keep
   `status:in-progress` until the PR closes the issue.

The PR should use `Closes #<number>` when it completes the issue. Use
`Refs #<number>` when it is one independently mergeable part of a larger issue.

## Independent verification and closure

The reviewer must be someone other than the sole author. They should reproduce
the material claims in the PR validation ledger, verify acceptance criteria,
and update evidence to E4 only when the original failure or desired outcome was
independently checked.

After required checks pass, approval is current, and conversations are
resolved, a maintainer squash-merges the PR. Close duplicates by linking the
canonical issue; close declined work with a clear rationale.
