Resources here have been deprecated and moved to https://github.com/mocachain/moca-sp-deployment.

## Internal gRPC TLS

The manifests in this directory mount a distinct `<workload>-grpc-tls` Secret into each workload at `/var/run/moca-sp/grpc-tls`. The all-in-one workload uses `sp-all-in-one-grpc-tls`. Certificate issuance and Secret creation remain deployment responsibilities; this repository does not contain certificate material.

Each Secret must contain:

- `ca.crt`: the internal CA certificate trusted by all storage-provider workloads
- `tls.crt`: that workload's certificate with `serverAuth` and `clientAuth` extended key usages
- `tls.key`: the private key for `tls.crt`

Configure every workload with:

```toml
[GRPCTLS]
CACertFile = '/var/run/moca-sp/grpc-tls/ca.crt'
CertFile = '/var/run/moca-sp/grpc-tls/tls.crt'
KeyFile = '/var/run/moca-sp/grpc-tls/tls.key'
```

All fields are mandatory and invalid or missing files prevent startup. Internal gRPC requires mutual TLS 1.3 and has no plaintext fallback.

Clients derive the TLS server name from the configured endpoint. A workload certificate must include every Kubernetes Service DNS name used to dial that workload. For the all-in-one deployment, configure internal endpoints as `grpc:9333`; do not rely on an empty endpoint falling back to a wildcard `GRPCAddress` such as `0.0.0.0:9333`.
