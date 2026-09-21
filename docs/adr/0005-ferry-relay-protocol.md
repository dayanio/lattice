# ADR-0005: FERRY — the Lattice relay service and the FERRY/1 protocol

- **Status**: Proposed
- **Date**: 2026-09-19
- **Related**: ADR-0004 (LRP per-peer authentication), ADR-0003 (client-side
  keygen)
- **Supersedes** (when accepted): the wire format and naming of LRP ("bolt",
  "lrp", "wrrp", "lrper" are all names of this one service today)

## Summary

When two peers cannot reach each other directly (ICE fails: symmetric NAT,
UDP blocked, a container behind a bridge), Lattice carries their WireGuard
packets through a relay. That relay exists, but live testing on 2026-09-19
showed it had **never actually carried a packet**, and a review of the wire
protocol found several design problems that make it unsafe to leave as is.

This ADR (1) names the service **FERRY** (in the role Tailscale's DERP plays),
(2) records what we found, (3) specifies **FERRY/1**, a cleaned-up wire
protocol, and (4) plans a compatible migration from LRP.

The name: a ferry is what you take when there is no bridge. Peers use the
direct path when it exists and are carried across only when it does not; when
the bridge is back they go back to it.

## Context

### What the relay is today

Facts, from the code as of commit `e28eb9bb`:

- One service, several names. `lrper` (`cmd/lrper`) is the standalone binary;
  `latticed --standalone` embeds the same server in process. Packages,
  config keys and log lines say `lrp`, `bolt` or `wrrp` interchangeably.
- Two transports. **TCP**: `GET /lrp/v1/upgrade` with `Upgrade: lrp`
  (`bolt` is also accepted), then an opaque frame stream on the hijacked
  connection. **QUIC**: ALPN `lrp`; a control stream carries Register, auth
  and keepalive, and `Forward`/`Probe` frames travel as QUIC datagrams.
- A frame is a 12-byte little-endian header
  (`Seq u16 | PayloadLen u32 | Cmd u8 | ToID u32 | Reserved u8`) plus payload.
  Commands: Register, Forward, KeepAlive, Probe, AuthChallenge, AuthResponse.
- A peer is identified by a 32-bit ID: bytes 4..8 of its WireGuard public key
  (`peerIDFromPublicKey`).
- `Forward` carries a WireGuard packet (already encrypted end to end). `Probe`
  carries a signaling packet, so the relay can also stand in for NATS.
- Authentication: a shared token in the Register payload, optionally followed
  by the ADR-0004 X25519 challenge-response. `lrp-require-peer-auth` defaults
  to `false`.
- Client behaviour: a keepalive every 20 s (TCP), reconnect with 1 s to 30 s
  exponential backoff, QUIC idle timeout 90 s with a 25 s keepalive period.
- The control plane advertises the relay to agents through
  `relay-advertise-url`, delivered in the registration response and netmap as
  `LrpUrl`.
- The transport race is in `Probe.discover`: ICE and LRP are dialled
  concurrently, and if LRP wins, an ICE success within the same call upgrades
  the connection.

### What live testing showed (2026-09-19)

Testing a Mac, a cloud container and a phone against a cloud control plane
found that the relay path had never worked end to end. Each item below was a
separate defect, and each hid the next:

| # | Defect | Fix |
|---|--------|-----|
| 1 | The registration response carried no relay URL, so no agent ever created a relay client (server saw 0 sessions, 0 rejections). | `501903a5` |
| 2 | The relay token contains `+` (base64). `splitURLToken` used `url.ParseQuery`, which turns `+` into a space, so a correctly configured token was always rejected. | `501903a5` |
| 3 | The agent created the relay client but never set `EnableLrp`, which gates both the ICE/LRP race and the bind's relay receive path. | `501903a5` |
| 4 | A `relay-url` written from server defaults (`:6266`) was treated as an operator override, so the agent dialled localhost. | `21dadf0d` |
| 5 | The server forwarded a sender's frame untouched, so the header still held the **receiver's** own ID. WireGuard's roaming logic then re-pointed the peer at the node itself. The relay was selected in about a second, but no handshake could ever complete over it (`lrp-ready`, `Handshake: never`, probe restarted every 16 s). | `129e5357` |
| 6 | The cloud security group did not open the relay port, and a container cannot reach its own host's public IP on it. | operational |

Separately, the failover around the relay was slow and one-way: a silently
dead direct path took about 3 minutes to notice (bounded by the 180 s
handshake-age rule), and once on the relay a probe never returned to the
direct path (`e28eb9bb` addresses both).

Defect 5 is the important one. It is not a coding slip so much as a symptom of
the protocol: see finding F3.

### Protocol review findings

Severity is our judgement. "Verified" means we read the code that does it.

| ID | Sev | Finding | Status |
|----|-----|---------|--------|
| F1 | High | **Peer authentication is off by default.** With `lrp-require-peer-auth=false` the session is inserted before the challenge completes, and `SessionManager.Register` replaces any existing session for the ID unconditionally. A holder of the shared token can displace a verified peer without proving anything. | Verified |
| F2 | High | **The identity binding is 32 bits.** The claimed ID is compared with 4 bytes of the public key. A keypair whose public key matches those 4 bytes costs about 2^32 X25519 key generations, so an attacker can pass both the prefix check and the challenge for a chosen victim ID. Honest collisions also grow as N²/2^33 (about 1% at 10,000 peers). ADR-0004 reasoned about 8 bytes; the implementation uses 4. | Verified |
| F3 | Med | **`ToID` means three different things**: the client's own ID in Register, the destination when a client sends, and (before defect 5's fix) the receiver's own ID when a frame is delivered. Receivers derive the peer's identity from it, so any relay that forgets to rewrite it silently breaks routing. | Verified |
| F4 | Med | **No backpressure on the relay's forward path.** `Relay()` writes to the destination's connection synchronously from the *sender's* read loop, with no write deadline and no per-session queue. A stalled receiver can block every sender addressing it, including the senders' own keepalive reads. | Read from code; not load-tested |
| F5 | Med | **The control channel is plaintext.** The TCP client dials plain HTTP; the shared token travels in clear. The QUIC client sets `InsecureSkipVerify: true`, so a man in the middle can read the token and strip traffic. ADR-0004 lists the QUIC part as a known follow-up. Advertising the token-bearing URL to every agent (defect 1's fix) widens what a leak exposes. | Verified |
| F6 | Low | **One relay, no selection.** The netmap names a single relay for everyone: a single point of failure and no proximity choice. | Verified |
| F7 | Low | **Format inconsistencies.** Header is little-endian while IDs are big-endian elsewhere; `Seq` is written but never read; `Reserved` has no version semantics; five names for one service. | Verified |
| F8 | Low | **No server-driven liveness.** The relay does not ping; a half-open client connection is only discovered on write failure or a 30 s read deadline. | Verified (TCP) |
| F9 | ? | **QUIC datagram size.** WireGuard packets on our overlay (MTU 1280 on the Mac tunnel) plus a header can exceed a QUIC datagram. Oversized sends would be dropped. | **Unverified**, needs a test |

ADR-0004 also differs from the code in places (Register payload contents,
AuthResponse size, 64-bit vs 32-bit ID). This ADR treats the code as the truth
and corrects the record.

## Goals and non-goals

Goals:

1. A single, documented, versioned wire protocol with one meaning per field.
2. Identity that cannot be spoofed by holders of the shared token or by
   brute-forcing a short ID prefix.
3. A relay that cannot be stalled by one slow receiver.
4. Confidentiality of the control channel (token, metadata).
5. A migration in which old and new agents and relays interoperate.
6. Enough observability to answer "is this peer relayed, and is the relay
   healthy" without reading logs.

Non-goals (this ADR):

- Relay-to-relay meshing and a "home relay" model like DERP's (see *Future
  work*).
- Hiding metadata from the relay operator.
- Replacing NATS for signaling. `Probe` stays as a fallback.
- Changing what is relayed. Payloads stay opaque WireGuard packets.

## Decision

### 1. Name and identifiers

The service is **FERRY**; the protocol is **FERRY/1**.

| Today | After |
|-------|-------|
| `lrper` binary | `ferryd` |
| `LRP relay server` / `lrp` / `bolt` / `wrrp` | `FERRY` |
| `Upgrade: lrp`, `/lrp/v1/upgrade` | `Upgrade: ferry/1`, `/ferry/v1/upgrade` |
| QUIC ALPN `lrp` | `ferry/1` |
| config `relay-url`, `relay-quic-url`, `relay-advertise-url`, `lrp-auth-token`, `lrp-require-peer-auth`, `enable-lrp` | `ferry-url`, `ferry-quic-url`, `ferry-advertise-url`, `ferry-token`, `ferry-require-peer-auth`, `enable-ferry` |
| netmap field `LrpUrl` | `FerryUrl` |
| transport state strings `lrp-ready` | see *Open questions* |

Every old name is accepted as an alias for the transition (see Migration).

### 2. FERRY/1 wire format

All integers are **big-endian**. A frame is a 24-byte header followed by
`PayloadLen` bytes.

```
 0        1        2        4                 8                      16                     24
 +--------+--------+--------+-----------------+----------------------+----------------------+
 |  Ver   |  Cmd   | Flags  |   PayloadLen    |        SrcID         |        DstID         |
 |  u8=1  |   u8   |  u16   |       u32       |         u64          |         u64          |
 +--------+--------+--------+-----------------+----------------------+----------------------+
```

- `Ver`: protocol version, 1. Frames with an unknown `Ver` close the session.
- `Flags`: reserved, must be 0 in v1; receivers ignore unknown bits.
- `SrcID` / `DstID` are the full 64-bit `PeerID` (the first 8 bytes of the
  WireGuard public key, `infra.FromKey`), the same value already used for NATS
  subjects and the fake endpoint address.
- **`SrcID` is set by the relay, never trusted from the client.** On `Forward`
  and `Probe` the relay overwrites `SrcID` with the sender's authenticated
  session ID before delivery, so a receiver can rely on it. `DstID` is set by
  the sender and is left unchanged. This gives each field exactly one meaning.
- `Seq` is dropped.

The header grows from 12 to 24 bytes per relayed packet: on a 1400-byte
packet that is about 1.7% total header overhead instead of about 0.9%. That is
the price of 64-bit IDs and a version byte; it does not affect direct
connections.

Commands:

| Cmd | Value | Direction | Payload |
|-----|-------|-----------|---------|
| Hello | 0x01 | client → relay | client public key (32 B), capabilities (u32) |
| Challenge | 0x02 | relay → client | ephemeral X25519 public key (32 B), relay capabilities (u32) |
| Proof | 0x03 | client → relay | `HMAC-SHA256(token, challenge \|\| clientPub)` (32 B) \|\| `X25519(clientPriv, challenge)` (32 B) |
| Ready | 0x04 | relay → client | empty; the session is now usable |
| Forward | 0x10 | both | opaque WireGuard packet |
| Probe | 0x11 | both | signaling packet |
| KeepAlive | 0x20 | both | empty; either side may send, the peer answers with KeepAlive |
| Error | 0x7f | relay → client | u16 code, then text; sent only after authentication |

`Hello` replaces `Register`. It carries the **full 32-byte public key**, and
the relay derives `PeerID` from it; the client no longer claims an ID.
Because the token now enters an HMAC instead of being sent, the token is no
longer disclosed to the relay in clear or to anyone observing an unencrypted
leg (TLS still protects everything else; see 4).

### 3. Session lifecycle and authentication

```
client                                        ferryd
  |  Upgrade: ferry/1  (or QUIC ALPN ferry/1)   |
  |-------------------------------------------->|
  |  Hello   { pub, caps }                      |
  |-------------------------------------------->|   no session exists yet
  |  Challenge { eph_pub, caps }                |
  |<--------------------------------------------|
  |  Proof   { HMAC(token, eph||pub), DH }      |
  |-------------------------------------------->|   verify token, DH, pub prefix == PeerID
  |  Ready                                      |
  |<--------------------------------------------|   session inserted, now routable
  |  Forward / Probe / KeepAlive ...            |
```

Rules:

1. **A session is created only after `Proof` verifies.** No lenient mode in
   v1. An unauthenticated connection never occupies an ID and never receives
   traffic. There is a 10 s deadline from connect to `Ready`.
2. **Takeover.** A verified session for the same public key replaces the
   previous one (this is what a reconnect is). A verified session for a
   *different* public key that maps to an occupied `PeerID` is refused
   (`Error: id-collision`) while the incumbent is alive. This closes F1 and
   makes a 64-bit prefix collision an availability problem for at most one of
   two peers rather than a hijack.
3. **Liveness.** Both sides may send `KeepAlive`; the relay sends one if a
   session is idle for 15 s and closes it if nothing arrives for 45 s. This
   addresses F8 and gives the relay a way to notice half-open TCP connections.
4. **Version negotiation.** The transport chooses the codec: TCP by the
   `Upgrade` token (`ferry/1`, else legacy `lrp`/`bolt`), QUIC by ALPN. There
   is no sniffing of frame bytes, which would be ambiguous because a legacy
   header begins with an arbitrary `Seq`.

### 4. Confidentiality

- TCP mode uses TLS (HTTPS upgrade) by default. Plain HTTP is available only
  with an explicit `ferry-insecure` setting, for loopback and tests.
- QUIC already runs TLS 1.3; `InsecureSkipVerify` is removed.
- **Pinning.** Because a relay is often self-hosted with a self-signed
  certificate, the control plane advertises the relay's certificate
  fingerprint next to its address:
  `ferry://host:6266#sha256=<hex>`. Agents pin it. This avoids a public CA
  requirement and removes the MITM in F5.
- The token stays a membership credential, not an identity: identity comes
  from the DH proof, membership from the HMAC.

### 5. Data path semantics

`Forward` is **best effort, unordered, unacknowledged**, exactly like the UDP
it carries. The relay must never let one receiver slow another sender.

- Each session has a bounded outbound queue (default 256 frames or 512 KiB,
  whichever is smaller). `Relay()` enqueues and returns; a per-session writer
  drains the queue with a 2 s write deadline.
- When the queue is full, **drop the new frame** and count it. Never block the
  sender, never buffer without bound.
- A writer that misses its write deadline closes the session; the client
  reconnects with its existing backoff.
- Oversize frames (`PayloadLen` above 64 KiB) close the session before any
  allocation, as today.
- Datagram-size handling on QUIC is decided by test F9; if oversized packets
  are dropped, either the relay advertises a maximum forward size in
  `Challenge` so the agent lowers the tunnel MTU on relayed paths, or `Forward`
  falls back to a stream for large frames.

### 6. Observability

The relay package has no metrics today. Add these, with the `ferry_` prefix,
using the same metrics library the agent already uses:

- `ferry_sessions` (gauge, by transport tcp/quic)
- `ferry_frames_total{cmd,result=forwarded|no_route|dropped_full|dropped_oversize}`
- `ferry_bytes_total{direction}`
- `ferry_auth_failures_total{reason}`
- `ferry_session_writer_stalls_total`

Agent side, surfaced through the existing `lattice status` transport line:
whether a peer is relayed, and which relay carries it. The `Transport:` line
introduced with the status enhancement already prints `lrp-ready (relayed)`;
FERRY adds the relay address and the relay RTT to it.

## Migration and compatibility

The wire change is breaking, so it ships in phases. At every phase an
old agent works against a new relay and the reverse.

| Phase | Relay (`ferryd`) | Agent | Notes |
|-------|------------------|-------|-------|
| P0 (done) | Fix defects 1 to 6 | Same | Already released as `501903a5`, `21dadf0d`, `129e5357` |
| P1 | Dual-stack: serves FERRY/1 and legacy LRP on the same listener (by `Upgrade` token / ALPN). Non-blocking forward queue and write deadline apply to **both** codecs. Legacy sessions cannot replace verified ones. | Unchanged | Fixes F1 (partly), F4 immediately, with no agent change |
| P2 | Same | Speaks FERRY/1 when the relay advertises it (`FerryUrl` present), else legacy | Netmap carries both `LrpUrl` and `FerryUrl` |
| P3 | `ferry-legacy=false` becomes the default | Same | Legacy support is removed one release after telemetry shows no legacy sessions |
| P4 | Rename binary, config keys, metrics, log fields | Same | Old names stay as aliases for two releases, with a deprecation log line |

Two things stay stable across the rename because outside consumers read them:
the transport state strings used by the Apple client (`ConnectionStates()`),
and the `lrp-ready` text in `lattice status`. Both change only with a
deprecation cycle, see *Open questions*.

## Testing plan

- Unit: frame encode/decode round trip and rejection of unknown `Ver`;
  `SrcID` is overwritten on forward whatever the client wrote; HMAC and DH
  proof happy path and each failure (bad token, wrong DH, prefix mismatch,
  low-order point); takeover rules (same key replaces, different key refused).
- Integration on loopback (extends `integration_auth_test.go`): two clients,
  frame from A to B arrives with `SrcID = A` (the regression test for defect 5
  already exists in the LRP codec and moves over).
- Backpressure: a receiver that never reads must not delay delivery between two
  other peers, the queue depth is bounded, and `dropped_full` increases.
- Compatibility: legacy client against the P1 relay; FERRY/1 client against a
  legacy-only relay (falls back).
- Fuzz: the header and Hello/Proof parsers.
- F9: measure the largest WireGuard packet that survives a relayed round trip
  over QUIC datagrams at the tunnel MTU we ship.
- End to end on the cloud deployment: the two experiments that exposed
  defect 5 (block the direct UDP path in the `raw` table so conntrack cannot
  short-circuit it, then confirm recovery over the relay, then confirm the
  upgrade back to direct).

## Alternatives considered

- **Keep LRP and fix defects in place.** Fixes F1, F4 cheaply (and P1 does
  exactly that), but leaves F2, F3, F5 and F7, which are wire-format problems.
- **Adopt Tailscale's DERP protocol, or embed its `derper`.** DERP addresses
  frames by the full 32-byte public key, has a home-relay model and relay
  meshing, and is well proven. As we understand it, it is designed around its
  own coordination server's node keys and region map; adopting it means taking
  on that model and its dependencies for a small fraction of what we use.
  FERRY/1 borrows the good parts (server-stamped source, best-effort
  datagram semantics, HTTP-upgrade transport) without the mesh. We have not
  evaluated `derper` in depth, so this is a judgement to revisit if we later
  want relay meshing.
- **32-byte IDs in every frame.** Removes the prefix question entirely but
  costs 48 extra bytes per packet. 64 bits plus rejection of a colliding
  *different* key at registration (rule 2) gives the same practical guarantee
  for the price of 12 bytes.
- **QUIC only.** Simplifies the protocol, but QUIC is UDP, and the relay
  exists for networks where direct UDP fails. The TCP transport must stay.
- **Encrypt the relay hop.** Payloads are already WireGuard ciphertext. Hop
  encryption would only hide metadata from the operator, which is a non-goal.

## Security analysis

Closes:

- Displacing a verified peer by a token holder (F1), through the
  "no session before Proof" and takeover rules.
- ID-prefix brute force (F2): the brute-force cost rises from about 2^32 to
  about 2^64 key generations, and even a successful match cannot displace a live
  incumbent.
- Confused-deputy routing (F3): one meaning per field, `SrcID` only ever set by
  the relay.
- Token disclosure to an on-path observer and the QUIC MITM (F5).
- A slow receiver stalling unrelated senders (F4).

Remains open:

- The relay sees metadata: who connects, when, how much.
- A valid member can still flood the relay. Per-session rate limits are future
  work.
- The token is a shared secret. Losing it lets an attacker use relay capacity
  (not impersonate a peer); rotation needs a coordinated change.
- Availability under a deliberate 64-bit collision squatting an offline
  victim's ID is theoretically possible at about 2^64 cost and is accepted.

## Consequences

- Relayed packets carry 12 more bytes of header. Direct paths are unaffected.
- Relay and agent releases must follow the phase table; skipping P1 makes P2
  agents talk to a relay that only knows LRP.
- The protocol gains a real version field, so later changes (multi-relay,
  rate limits) no longer need another flag day.
- One name, `FERRY`, replaces five. Renaming touches the binary, config keys,
  metrics and docs, and is staged in P4 so it can lag behind the protocol
  work.

## Future work

- **Relay descriptors in the netmap**: a list of relays with region and
  fingerprint instead of one URL, so agents pick the lowest-RTT one.
- **Home relay and relay-to-relay forwarding** (the DERP model), once one
  relay per deployment is no longer enough.
- **Per-session rate limits and abuse controls.**
- **Seamless relay-to-direct upgrade.** Today the upgrade is a full probe
  restart with backoff because the signaling has no "upgrade only" flag. A flag
  in the SYN would let the responder run ICE alone while the relay keeps
  carrying traffic, removing the brief interruption per attempt.

## Open questions

1. **Transport state strings.** Rename `lrp-ready` to `relay-ready`, or to
   `ferry-ready`? The Apple client reads these strings, so the answer decides the
   deprecation plan.
2. **Should P1 (backpressure) be released ahead of everything else?** It needs no
   agent change and removes the only finding that can hurt other peers today.
3. **TLS on the all-in-one path.** `latticed --standalone` embeds the relay; do we
   generate and pin a self-signed certificate automatically, or require the
   operator to supply one?
4. **Token rotation.** Accept two tokens for a window, or move to per-workspace
   relay credentials issued by the control plane at registration?
5. **F9.** What is the largest relayed packet at the shipped MTU? The answer
   decides between an MTU advertisement and a stream fallback.
