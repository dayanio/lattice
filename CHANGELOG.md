# Changelog

All notable user-facing changes are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Versions follow `MAJOR.MINOR.PATCH`. During the 0.x phase (Public Beta), minor versions may include breaking changes.

---

## v0.4.1 (2026-10-11)

Exit-node reliability is the theme: a dead exit node no longer blackholes anyone, and it leaves nothing behind on the host. Also lands the make-before-break relay→direct upgrade and the tvOS cast engine work. The GUI clients (Mac/iOS) ship from the `lattice-apple` repository on their own schedule — see *Upgrade notes*.

### Exit node crash safety (Linux provider)
- A provider crash or `kill -9` no longer leaves the host in a broken state: every iptables rule lives in dedicated `LATTICE-FORWARD` / `LATTICE-NAT` / `LATTICE-INGRESS` / `LATTICE-EGRESS` chains (built-in chains hold a single jump each), exit-takeover routes live in policy table 5180 with rules at priorities 4500/5170/5180, and the host's default route is never touched. When the process dies any death, the kernel removes the tunnel interface, the takeover routes die with it, and lookups fall through to the main table — recovery starts before any cleanup code runs (verified on a real node: internet restored, `iptables-save` shows zero lattice entries)
- New command `lattice net cleanup` sweeps every piece of lattice network state (idempotent, works offline — this is what systemd runs on ExecStopPost/ExecStartPre); `lattice net status` shows what is currently applied
- The packaged `lattice.service` unit now wires `ExecStartPre`/`ExecStopPost` cleanup with `Restart=always`, so even SIGKILL sweeps the host within seconds

### Exit node consumer fail-open
- A consumer that selected an exit node no longer blackholes when the provider restarts: the takeover route is now gated on the provider's data path being alive (WireGuard counters growing, handshake renewing — probe state alone is not trusted). A stalled path fails open after 60 s to the direct path and reinstalls automatically on recovery (issue #63; verified on real devices: the "must disconnect and reconnect the tunnel" symptom is gone)
- Servers already withdrew a dead provider's routes after ~25 s; the gate covers the window where the control plane still believes the provider is online while its data path is dead

### Connectivity
- Relay→direct upgrades no longer tear down the working relay path first (make-before-break, ADR-0007): an ICE that cannot connect (symmetric NAT, TUN-mode proxies capturing UDP) no longer interrupts the tunnel every few minutes during upgrade retries
- Relay: fixed a per-session write race that could corrupt a stream, added per-session forward queues with write deadlines (ADR-0005 P1), and both peers now arm persistent keepalives
- Both peers measure direct-path latency (previously the responder never did, so some devices showed no RTT for a link)

### Cast (tvOS) — experimental
- Cast commands travel over the NATS signaling plane (`lattice.cast.<AppID>.cmd`) with in-engine interception as the primary transport; the embedded engine surfaces cast commands as host events
- `tvoslib`: a C-ABI static library of the engine for the tvOS player, plus `corelib` c-archive builds; `relayloadgen` is a load generator for relay fault drills

### Fixes
- The `lattice` CLI compiles for Windows again
- `/metrics` exposes VictoriaMetrics counters
- Docker builder image bumped to Go 1.26.8

### Upgrade notes
- **GUI clients are not part of this release.** The Mac/iOS apps build from the private `lattice-apple` repository and release on their own version — the consumer-side fail-open inside the Apple engine takes effect with the next app release, not with this tag
- Upgrade the control plane together with Linux agents; providers upgraded from ≤0.4.0 have their legacy rules swept automatically on first start (`lattice net cleanup` runs before the engine)
- As in every 0.x release, control plane and clients are meant to run the same minor version

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
