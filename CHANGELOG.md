# Changelog

## Unreleased

BUGFIXES

* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  fix(blocksyncer): keep the block prefetch bounded after a restart so a block that repeatedly fails to export cannot exhaust memory

IMPROVEMENTS

* [#193](https://github.com/mocachain/moca-storage-provider/pull/193)  chore(blocksyncer): redact the password from the DSN startup log

## v1.7.0

BUGFIXES

* [#14](https://github.com/mocachain/moca-storage-provider/pull/14)  fix: evm chain id read from the configuration file env.info

FEATURES

* [#9](https://github.com/mocachain/moca-storage-provider/pull/8)  feat: use evm type tx call sealObject
