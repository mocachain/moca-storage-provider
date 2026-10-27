## Pre-requisites for deployment

- MySql Database which is accessible from the k8s cluster where SP will be running in.
- storage_provider_db is created in MySql DB. (DB name is configurable in config.toml of moca-sp)
- block_syncer is created in Mysql DB. (can be in a different mysql to storage_provider_db one)
- S3 bucket created(equivalent object storage on other supported Cloud provider).
- AWS Secrets Manager store(equivalent Secret vault on other supported Cloud provider) for holding credentials, such as /dev/moca/gf-sp-devops/secrets
- IAM role with permission to S3 bucket and secret valut, and binding the role to the ServiceAccount used by SP pods.
- Put this as the content of the AWS Secrets Manager store with actual values:

```json
{
    "SP_DB_USER":"xxx",
    "SP_DB_PASSWORD":"xxx",
    "SP_DB_ADDRESS":"xxx:3306",
    "SP_DB_DATABASE":"storage_provider_db",
    "BLOCK_SYNCER_DSN":"xxx",
    "BLOCK_SYNCER_DSN_SWITCHED":"username:password@tcp(db_url)/dbname?parseTime=true&multiStatements=true&loc=Local",
    "BS_DB_USER":"xxx",
    "BS_DB_PASSWORD":"xxx",
    "BS_DB_ADDRESS":"xxx:3306",
    "BS_DB_DATABASE":"block_syncer",
    "BS_DB_SWITCHED_USER":"moca",
    "BS_DB_SWITCHED_PASSWORD":"02aMU4miGcdGZRfb",
    "BS_DB_SWITCHED_ADDRESS":"moca-sp-dev-metadata-instance-1.cnvhwydws6wc.ap-northeast-1.rds.amazonaws.com",
    "BS_DB_SWITCHED_DATABASE":"block_syncer_backup",
    "SIGNER_OPERATOR_PRIV_KEY":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "SIGNER_APPROVAL_PRIV_KEY":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "SIGNER_SEAL_PRIV_KEY":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "SIGNER_GC_PRIV_KEY":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "AWS_ACCESS_KEY":"xxx",
    "AWS_SECRET_KEY":"xxx",
    "BUCKET_URL":"https://s3.<region>.amazonaws.com/<bucket-name>",
    "P2P_PRIVATE_KEY":"XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
    }
```

The supported signer environment variables are `SIGNER_OPERATOR_PRIV_KEY`,
`SIGNER_APPROVAL_PRIV_KEY`, `SIGNER_SEAL_PRIV_KEY`, `SIGNER_BLS_PRIV_KEY` and
`SIGNER_GC_PRIV_KEY`. For these keys, a value in the environment overrides
`config.toml`; an absent variable preserves the configured value. An empty
variable is rejected. Startup logs identify the source without logging the value.
The p2p key follows the same source and empty-value rules.

### Remove legacy funding configuration before upgrading

`SIGNER_FUNDING_PRIV_KEY` is unsupported. Any presence, including an empty value,
now fails shared configuration validation, even in non-signer service modes.
Non-empty TOML `SpAccount.FundingPrivateKey` remains unsupported and is reported
first when both legacy inputs are present. Funding operations continue to use
`OperatorPrivateKey`; there is no separate funding signing account.

Before starting the updated application, remove the legacy environment variable
from self-managed manifests, `envFrom` sources and other injection mechanisms.
Setting it to an empty string is not a migration. Remove non-empty legacy TOML
values as well. The shipped single-signer and all-in-one components no longer
reference the funding Secret entry.

Only after old Pods and manifests no longer reference that entry should an
authorized operator remove it from Secret storage. Do not delete it while an old
workload still requires the reference. This change does not deploy workloads,
clean up Secrets, rotate supported signing keys or change funding accounts.

`BUCKET_URL` only applies when `PieceStore.Store.BucketURL` is left empty in `config.toml`. Setting both to different values fails the startup instead of silently redirecting piece storage.
