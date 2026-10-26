# Changelog

## Unreleased

BUGFIXES

* [#217](https://github.com/mocachain/moca-storage-provider/pull/217)  fix(downloader): report the piece store read error on object download
* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  fix(blocksyncer): keep the block prefetch bounded after a restart so a block that repeatedly fails to export cannot exhaust memory

IMPROVEMENTS

* [#214](https://github.com/mocachain/moca-storage-provider/pull/214)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#209](https://github.com/mocachain/moca-storage-provider/pull/209)  chore(build): build the release binaries with goreleaser-cross:v1.26.10 directly now that the 1.26.10 cross image is published, dropping the temporary GOTOOLCHAIN override added in #201
* [#201](https://github.com/mocachain/moca-storage-provider/pull/201)  chore(build): bump the Go toolchain to 1.26.10 (latest 1.26 patch release) across the build Dockerfile, the CI workflow go-version pins, and the goreleaser release builds
* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  chore(blocksyncer): redact the password from the DSN startup log

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
