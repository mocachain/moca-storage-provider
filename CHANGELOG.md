# Changelog

## Unreleased

IMPROVEMENTS

* ci(e2e): start MySQL with `sudo systemctl start mysql.service` in the SP, Go-SDK, SP Exit and BlockSyncer e2e workflows; the Ubuntu 26.04 runner images ship MySQL as a disabled systemd unit and no longer have `/etc/init.d/mysql`, so every e2e job exited at the MySQL step
* ci(e2e): pin the chain, moca-cmd and moca-go-sdk refs of the SP, Go-SDK and SP Exit e2e jobs to full commit SHAs (checked before the chain starts), record the tested commits, local test patches, go.mod/go.sum digests, binary hashes and step results, upload those files and the logs on every run after scrubbing test keys and mnemonics from them, skipping the upload if the scrub fails (30-day retention, BlockSyncer now uploads its test output and coverage), and cap each e2e job at 60 minutes
* ci(e2e): build each e2e job's diagnostics upload from a static snapshot of a fixed allowlist: `--redactEvidence <profile>` copies only the files the upload step used to list (never SP `data/`/database WAL, config, keys or symlinks), exactly as they were when opened, scrubs and re-checks the copy and publishes `.github/e2e-diagnostics/` with a manifest of captured, missing and failed files only when all of it verifies (20 MiB per file, 100 MiB and 120 s in total, fails closed); the scrub no longer fails with FileNotFoundError on a rotating SP database WAL and no longer edits the run tree in place
* [#213](https://github.com/mocachain/moca-storage-provider/pull/213)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#205](https://github.com/mocachain/moca-storage-provider/pull/205)  chore(build): bump the Go toolchain to 1.26.10 across the go.mod toolchain directive, the Dockerfile (now golang:1.26.10-bookworm), the goreleaser-cross release builds (pinned via GOTOOLCHAIN), the CI workflow go-version pins; also bumps github.com/bytedance/sonic to v1.15.1 (+ loader v0.5.1), required to compile under Go 1.26. The go directive stays go 1.23.6

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
