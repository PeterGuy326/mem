# Security Policy

## Supported versions

Security fixes are applied to the current `main` branch. After stable releases
begin, the latest stable release will also receive security fixes. Older
commits, forks, and prereleases are supported on a best-effort basis only.

| Version | Supported |
| --- | --- |
| `main` | Yes |
| Latest stable release | Yes, once available |
| Older versions and prereleases | Best effort |

## Report a vulnerability privately

Do not open a public issue, discussion, or pull request for a suspected
vulnerability.

Use GitHub's
[private vulnerability reporting form](https://github.com/fullstack-ai-infra/mem/security/advisories/new).
If that form is unavailable, contact an organization owner through their
GitHub profile and ask for a private reporting channel without including
vulnerability details in the public message.

Include, when available:

- affected versions, commits, and components;
- impact and a realistic attack scenario;
- minimal reproduction steps or a proof of concept;
- required configuration or privileges;
- suggested mitigation; and
- whether anyone else has received the report.

Remove credentials, personal data, and third-party secrets from all evidence.
Use test accounts and the least destructive proof necessary.

## What to expect

Maintainers aim to acknowledge a complete report within five business days.
They will validate the report, agree on severity and disclosure timing, and
provide status updates while a fix is in progress. These are response targets,
not guarantees.

Please allow a reasonable remediation window before disclosure. Maintainers
will credit reporters who request credit, unless legal, privacy, or safety
constraints prevent it.

## Scope and safe research

Good-faith research must:

- avoid accessing or changing data that is not yours;
- avoid service disruption, persistence, social engineering, and destructive
  testing;
- stop after demonstrating the minimum evidence required; and
- comply with applicable law.

Dependency vulnerabilities that do not affect `mem` should be reported to the
upstream project. Reports about secrets accidentally committed to this
repository are in scope and should be submitted privately immediately.
