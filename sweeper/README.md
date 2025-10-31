# Sweeper

## Overview

`sweeper` is a lightweight Go service designed to monitor etcd storage usage and automatically trigger cleanup or maintenance actions before the database reaches its configured quota limit.

The tool was originally developed to support the `eventsse` service, which stores Krateo PlatformOps events in etcd. Over time, these event records can accumulate and cause etcd’s backend database to approach its quota size (`--quota-backend-bytes`).  
When this happens, etcd will reject new writes and may impact cluster stability.

Sweeper’s purpose is to proactively prevent storage exhaustion by:

- watching etcd’s `dbSizeInUse` and `dbSizeQuota` via the etcd API
- executing a cleanup routine when a configurable usage threshold is exceeded

This ensures that systems relying on etcd (like event loggers or controllers) remain healthy and performant even under heavy or long-running workloads.


## Features

- **Smart monitoring** — continuously checks etcd database usage.
- **Custom cleanup logic** — triggers a callback when usage exceeds a threshold (e.g. 80%).
- **Selective key deletion** — example implementation removes old event keys, prioritizing non-`comp-` prefixed ones first.
- Health endpoints — exposes `/healthz` (liveness) and `/readyz` (readiness) for Kubernetes probes.
- Graceful shutdown — handles SIGTERM, SIGINT, and integrates cleanly with Kubernetes pod lifecycle.


## Architecture

```text 
            ┌──────────────────────────┐
            │      Kubernetes Pod      │
            ├──────────────────────────┤
            │        sweeper           │
            │      (this tool)         │
            ├──────────────┬───────────┤
            │ etcd watcher │ HTTP srv  │
            └──────┬───────┴─────┬─────┘
                   │             │
                   ▼             ▼
     ┌────────────────────┐   ┌──────────────────────┐
     │ etcd endpoint(s)   │   │ /healthz /readyz API │
     │ (dbSize monitoring)│   └──────────────────────┘
     └────────────────────┘
```


## Configuration

Sweeper can be configured using a environment variables.

| Env Var.   | Description  | Default |
|------------|--------------|----------|
| `DEBUG`    | Enable or disable debug logs | `true` |
| `PORT`     | Port to listen on (for liveness and readyness HTTP endpoints) | `8081` |
| `ETCD_SERVERS` | List of etcd endpoints to monitor | `["localhost:2379"]` |
| `CLEANUP_THRESHOLD` | Usage ratio (0.0–1.0) at which the cleanup callback is triggered | `0.8` |
| `MONITORING_INTERVAL` | Monitoring interval | `15s` |


## Example Cleanup Logic

A typical cleanup routine might:

1. Delete etcd keys with the prefix krateo.io.events/ that do not start with comp-.
2. If storage pressure remains high, delete also the comp- prefixed keys.

This strategy helps preserve more recent or relevant event data while maintaining etcd health.
