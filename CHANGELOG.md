# Changelog

All notable changes to computah are documented here. Release notes are generated
from conventional commits by GoReleaser; this file tracks higher-level history.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Changed
- Restructured from a single flat `package main` into focused `internal/`
  packages: core, agent, md, tui, web, tools_ext, plus a cmd command tree.
- Added a Cobra/Viper CLI surface (`run`, `exec`, `eval`, `dashboard`, `version`).

### Added
- Production repo scaffolding: MIT license, CI (build/test/vet/lint), GoReleaser
  release pipeline with multi-arch binaries and a GHCR container image,
  dependabot, and a Makefile.
