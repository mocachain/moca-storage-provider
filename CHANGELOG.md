# Changelog

## Unreleased

BUGFIXES

* [#233](https://github.com/mocachain/moca-storage-provider/pull/233)  fix(signer): every EVM transaction method now returns a signing failure (a call the node cannot estimate, or one estimated above its `*GasLimit`) at once with `failed to sign EVM tx: <reason>`, as DiscontinueBucket and UpdateSPPrice already did, instead of estimating the call three times back to back and reporting `failed to broadcast`; nonce refusals are still retried
* [#230](https://github.com/mocachain/moca-storage-provider/pull/230)  fix(gfspapp): stop within a bounded time on SIGTERM, SIGINT or SIGHUP. The gRPC server waited forever for open streams, so a stopped storage provider kept running until its supervisor sent SIGKILL (after 10s with Docker, 30s on Kubernetes, 90s with systemd). The gRPC stop now closes the remaining connections after 10s and waits at most 2s more for call handlers (one that ignores cancellation is left behind), the module stop is bounded at 30s (its timeout was 30 nanoseconds and never applied), the whole stop exits after 45s at the latest, and a second signal exits at once. The last log lines are flushed before exit, for at most 2s, so a stalled log writer cannot keep the process alive. docs/stopping.md lists the supervisor settings

* [#229](https://github.com/mocachain/moca-storage-provider/pull/229)  fix(signer): send every EVM transaction with the node's gas estimate plus `[Chain] EvmGasMarginPercent` (default 25%) instead of the fixed per-type `*GasLimit`; those settings become upper bounds (a transaction estimated above its bound is refused before sending) with defaults raised to about twice the gas measured on moca v2 (seal 1,000,000; the old defaults such as 1,200 for a seal were left over from fixed-gas pricing). A call the node cannot estimate, for example one that would revert, is no longer signed or broadcast, and every EVM method returns the node's reason (or the estimate and the bound) to the caller. Operators with an explicit `*GasLimit` below the measured usage (for example `SealGasLimit = 180000`) must raise or remove it. Requires a moca v2 chain that includes mocachain/moca#593 (cosmos/evm with the snapshotmulti fix; v2.0.0-rc0 does not have it), including the node in `[Chain] RpcAddress`: without it every seal fails to estimate and is refused, so upgrade the chain nodes first, then the gas-limit settings, then the SP (see docs/modules/signer.md). Pins moca main (b399ee03) and the matching mocachain/cosmos-evm replace
* [#220](https://github.com/mocachain/moca-storage-provider/pull/220)  fix(executor): log task info instead of the whole task after sealing
* [#219](https://github.com/mocachain/moca-storage-provider/pull/219)  fix(executor): record a secondary signature parse failure on the task
* [#217](https://github.com/mocachain/moca-storage-provider/pull/217)  fix(downloader): report the piece store read error on object download
* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  fix(blocksyncer): keep the block prefetch bounded after a restart so a block that repeatedly fails to export cannot exhaust memory

IMPROVEMENTS

* [#226](https://github.com/mocachain/moca-storage-provider/pull/226)  ci(e2e): `make test` and the unit-test workflow now run the `test/e2e/piecestore` and `test/e2e/spworkflow` Go packages from an explicit list instead of dropping every path containing `e2e` (the S3, OSS, MinIO and B2 piece store cases skip without credentials), the go-sdk e2e fails when no test ran or any was skipped, the e2e script runs with `set -euo pipefail` and waits for `make proto-gen` before building the chain
* [#224](https://github.com/mocachain/moca-storage-provider/pull/224)  test(e2e): the SP exit e2e now proves the exit: it waits for the exiting SP's family and secondary GVGs to be swapped out to a successor, for the SP to drain out of every group and leave the SP set, then checks sha256 of every object served by the remaining SPs and the successor's migrated pieces; the e2e retry helper now treats a `run command error:` or no-balance message printed by moca-cmd as a failure, bucket create asserts the owner and name and every upload is checked sealed
* [#223](https://github.com/mocachain/moca-storage-provider/pull/223)  test(blocksyncer): make every BlockSyncer e2e verify step assert the rows and fields its block writes, so none passes against an empty table, and run all blocksyncer test packages from bs_test.sh
* [#221](https://github.com/mocachain/moca-storage-provider/pull/221)  chore(ci): start the runner's MySQL with systemd so the workflows run on the Ubuntu 26.04 runner image
* [#218](https://github.com/mocachain/moca-storage-provider/pull/218)  chore(ci): run the SP e2e, Go-SDK e2e and SP exit e2e workflows on Node.js 24 instead of the end-of-life 14.x, which only installs ethers and generates the test private key
* [#214](https://github.com/mocachain/moca-storage-provider/pull/214)  chore(build): run the goreleaser release image (Dockerfile.release) on debian:trixie-slim (glibc 2.41) instead of debian:bookworm-slim (glibc 2.36), so the image's glibc is not older than the Ubuntu 24.04 goreleaser-cross:v1.26.10 build image (glibc 2.39)
* [#209](https://github.com/mocachain/moca-storage-provider/pull/209)  chore(build): build the release binaries with goreleaser-cross:v1.26.10 directly now that the 1.26.10 cross image is published, dropping the temporary GOTOOLCHAIN override added in #201
* [#201](https://github.com/mocachain/moca-storage-provider/pull/201)  chore(build): bump the Go toolchain to 1.26.10 (latest 1.26 patch release) across the build Dockerfile, the CI workflow go-version pins, and the goreleaser release builds
* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  chore(blocksyncer): redact the password from the DSN startup log

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
