# Agent Orchestration System Specification

## Overview

This specification describes an **agent orchestration system built on Kubernetes** that enables running, managing, and coordinating fleets of AI agents in stateful sandboxes. The system provides a universal adapter interface (via the Sandbox Agent SDK) across multiple agent harnesses (Claude Code, Codex, Pi, etc.) and supports use cases ranging from fleet-based software development to personal assistants, custom tool-using agents, and batch automation.

## Reading Guide

The spec is organized for **progressive disclosure**. Start with the vision, then build up your understanding layer by layer. Each document is self-contained but references related documents where appropriate.

### Layer 1: Why and What

| Document | Description |
|----------|-------------|
| [01 - Vision and Goals](01-vision-and-goals.md) | What we're building, why, target use cases, and explicit non-goals |
| [02 - Concepts and Terminology](02-concepts-and-terminology.md) | Domain model, key abstractions, and glossary |

### Layer 2: System Architecture

| Document | Description |
|----------|-------------|
| [03 - Architecture Overview](03-architecture-overview.md) | High-level system architecture, component map, and deployment topology |

### Layer 3: Subsystem Specifications

| Document | Description |
|----------|-------------|
| [04 - Control Plane](04-control-plane.md) | Kubernetes operators, Custom Resource Definitions, and API design |
| [05 - Sandbox Runtime](05-sandbox-runtime.md) | Sandbox lifecycle, stateful environments, filesystem and dependency caching |
| [06 - Agent Adapter Layer](06-agent-adapter.md) | Sandbox Agent SDK integration, bridge sidecar, session management, event streaming |
| [07 - Orchestration Engine](07-orchestration-engine.md) | Task decomposition, DAG execution, multi-agent workflows |

### Layer 4: Cross-Cutting Concerns

| Document | Description |
|----------|-------------|
| [08 - Observability and Events](08-observability-and-events.md) | Logging, metrics, tracing, and event streaming |
| [09 - Security Model](09-security-model.md) | Isolation, secrets management, RBAC, and network policies |

### Layer 5: Context

| Document | Description |
|----------|-------------|
| [10 - Prior Art](10-prior-art.md) | Analysis of Sandbox Agent SDK, Cloudflare Dynamic Workers, Pi, and CNCF projects |

## Technology Choices

- **Primary language:** Go
- **Runtime platform:** Kubernetes
- **CNCF ecosystem:** Leverage existing projects where they fit (detailed in individual specs)
- **Secondary languages:** Rust or TypeScript where ecosystem demands it (e.g., the Sandbox Agent SDK is Rust)

## Status

This specification is a **living document**. Each subsystem spec includes a status indicator:

- `DRAFT` — Initial ideas, subject to significant change
- `REVIEW` — Ready for stakeholder feedback
- `ACCEPTED` — Approved for implementation
