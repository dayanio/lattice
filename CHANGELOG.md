# Changelog

All notable user-facing changes are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versions follow `MAJOR.MINOR.PATCH`. During the 0.x phase (Public Beta), minor versions may include breaking changes.

---

## v0.4.0 (2026-09-27)

Exit nodes, "China direct" split routing and IPv6 for exit-node mode; latency now measured on both ends of a link. The iOS and macOS clients are now built from their own repository (see *Clients*), and the apps and the engine are released together under one version from here on.

### Exit node
- Exit node data plane: a Linux agent can act as an exit provider (IP forwarding, FORWARD and MASQUERADE) and advertises `0.0.0.0/0` in the netmap; a device that selects it sends all its traffic through it
- Consumers are told immediately when a selected provider comes online or goes offline; an offline provider's routes are no longer expanded into consumers' netmaps (an offline exit node used to blackhole the consumer's whole network)
- Selecting or clearing an exit takes effect in seconds instead of up to a minute: route changes are applied as soon as a netmap has been applied, not at the next 15 s poll (measured 12 s and longer before, 2 to 4 s on a Mac and about 1 s on an iPhone after)
- Fixed: on a device that connected with no exit node, selecting one and switching back to "direct" was skipped as "version already applied", so the exit route stayed installed after the server had withdrawn it
- Each netmap apply now prunes peers that are no longer in the netmap (manager entry, WireGuard peer and probe together)

### China direct (split routing)
- With an exit node selected and the switch on, Chinese IPv4 blocks (5494, from the APNIC delegation data) bypass the tunnel while everything else still goes through the exit; measured on an iPhone: 0 packets to Chinese addresses through the exit and 79% fewer bytes
- The switch can be flipped at runtime without restarting the engine (`SetSplitRouting`); `OnRoutesChanged` may carry `{"included":[...],"excluded":[...]}`, and stays a bare array when nothing is excluded, so older clients keep decoding it
- IPv4 only; no domain rules yet (a Chinese domain that resolves to an overseas address still uses the exit)

### IPv6 with an exit node
- Phase 1: with an exit node selected, a device's IPv6 no longer bypasses it. A node's overlay IPv6 is derived from its IPv4 (`fd6c:7270:6c74::/64`); the exit agent probes for a usable IPv6 egress and does IPv6 forwarding and NAT66; the engine has three modes (off, tunnel, blackhole: with no IPv6 egress, IPv6 from the OS is answered with an ICMPv6 "no route" so apps fall back to IPv4)
- Server switch `overlay-ipv6` (env `LATTICE_OVERLAY_IPV6`), **off by default**. Upgrade the clients first, then turn it on: old clients break on IPv6 CIDRs in the netmap
- Not verified on a real exit with IPv6 egress (tunnel mode, the exit agent's probe and the withdraw/recover timing)

### Connectivity
- The direct-path latency is now measured by both ends of a link. Only the initiator used to send the path echoes, so a device that was the responder toward a peer (an iPhone toward the exit node, for example) never had a latency for it. The responder only measures: it never restarts the connection, and it stops asking a peer that predates the echo
- Relay: fixed a data race that could corrupt a session stream (several goroutines wrote to one buffered writer), and the relay upgrade request now carries a `Host` header (every relay upgrade was answered with 400 before)

### Clients
- The iOS and macOS client sources moved to their own repository and build against `apple/engine`, which stays here (it imports `internal/`)
- `apple/engine`: DNS interception answers only queries addressed to the tunnel's own `10.96.0.1` (matching port 53 alone re-forwarded the forwarder's own upstream queries forever)

### Build
- The `lattice` CLI compiles for Windows again (a process probe used a Unix-only call, and `service install` did not exist on Windows)

### Docs
- Exit-node routing and DNS, split routing and IPv6 exit designs (`docs/design/`, `docs/superpowers/specs/`)

### Upgrade notes
- Apps and the control plane of a `0.x` release are meant to run the same minor version; upgrade the control plane with the clients
- IPv6 for exit nodes stays off until you set `overlay-ipv6` (see above)

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
