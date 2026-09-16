# ADR-0004: LRP Per-Peer Authentication via X25519 Challenge-Response

- **Status**: Proposed
- **Date**: 2026-09-17
- **Related**: ADR-0003 (enrollment approval + client-side keygen — control plane), 2026-09-17 codebase review findings #1-#5

## Context

The LRP relay previously accepted any `Register` frame and let the client
claim any peer ID. Commit `a6e75b94` added a **shared token** so at least
deployment membership is required, but a shared token proves *membership*,
not *identity*: every peer in the deployment holds the same secret, so any
valid member can still register as any other member's peer ID and

- receive the victim's relayed traffic (encrypted, but usable for traffic
  analysis and replay-into-WireGuard noise),
- inject garbage frames into the victim's WireGuard receive path,
- blackhole the victim's relay connectivity (the only fallback when ICE
  fails, e.g. symmetric NAT on both sides).

Constraints that shape the solution:

- The relay (`lrper`) is a **standalone** process. It has no access to the
  control plane's peer registry and should stay that way (it is deployed
  independently and must keep working when the control plane is down).
- The system already has a **self-certifying identity**: `PeerID` is the
  first 8 bytes (big-endian) of the peer's WireGuard public key
  (`infra.FromKey`). LRP session IDs, NATS subjects and fake endpoint
  addresses are all derived from it.
- WireGuard keys are X25519 (Curve25519) pairs — **not signing keys** — so
  "sign the register request" is not directly available.

## Decision

Bind a relay session to possession of the peer's WireGuard private key with
an **ephemeral X25519 challenge-response performed at Register time**:

1. Client sends `Register { claimed PeerID, client public key (32 B), shared
   token if configured }`.
2. Relay checks `first-8-bytes(client public key) == claimed PeerID`, then
   replies with `AuthChallenge { 32 B ephemeral X25519 public key }`,
   generated fresh for this connection (its private half never leaves
   memory and is discarded after verification).
3. Client responds `AuthResponse { X25519(client private key,
   relay ephemeral public key) }` (32 B).
4. Relay computes `X25519(relay ephemeral private key, client public key)`
   and accepts the session only on an exact match (constant-time compare,
   reject the all-zero shared secret).

The claimed ID ↔ public key prefix check plus the DH proof together make
impersonation impossible without the victim's private key: a forged public
key with the right 8-byte prefix fails step 4, and a real key of another
peer fails the prefix check.

**The relay needs no control-plane contact and no new secrets.** This works
identically for standalone `lrper`, the `manager lrper` subcommand and the
all-in-one binary.

## Detailed design

### Wire protocol

New frame commands on the control channel (TCP: the upgraded stream; QUIC:
the control stream):

| Cmd            | Value | Direction     | Payload                                  |
|----------------|-------|---------------|------------------------------------------|
| `AuthChallenge`| 0x05  | relay → client| 32 B ephemeral X25519 public key         |
| `AuthResponse` | 0x06  | client → relay| 32 B shared secret (`DH(priv, challenge)`)|

`Register` payload becomes `token (0..512 B) || client public key (32 B)`.
The session is inserted into the `SessionManager` **only after** step 4
succeeds — a half-registered connection must never occupy or replace an ID
slot (this also removes the current last-write-wins hijack window for
unauthenticated connects). On any failure the relay closes the connection;
no error frames (do not help attackers probe).

### Crypto notes

- Use `golang.org/x/crypto/curve25519.X25519()` for both sides.
- Reject an all-zero DH output (low-order point input); log and close.
- The ephemeral keypair is generated with `crypto/rand` per connection and
  zeroed after use; replay is structurally impossible because the challenge
  never repeats.
- `Forward`/`Probe` frames from a connection are ignored until the session
  is fully registered.

### Compatibility and rollout

Same staged pattern as the shared-token change:

1. Ship relay support behind `--require-peer-auth` (default **off**): with
   the flag off, the relay performs the handshake when the client offers it
   but still accepts legacy `Register`-only connections.
2. Ship client support (agent + sandbox paths): clients always perform the
   handshake; if the relay answers a legacy bare-register instead of
   challenging, the client proceeds (server is older).
3. Operators who want the guarantee flip `--require-peer-auth`; from then
   on, unauthenticated registers are rejected with a clear server log line.

No auto-fallback beyond the above: mixed-version clusters degrade to
membership-only auth (today's behavior) rather than failing.

### Testing plan

- Unit: DH proof happy path; forged public key with correct prefix; wrong
  prefix with valid DH; all-zero shared secret; replayed response against a
  second connection; `token || pubkey` payload parsing.
- Integration: real relay on TCP loopback — two clients, one attempts to
  register the other's ID with its own key → rejected; victim's session
  unaffected.
- Race: `-race` on the session manager insertion point (only-after-success
  registration changes the locking window).

## Alternatives considered

- **Control-plane-issued per-peer HMAC credentials** (server signs
  `HMAC(relaySecret, peerID, expiry)` at registration, relay verifies
  locally): single-round-trip and cheap to verify, but requires distributing
  `relaySecret` to every standalone relay deployment and threading the
  credential through registration + netmap + re-registration. The X25519
  design needs zero credential infrastructure and composes with ADR-0003
  (proof of the actual identity key, not a bearer credential derived from
  it).
- **Relay → control-plane validation callback**: adds a runtime dependency
  from the fallback data path to the control plane; a relay must stay
  usable during control-plane outages. Rejected.
- **WireGuard-noise-style full handshake on the relay channel**: strictly
  more security (relay traffic would become encrypted against off-relay
  observers) but a much larger protocol change; the relay already carries
  only WireGuard-encrypted payloads, so hop encryption adds little.

## Security analysis

**Closes**: cross-member session hijack, ID squatting, unauthenticated
relay use (combined with the existing token), registration-based DoS of a
specific victim ID.

**Remains open** (documented non-goals for this ADR):

- The relay still observes ciphertext metadata (who talks to the relay,
  when, volume). Mitigating this means hopping relays / cover traffic — out
  of scope.
- QUIC relay transport uses `InsecureSkipVerify`; a MITM on the relay leg
  can strip traffic. Fix: control plane advertises the relay certificate
  fingerprint (TOFU pinning) — small follow-up ADR.
- A *valid* member can still flood the relay (internal DoS). Mitigation:
  per-session rate limits — future work.

## Consequences

- Registration gains one extra round trip per relay connection (negligible;
  it happens once per connection lifetime, and the auto-reconnect supervisor
  from commit `92d4dca4` is the only caller).
- Relay and agent must both understand the handshake before the flag can be
  enabled (documented upgrade order above).
- With ADR-0003 landed, the relay proves possession of a key the control
  plane never saw — the identity chain becomes end-to-end: client-generated
  key → approved peer → self-certifying relay session.
