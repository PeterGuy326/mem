# Releasing mem

This document defines the initial release and packaging policy for `mem`.
Release automation, signing, and publishing to additional distribution
channels are intentionally deferred until the project has completed a stable
release workflow.

## Version policy

`mem` is a single-version monorepo. The Go service and clients, Python worker,
web application, schemas, and documentation ship under one
[Semantic Versioning](https://semver.org/) version:

- Stable release tag: `vMAJOR.MINOR.PATCH`
- Prerelease tag: `vMAJOR.MINOR.PATCH-rc.NUMBER`

Component-specific versions are not released independently. During the
experimental phase, breaking changes may be published under `v0.x.y`, but they
must still be documented.

An annotated Git tag and its GitHub Release are the canonical public release.
Do not move, delete, or reuse a published tag. Correct a bad release with a new
version.

## Current CI packages

The `CI` workflow builds verification artifacts for every pull request and
push to `main`:

| Artifact | Contents |
| --- | --- |
| `mem-go-linux-amd64-*` | `memd`, `mem`, `mem-mcp`, and `LICENSE` |
| `mem-worker-package-*` | Python wheel and source distribution |
| `mem-web-*` | Production web build |
| `go-coverage-*` | Go coverage profile and summary |
| `worker-coverage-*` | Worker coverage XML |

These artifacts are short-lived build evidence. They are not signed,
multi-platform release assets and must not be presented as a stable
distribution.

## Release preparation

Every release starts with a release issue and a dedicated release pull
request. The pull request must:

1. Select one SemVer version and document whether it is stable or a
   prerelease.
2. Move the relevant entries from `CHANGELOG.md`'s `Unreleased` section into a
   versioned section dated in `YYYY-MM-DD` format.
3. Synchronize every user-visible version field, including the Python worker,
   web package, MCP server metadata, and Go build version injection.
4. Regenerate affected lockfiles and generated metadata.
5. Record compatibility, migration, security, and rollback considerations.
6. Include reproducible validation evidence for all packages.

The release pull request may merge only after the protected `main` checks pass
and an independent reviewer approves it.

## Publication gate

Before creating a tag or GitHub Release, a maintainer must verify:

- The release pull request is merged into `main`.
- `CI`, `PR Policy`, and every required repository check passed for the exact
  release commit.
- The working tree is clean and the selected commit is contained in
  `origin/main`.
- The version does not already exist as a local tag, remote tag, or GitHub
  Release.
- Release notes match the versioned `CHANGELOG.md` section.
- Packages were rebuilt from the exact tag commit in a clean GitHub-hosted
  environment and their checksums were recorded.

The project does not yet have a repository-controlled release workflow that
can satisfy the final clean-build and publication gate. Until that workflow
lands, maintainers must not advertise CI artifacts as an official stable
release. The first publication workflow should require explicit approval,
create an annotated tag, build assets from that tag, emit checksums, and create
one GitHub Release.

## Deferred channels

The following are out of scope for the initial baseline:

- Automated release or version-bump pull requests
- PyPI publication
- npm publication
- Container registry publication
- Homebrew or other operating-system package managers
- Signed multi-platform binaries and provenance attestations

Each new channel requires its own issue, threat and rollback analysis,
credential design, and independently reviewable workflow. GitHub Release
remains the source of truth even after additional channels are added.
