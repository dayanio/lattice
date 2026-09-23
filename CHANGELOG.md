# Changelog

All notable user-facing changes are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versions follow `MAJOR.MINOR.PATCH`. During the 0.x phase (Public Beta), minor versions may include breaking changes.

---

## v0.3.0 (2026-09-23)

### Apple Embedded SDK (M0–M3)
- lattice-shim tsnet-style `Server` — user-space Dial/Listen over a gVisor netstack, no kernel TUN device
- `apple/engine/embedded` — NE-free embedded engine: real enrollment (Start/StartAsync/Stop), overlay Dial/Listen, persisted device identity, gomobile-bindable wrappers (`EmbeddedConn`/`EmbeddedListener`)
- EmbeddedKit Swift Package + xcframework build script (iOS + macOS slices)
- Integration tests against a live control plane; multi-round stability verified

### Clients
- iOS: subnet-route (CIDR) editor, share publishing, realtime peer metrics (RTT chart, rates, counters), LatticeDNS row
- macOS: window sizing polish, peer detail action rows inline
- Embedded engine UI withheld from this release (SDK shipped for embedders)

### Docs
- gVisor embedded networking design: security model, approval flow, egress/ingress, protocol roadmap, platform matrix, Tailscale positioning

---

## Unreleased (v0.2.0)

### Agent Sandbox
- Agent detail drawer in dashboard — trace viewing, network audit log, sub-agent delegation UI
- Token-based enrollment system for sandbox agents

### MCP Server
- External MCP server management page (register, list, delete) in dashboard

### E2E Testing
- Sandbox E2E test infrastructure (Ginkgo-based, covers gVisor startup, WireGuard connectivity, egress filter, forward listener)

### Security
- SaaS security compliance hardening for public deployment

---

## v0.1.0 (2026-05)

First public beta release.

### Highlights

- **AI Agent Platform**: MCP Server (14 tools), AgentIdentity CRD + lifecycle, gVisor zero-privilege sandbox, intent engine (Pro), time-travel debugging (Pro), compliance automation (Pro)
- **Agent Sandbox**: `lattice sandbox start` — gVisor user-space netstack + WireGuard, tool call tracing (`la_tool_spans`), sub-agent delegation API
- **Egress Control (Pro)**: CIDR whitelist filter, HTTP CONNECT forward proxy, ForwardListener
- **Signaling**: HTTP REST API for CLI management
- **Policy Engine**: Rule engine with eBPF TC support (Pro), comprehensive test coverage
- **Network Peering**: Cluster Peering CRD, Network Peering with status cards
- **Audit & Observability**: MCP tool spans, NATS flow audit (Pro), i18n support
- **Multi-tenancy**: Workspace isolation, RBAC (admin/editor/member/viewer), JWT authentication
- **Core Networking**: WireGuard mesh, ICE NAT traversal (STUN/TURN), LRP relay (TCP + QUIC), K8s CRD Operator (kubebuilder)
