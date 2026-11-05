# Changelog

## Unreleased

IMPROVEMENTS

* ci(e2e): start MySQL with `sudo systemctl start mysql.service` in the SP, Go-SDK, SP Exit and BlockSyncer e2e workflows; the Ubuntu 26.04 runner images ship MySQL as a disabled systemd unit and no longer have `/etc/init.d/mysql`, so every e2e job exited at the MySQL step
* ci(e2e): pin the chain, moca-cmd and moca-go-sdk refs of the SP, Go-SDK and SP Exit e2e jobs to full commit SHAs (checked before the chain starts), record the tested commits, local test patches, go.mod/go.sum digests, binary hashes and step results, upload those files and the logs on every run after scrubbing test keys and mnemonics from them, skipping the upload if the scrub fails (30-day retention, BlockSyncer now uploads its test output and coverage), and cap each e2e job at 60 minutes
* ci(e2e): build each e2e job's diagnostics upload from a static snapshot of a fixed allowlist: `--redactEvidence <profile>` copies only the files the upload step used to list (never SP `data/`/database WAL, config, keys or symlinks), exactly as they were when opened, scrubs and re-checks the copy and publishes `.github/e2e-diagnostics/` with a manifest of captured, missing and failed files only when all of it verifies (20 MiB per file, 100 MiB and 120 s in total, fails closed: a symlinked or non-directory `.github` is refused before anything is removed, and so is a generated key source that is empty, not UTF-8, not in the expected mnemonic or hex key form, or an SP JSON without private key fields or with a private key field that is not a bare hex string of the length its file holds: 64 digits for the fields normalize_sp_private_keys pads, up to 64 otherwise); the scrub no longer fails with FileNotFoundError on a rotating SP database WAL and no longer edits the run tree in place
* ci(e2e): stop echoing the per-run test private key in the SP, Go-SDK and SP Exit e2e workflows' set-key step and in e2e_test.sh; the key is still passed through `$GITHUB_ENV` and imported by moca-cmd as before, and `key_log_fixture_test.py` checks both offline; `generate-private-key.js` replaces its plain-text print with `::add-mask::` commands for the `0x` and bare hex forms, sent before the step output is written, which the fixture checks from the recorded calls with a stub ethers (whether the runner applies the masks to step headers, env blocks and the output of moca-cmd, localup and go test is only observable on a real run)
* ci(e2e): pin github.com/bytedance/sonic v1.15.1 and sonic/loader v0.5.1 in the e2e harness's moca-go-sdk and moca-cmd checkouts (each module graph on its own, selected versions checked and recorded along with a graph hash that does not depend on the runner's workspace path, and the build stops if the pin moves any module outside sonic, loader and the dependencies listed below) so they compile with Go 1.26.10; the pinned 1.2.x refs require sonic v1.14.0, which does not build on Go 1.26. This also selects bytedance/gopkg v0.1.3 and cloudwego/base64x v0.1.6 and drops cloudwego/iasm, knz/go-libedit and nullprogram.com/x/optparse in those test checkouts; the SP go.mod is unchanged
* [#213](https://github.com/mocachain/moca-storage-provider/pull/213)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#205](https://github.com/mocachain/moca-storage-provider/pull/205)  chore(build): bump the Go toolchain to 1.26.10 across the go.mod toolchain directive, the Dockerfile (now golang:1.26.10-bookworm), the goreleaser-cross release builds (pinned via GOTOOLCHAIN), the CI workflow go-version pins; also bumps github.com/bytedance/sonic to v1.15.1 (+ loader v0.5.1), required to compile under Go 1.26. The go directive stays go 1.23.6

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
