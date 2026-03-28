# 05 — Sandbox Runtime

**Status:** DRAFT

## Overview

The sandbox runtime provides isolated, stateful execution environments for agents. It is the most performance-critical component of the system — sandbox startup time directly impacts task latency and developer experience.

This document covers sandbox lifecycle management, filesystem strategies for statefulness, dependency caching, and the container architecture.

## Design Principles

1. **Fast startup**: A warm sandbox should be usable in under 5 seconds. A cold sandbox (no pre-provisioned pool member) should start in under 60 seconds.
2. **Stateful by default**: Sandboxes retain filesystem state between sessions. An agent resuming work should find its previous state intact.
3. **Reproducible**: Sandbox state can be snapshotted and restored. Two sandboxes created from the same snapshot should behave identically.
4. **Isolated**: Sandboxes cannot interfere with each other or the host. Process, filesystem, and network isolation are enforced.

## Container Architecture

Each sandbox runs as a Kubernetes Pod with the following container structure:

```
Pod: sandbox-abc123
├── Init Container: workspace-init
│   ├── Clone git repositories
│   ├── Restore dependency caches (from PV or object storage)
│   └── Apply warmup commands from Pool spec
│
├── Container: sandbox-agent-sdk (Rust binary)
│   ├── Runs the Sandbox Agent SDK (https://sandboxagent.dev/)
│   ├── Manages agent process lifecycle (Claude Code, Codex, Pi, etc.)
│   ├── Exposes HTTP API on :2468 for session control, filesystem, process mgmt
│   ├── Provides desktop runtime for GUI agents (Cursor)
│   └── Mounts workspace PV at /workspace
│
├── Container: bridge (Go binary)
│   ├── Connects SDK to Kubernetes control plane
│   ├── Consumes SSE events from SDK → publishes to NATS JetStream
│   ├── Credential proxy (intercepts outbound HTTPS, injects auth headers)
│   ├── Updates Session/Sandbox CR status
│   └── Metrics exporter (Prometheus endpoint)
│
└── Volumes:
    ├── workspace-pv: PersistentVolumeClaim (sandbox state)
    ├── cache-pv: PersistentVolumeClaim (shared dependency cache, ReadOnlyMany)
    └── secrets: Projected volume (API keys, tokens — read by bridge only)
```

## Filesystem Strategy

### Workspace Volume

Each sandbox gets a dedicated PersistentVolumeClaim for its workspace. This volume persists across pod restarts (for sandbox reuse) and contains:

```
/workspace/
├── repo/                  # Cloned repository
├── .sandbox/              # Sandbox metadata
│   ├── state.json         # Sandbox state (phase, assigned task, etc.)
│   └── sessions/          # Session logs and artifacts
├── .cache/                # Agent-specific caches
│   ├── npm/
│   ├── pip/
│   └── go/
└── .tools/                # Installed tools and binaries
```

### Shared Dependency Cache

A ReadOnlyMany PVC can be mounted across sandboxes in the same pool to share common dependency caches:

- **npm**: `node_modules` for common packages
- **pip**: Wheels and virtualenvs for common Python packages
- **Go**: Module cache
- **apt**: Package cache

The shared cache is rebuilt periodically (e.g., nightly) by a CronJob that runs dependency installation against the pool's warmup spec.

### Volume Lifecycle

| Event | Volume Behavior |
|-------|----------------|
| Sandbox created (cold) | New PVC created, init container populates it |
| Sandbox created (warm) | PVC restored from snapshot or retained from previous sandbox |
| Session starts | Volume already mounted, no action needed |
| Session ends | Volume retained as-is |
| Sandbox terminated (Retain policy) | PVC kept for reuse by next sandbox |
| Sandbox terminated (Delete policy) | PVC deleted |
| Pool scaled down | Excess PVCs cleaned up after grace period |

## Snapshot and Restore

Sandboxes support snapshotting via [VolumeSnapshots](https://kubernetes.io/docs/concepts/storage/volume-snapshots/) (CSI feature):

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: sandbox-abc123-snap-20260327
spec:
  volumeSnapshotClassName: csi-snapshotter
  source:
    persistentVolumeClaimName: sandbox-abc123-workspace
```

Use cases:
- **Pre-warm snapshots**: Take a snapshot after warmup completes. New sandboxes restore from this snapshot instead of re-running warmup commands.
- **Task checkpointing**: Snapshot before a risky operation so the sandbox can be rolled back.
- **Debugging**: Snapshot a failed sandbox for post-mortem analysis.

## Sandbox Warmup

The warmup process runs in the init container and prepares the sandbox for work:

1. **Base image pull**: The sandbox image contains OS-level dependencies (compilers, runtimes, common tools).
2. **Git clone**: Clone specified repositories into `/workspace/repo/`.
3. **Dependency install**: Run warmup commands (e.g., `npm install`, `pip install -r requirements.txt`).
4. **Cache population**: Populate `.cache/` directories.
5. **Health check**: Verify the harness is responsive.

### Warmup Optimization Strategies

| Strategy | Approach | Startup Reduction |
|----------|----------|-------------------|
| **Pre-provisioned pools** | Keep warm sandboxes ready | ~0s (already warm) |
| **Volume snapshots** | Restore from snapshot instead of re-running warmup | 5-15s |
| **Shared cache volumes** | Mount pre-built dependency caches as ReadOnlyMany | 10-30s |
| **Image layering** | Bake common dependencies into the base image | Eliminates install step |
| **Lazy git clone** | Use sparse checkout or shallow clone | 50-80% clone time reduction |

## Resource Management

### Compute

Sandboxes request and limit CPU/memory via standard Kubernetes resource specs. Recommendations:

| Agent Type | CPU Request | Memory Request | Rationale |
|------------|-------------|----------------|-----------|
| Claude Code | 2 cores | 4Gi | Needs headroom for tool execution (compilers, test runners) |
| Codex | 2 cores | 4Gi | Similar tool execution needs |
| Pi | 1 core | 2Gi | Lighter weight, fewer concurrent tool processes |

### Storage

- **Workspace volume**: 10-100Gi depending on repository size. Default 50Gi.
- **Shared cache**: 20-50Gi per pool. Mounted ReadOnlyMany.
- **StorageClass**: Prefer SSDs for workspace volumes (latency-sensitive). HDD acceptable for shared caches.

### Network

Each sandbox pod gets a NetworkPolicy that restricts egress:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sandbox-abc123-netpol
spec:
  podSelector:
    matchLabels:
      factory.example.com/sandbox: abc123
  policyTypes: [Egress]
  egress:
    # Allow DNS
    - to:
        - namespaceSelector: {}
      ports:
        - port: 53
          protocol: UDP
    # Allow configured endpoints
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
      ports:
        - port: 443
          protocol: TCP
```

The sidecar credential proxy can further restrict which HTTPS hosts are reachable (allowlist-based).

## Sandbox Reuse

When a task completes and the sandbox returns to `Idle`, it can be reused by the next task in the same pool. Reuse avoids the cost of provisioning a new sandbox.

### Reuse Hygiene

Between task assignments, the sandbox controller performs lightweight cleanup:

1. **Kill stale processes**: Signal any leftover processes from the previous session.
2. **Reset working directory**: Optionally `git clean` / `git checkout` the repo.
3. **Clear temp files**: Remove `/tmp/*` and agent temp directories.
4. **Preserve caches**: Keep `.cache/` and installed dependencies.

The level of cleanup is configurable per pool:

```yaml
spec:
  sandboxTemplate:
    reusePolicy:
      cleanup: partial    # none | partial | full
      gitReset: true      # Reset repo to clean state
      preserveCaches: true
```

## Future Considerations

- **Rootless containers**: Run sandbox containers without root for additional security (requires careful handling of package installation).
- **gVisor/Kata**: Additional sandboxing layers for untrusted agent workloads.
- **Ephemeral containers**: Use Kubernetes ephemeral containers for debugging live sandboxes.
- **Hibernation**: Checkpoint sandbox processes to disk and restore them later (CRIU-based), enabling true pause/resume.
