# Changelog

## Unreleased

IMPROVEMENTS

* [#213](https://github.com/mocachain/moca-storage-provider/pull/213)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#205](https://github.com/mocachain/moca-storage-provider/pull/205)  chore(build): bump the Go toolchain to 1.26.10 across the go.mod toolchain directive, the Dockerfile (now golang:1.26.10-bookworm), the goreleaser-cross release builds (pinned via GOTOOLCHAIN), the CI workflow go-version pins; also bumps github.com/bytedance/sonic to v1.15.1 (+ loader v0.5.1), required to compile under Go 1.26. The go directive stays go 1.23.6

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
