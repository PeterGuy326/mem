# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) for releases.

## [Unreleased]

### Added

- Issue-first contribution, triage, review, security, and community governance
  standards.
- Pull request policy and CI jobs for Go, the Python worker, and the Web
  application.
- Go and Python coverage artifacts plus verified Go, Python, and Web build
  artifacts.
- Checked-in Go and Python protobuf stubs for reproducible fresh-clone builds.

### Changed

- Rewrote the project README around the current experimental implementation
  and verifiable development commands.
- Applied `gofmt` to the existing Go baseline so formatting can be enforced in
  CI.

### Fixed

- Made `same_person` ranking use directional source-person coverage and stable
  file-ID tie-breaking so a full person-set match ranks ahead of a partial
  match and equal candidates are selected deterministically.

[Unreleased]: https://github.com/fullstack-ai-infra/mem/commits/main
