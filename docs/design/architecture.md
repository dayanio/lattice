---
title: Architecture Overview
---

# Architecture Overview

Lattice is composed of three core components:

| Component | Entry Point | Purpose |
|-----------|-------------|---------|
| **Lattice Agent** | `cmd/lattice` | Edge node: WireGuard tunnel, NATS signaling, ICE/LRP |
| **LatticeD** | `cmd/latticed` | All-in-one control plane: NATS + SQLite + API + UI |
| **Manager** | `cmd/manager` | Kubernetes operator: reconciles CRDs |

## Transport Layer

Each peer-to-peer connection goes through a state machine:

```
Created → Probing → ICEReady / LRPReady → Failed → Closed
```

ICE (direct P2P) and LRP (relay fallback) race in parallel. Whichever establishes first wins. The connection upgrades from LRP to ICE transparently when a direct path becomes available.

```
Peer A                                    Peer B
  │  ── NATS signal ──────────────────────▶  │
  │  ◀── NATS signal ──────────────────────  │
  │                                          │
  │  ── ICE probe ─────────────────────────▶ │  ← races with LRP
  │  ── LRP connect ───────────▶ Relay ────▶ │
  │                                          │
  └─────────── WireGuard data ───────────────┘
```

## Policy Enforcement

`PolicyEnforcer` interface in `internal/agent/provision/provisioner.go`:

```
SelectEnforcerMode()
  ├── Linux 5.10+ && EDITION=pro → eBPF TC on wf0 TUN interface
  └── default                   → iptables
```

Community edition always uses iptables. Pro edition uses eBPF when kernel supports it, with automatic fallback to iptables.

## Agent Sandbox

The sandbox provides a **zero-privilege** alternative to the regular node:

- Regular node: kernel TUN (`wf0`), requires `CAP_NET_ADMIN`
- Sandbox: gVisor `pkg/tcpip` + `TUNAdapter`, runs as any user

See [Sandbox Architecture](/design/sandbox) for the detailed design.

## Signal Flow

```
LatticeD (NATS broker)
    │
    ├── lattice.signals.peers.<PeerID>  ← peer signaling
    ├── lattice.audit.flow              ← Pro: NATS flow audit
    └── lattice.config.<PeerID>         ← peer config push
```

## Enrollment & Network Management (shipped 2026-09)

Capabilities verified on real devices (cloud control plane + macOS/iOS clients + container nodes):

| Capability | How it works | Reference |
|------------|--------------|-----------|
| **Zero-trust enrollment** | Client generates its WireGuard keypair locally; the private key never leaves the device. Registration with an enrollment token lands in `pending` when the workspace requires approval; the admin approves in the dashboard before the netmap is served. Re-registering with a different pubkey is rejected. | [ADR-0003](/adr/0003-peer-enrollment-approval-and-client-side-keygen) |
| **Join QR** | Dashboard token page renders `lattice://join?server=<dashboard origin>&token=<token>`; Apple clients scan it to enroll without typing. The `server` embedded is the dashboard address the phone is browsing — it must be an address the device can reach. | `frontend/src/pages/manage/tokens` |
| **Netmap convergence** | Netmap is pushed over NATS and re-pulled every `netmap-poll-interval` (default 30s); peers carry endpoints, relay info and allowed IPs. | `internal/server/reconcilers/netmap_builder.go` |
| **LatticeDNS** | Optional built-in `*.lattice` DNS responder in the engine (`enable-dns`), so peers resolve names over the overlay. | [LatticeDNS technical notes](/latticedns-technical) |
| **Subnet routes / exit node** | A peer can advertise subnets (CIDR-validated) so non-agent-installable devices reach the mesh through a gateway node. | spec 2026-09-14-exit-node-subnet-route |
| **Publish gateway** | A mesh member can expose an internal service to the public internet via an ingress gateway (`ingress-addr`), with share links managed in the dashboard. | commit 3dfe10dd |
| **Per-peer stats & approvals UI** | Device detail shows per-peer latency/traffic; enrollment approvals are managed from the device list (macOS client + dashboard). | commits 59079d39, 95cb08d0 |
| **Apple clients** | macOS app runs the Go engine inside a Network Extension (real mesh member, host can bind on the overlay); iOS app with QR join; both ship tunnel logs for diagnostics. | spec 2026-09-13-apple-clients |
