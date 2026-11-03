# Stopping a storage provider

`moca-sp` stops gracefully on SIGTERM, SIGINT or SIGHUP:

1. Every module stops (approver, gateway, manager, signer, ...). This step may take at most 30 seconds; a module that has not stopped by then is left behind and the stop continues.
2. The gRPC server stops gracefully: it waits up to 10 seconds for in-flight calls and open streams (other storage providers and clients keep some open), then closes every remaining connection and waits up to 2 more seconds for the call handlers to return. A handler that is still running after that (for example one blocked on a backend call that ignores cancellation) is left behind.
3. The last log lines are written and flushed, for at most 2 seconds.
4. The process exits with status 0.

Logging the first signal does not delay cancellation or handling a second signal. The whole stop is bounded: if it has not finished 45 seconds after the signal, the process exits with status 1. A second SIGTERM, SIGINT or SIGHUP during the stop exits at once with status 1. Both of these exits also flush the logs for at most 2 seconds, so a stalled log writer (a full stdout pipe, a stalled disk) delays them by 2 seconds at most and never keeps the process alive.

SIGKILL cannot be handled by any process. Give the supervisor more time than the 47-second worst case, so it never has to send it:

| Supervisor | Setting |
|---|---|
| systemd | `KillSignal=SIGTERM` (the default) and `TimeoutStopSec=60` in the unit's `[Service]` section (the default is 90s) |
| Kubernetes | `terminationGracePeriodSeconds: 60` in the pod spec (the default is 30s) |
| Docker | `docker stop --time 60` / `stop_grace_period: 60s` in compose (the default is 10s) |

If the process is killed anyway, the signer's persisted EVM state (see [signer-evm-disposition.md](signer-evm-disposition.md)) keeps track of every transaction it signed, so nothing signed is lost; uploads in progress have to be retried by their clients.
