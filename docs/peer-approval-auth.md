# Peer approval authentication

`Gateway.PeerApprovalAuthMode` controls BLS migration and swap-out approval callers:

- `disabled` accepts legacy callers without verification.
- `permissive` accepts requests with no authentication headers, but rejects malformed, expired, tampered, or wrong-caller authentication.
- `required` rejects requests unless their GNFD1-ECDSA signature is valid and belongs to the authoritative caller SP. This is the default.

```toml
[Gateway]
PeerApprovalAuthMode = "permissive"
```

Use `permissive` only while upgrading peers; new configuration templates use `required`.

Use a five-minute `X-Gnfd-Expiry-Timestamp`. The signature covers the request method, path, Host, expiry, and `X-Gnfd-Unsigned-Msg`.

For a BLS migration request, send the identical hexadecimal sign-doc bytes in both `X-Gnfd-Secondary-Migration-Bucket-Msg` and `X-Gnfd-Unsigned-Msg`. Receivers reject mismatches. For swap-out, use its existing `X-Gnfd-Unsigned-Msg` header.

Upgrade order: configure receivers as `permissive`, upgrade callers so they use the typed signer request, then upgrade receivers and switch them to `required`. Proxies must preserve the original `Host` header; changing it after signing invalidates the request.
