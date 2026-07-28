# Security Policy

The organization-wide
[security policy](https://github.com/fullstack-ai-infra/.github/blob/main/SECURITY.md)
defines confidential reporting, coordinated disclosure, and safe-research
requirements. This file adds only the versions, scope, response target, and
private intake route specific to `mem`.

## Supported versions

Security fixes are applied to the current `main` branch. After stable releases
begin, the latest stable release will also receive security fixes.

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Latest stable release | Yes, once available |
| Older versions and prereleases | Best effort |

## Private reporting

Do not disclose a suspected vulnerability in a public issue, discussion, or
pull request. Use the
[`mem` private vulnerability reporting form](https://github.com/fullstack-ai-infra/mem/security/advisories/new).
If the form is unavailable, follow the confidential fallback in the
organization security policy and identify `fullstack-ai-infra/mem` as the
affected repository.

Maintainers aim to acknowledge a complete report within five business days.
This is a response target, not a guarantee.

## `mem` scope

In scope are vulnerabilities affecting the server, CLI, MCP adapter, Worker,
Web application, authorization boundaries, memory lifecycle, workspace
bundles, release artifacts, or secrets committed to this repository.

Dependency vulnerabilities that do not affect `mem` should be reported to the
upstream project.
