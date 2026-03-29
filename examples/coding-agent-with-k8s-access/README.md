# Coding Agent with Kubernetes Access

This example sets up a Claude Code agent that can build, deploy, and troubleshoot an application in a dedicated Kubernetes namespace. The agent has access to `kubectl` inside its sandbox and a scoped ServiceAccount that grants it permissions to a target namespace.

## Use Case

A development team wants an AI agent that can:

1. Write application code
2. Build container images
3. Deploy to a staging namespace (`app-staging`)
4. Inspect pod logs, events, and resource status to troubleshoot issues
5. Iterate on fixes until the deployment is healthy

## Architecture

```
┌─────────────────────────────────────────────┐
│  team-alpha namespace                       │
│                                             │
│  ┌────────────┐    ┌─────────────────────┐  │
│  │ Pool       │───►│ Sandbox Pod         │  │
│  │ (claude-k8s│    │  ├ SDK              │  │
│  │  -pool)    │    │  ├ Bridge           │  │
│  └────────────┘    │  └ Agent (Claude)   │  │
│                    │    └ kubectl access ─┼──┼──► app-staging namespace
│  ┌────────────┐    │      (ServiceAccount)│  │
│  │ AgentConfig│    └─────────────────────┘  │
│  │ (claude-k8s│                             │
│  └────────────┘                             │
└─────────────────────────────────────────────┘
```

The agent's sandbox mounts a ServiceAccount token that grants RBAC permissions to the `app-staging` namespace. Network policies allow the agent to reach the Kubernetes API server and container registry.

## Manifests

### 1. Target namespace and RBAC

The agent needs permissions in the namespace it deploys to. We create a Role with deployment-related permissions and bind it to a ServiceAccount that the sandbox pod uses.

See [`manifests/rbac.yaml`](manifests/rbac.yaml).

### 2. AgentConfig

Configures Claude Code with `kubectl` pre-installed in the warmup and credentials for both the Anthropic API and the container registry.

See [`manifests/agentconfig.yaml`](manifests/agentconfig.yaml).

### 3. Pool

A pool with `kubectl` and Docker CLI in the warmup image, network access to the K8s API server and container registry, and the sandbox ServiceAccount mounted.

See [`manifests/pool.yaml`](manifests/pool.yaml).

### 4. Task

A standalone task that asks the agent to build, deploy, and verify an application.

See [`manifests/task.yaml`](manifests/task.yaml).

## How It Works

1. The **Pool Controller** pre-provisions sandboxes from the pool template. Each sandbox pod runs with the `sandbox-deployer` ServiceAccount, which has RBAC permissions in `app-staging`.

2. The **Task Controller** claims a sandbox and creates a Session. The bridge sidecar writes the task prompt and context files into the sandbox.

3. **Claude Code** starts working: it writes code, builds an image, runs `kubectl apply`, and checks `kubectl get pods` to verify the deployment.

4. If pods crash, the agent reads logs with `kubectl logs` and iterates on fixes — all within the same stateful sandbox so it doesn't lose context.

5. On completion, the bridge extracts output artifacts (the final manifests) and publishes a session-complete event.

## Key Points

- **ServiceAccount scoping**: The sandbox pod's ServiceAccount only has access to `app-staging`, not the broader cluster. The agent cannot modify the orchestration namespace or other tenants.
- **Network policy**: Egress is restricted to the K8s API server, container registry, and Anthropic API. The agent cannot reach arbitrary endpoints.
- **Credential proxy**: The Anthropic API key is injected by the bridge sidecar's credential proxy — the agent never sees raw credentials.
- **Statefulness**: The sandbox retains the cloned repo, built artifacts, and installed tools across retries. If the agent fails and retries, it picks up where it left off.
