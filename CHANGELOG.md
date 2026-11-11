# Changelog

## Unreleased

IMPROVEMENTS

* [#245](https://github.com/mocachain/moca-storage-provider/pull/245)  chore(build): bump the Go toolchain to 1.26.11 (security fixes to cgo, the go command, the compiler, the linker and net/http) across the go.mod toolchain directive (the go directive stays go 1.23.6), the Dockerfile (now golang:1.26.11-trixie for both stages, matching the trixie-based Dockerfile.release, #213), the README requirement and the CI workflow go-version pins; goreleaser-cross has no v1.26.11 image yet, so the release targets keep goreleaser-cross:v1.26.10 and set GOTOOLCHAIN=go1.26.11 inside it. Backport of #244
* [#213](https://github.com/mocachain/moca-storage-provider/pull/213)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#205](https://github.com/mocachain/moca-storage-provider/pull/205)  chore(build): bump the Go toolchain to 1.26.10 across the go.mod toolchain directive, the Dockerfile (now golang:1.26.10-bookworm), the goreleaser-cross release builds (pinned via GOTOOLCHAIN), the CI workflow go-version pins; also bumps github.com/bytedance/sonic to v1.15.1 (+ loader v0.5.1), required to compile under Go 1.26. The go directive stays go 1.23.6

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
