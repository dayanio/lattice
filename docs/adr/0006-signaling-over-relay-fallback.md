# ADR-0006: Peer signaling falls back to the relay

- **Status**: Proposed
- **Date**: 2026-09-19
- **Related**: ADR-0005 (FERRY relay protocol; this ADR makes its "`Probe` stays
  as a fallback" line concrete), ADR-0004 (LRP per-peer authentication)

## Summary

Every peer-to-peer signaling packet (`HANDSHAKE_SYN`, `HANDSHAKE_ACK`, `OFFER`,
`ANSWER`, `RESTART_NOTIFY`) travels over NATS. When a node's NATS connection is
dead or half-dead, no probe can be (re)negotiated, even though the node's relay
connection is healthy and would carry the data. We add a second signaling
channel that already exists on the wire, the relay's `Probe` frame, and use it
when the NATS channel is not making progress.

## Context

### What signaling does and how it travels today

- A probe negotiates a transport with one remote peer (ICE and LRP race in
  `Probe.discover`). Both dialers are built with `Sender: p.signal.Send`
  (`probe_factory.go`), which publishes to the remote's NATS subject.
- The relay carries WireGuard packets (`Forward`) and, since the first LRP
  version, signaling packets (`Probe`). Today the only sender of `Probe` is the
  LRP dialer's OFFER/ANSWER (`lrpDialer.sendOfferFromLrp`).
- The receive side is already shared: the relay client's `probeWorker`
  unmarshals a `Probe` payload into a `SignalPacket` and calls
  `node.probeFactory.Handle`, the same entry point NATS messages use
  (`node.go`, `lrp_client.go`). A peer therefore already understands SYN, ACK,
  OFFER, ANSWER and RESTART_NOTIFY arriving over the relay.
- `MESSAGE` packets (netmap pushes) come from the control plane, not from peers,
  and stay NATS-only.

### The failure

Test of 2026-09-19, macOS app on wifi, iPhone switched wifi to cellular, twice:

| Run | Mac app to phone | Phone NATS |
|---|---|---|
| 1 | 108 s | `nats: stale connection`, six reconnects about 17 s apart |
| 2 | 23 s | no reconnects |

In run 1 the Mac noticed the dead direct path after 10 s and sent SYN every 2 s
from then on; the phone received the first one at about 105 s. Meanwhile:

- the phone's relay TCP connection re-registered about 20 s after the switch and stayed healthy;
- the container, which only needs relay data, recovered in 22 s;
- on the server, three of the phone's cellular connections to `:4222` held 485
  unacknowledged bytes in the send queue, while its `:6266` connection had none.

We do not know why those NATS flows were unusable (the working assumption is
the carrier path for new flows to that port). It does not matter for the
design: NATS is a second, independent dependency that gates re-negotiation, and
when it fails the relay is up but unused for signaling.

Related work already done: NATS ping every 5 s with 2 outstanding (a dead
connection is noticed in about 15 s instead of minutes), and the upgrade retry
no longer restarts a probe while signaling is down. Those shorten the failure;
they do not remove the dependency.

## Goals

1. A node whose NATS is unusable, but whose relay session is up, still reaches
   `lrp-ready` with any peer that is also on the relay, and can still upgrade
   to direct through ICE candidates exchanged over the relay.
2. No behaviour change when NATS is healthy, apart from a small amount of extra
   traffic during a probe attempt that is not progressing.
3. Works between new and old agents in both directions, without a relay upgrade.

Non-goals: replacing NATS, relay-to-relay routing, changing what the relay
forwards (ADR-0005), hiding signaling metadata from the relay.

## Decision

### 1. One sender for both dialers

Add `peerSignaler` in `internal/server/transport`. It implements the
`Send(ctx, to PeerID, data []byte) error` shape the dialers already use and
replaces `p.signal.Send` in `probe_factory.go` for both dialers. It owns two
channels:

- NATS: `infra.SignalService.Send`.
- Relay: `Lrp.Send(ctx, to, relay.Probe, data)`, available when the relay
  client is connected. `infra.Lrp` gains `Connected() bool`.

### 2. Escalation rule

The sender cannot see the receiver's NATS: a publish to a peer whose
subscription is dead still succeeds. So the trigger is lack of progress, not the
sender's own NATS health.

For each probe attempt (from `Start` or `restart` until the probe leaves
`probing`):

- send over NATS only, as today;
- once `relaySignalAfter` (2 s, one SYN period) has passed without any
  signaling packet received from that remote, also send every packet over the
  relay while the relay is connected;
- if the local NATS reports `Connected() == false`, or a NATS send returns an
  error, use the relay immediately;
- stop escalating when the probe leaves `probing`, or when a packet from the
  remote arrives over the relay (the relay path is then proven, and it stays
  in use for the rest of the attempt).

"Received anything from the remote" is tracked in `Probe.Handle`, which already
sees every inbound packet regardless of channel.

Sending on both channels from the first packet is simpler and 2 s faster. It is
rejected because it doubles duplicate delivery on every attempt of every healthy
peer, and duplicates are the main risk (below).

### 3. Receiver

No change to the receive path. Packets arriving over the relay reach
`probeFactory.Handle` exactly like NATS ones. The sender identity comes from
`SignalPacket.SenderID`, as on NATS.

Trust: today any authenticated NATS client can publish a packet with any
`SenderID`. The relay is authenticated by the shared relay token, and by
ADR-0004 when peer auth is on. The relay channel is therefore not weaker than
NATS. Hardening comes with ADR-0005: the relay overwrites the source ID on
`Probe` as well as `Forward`, and receivers drop a packet whose `SenderID`
disagrees. It is not part of this change because enforcing it would break the
LRP OFFER/ANSWER path against relays that do not stamp `Probe` yet.

### 4. Duplicates

With two channels a packet can arrive twice, possibly seconds apart. The
handlers are already retransmission-tolerant: SYN is resent every 2 s, ICE
candidates are cached and resent, and an LRP SYN within 5 s of the session
forming is treated as a retransmit (`lrpSynGrace`, commit 20fda9c2). The
remaining risk is a late duplicate SYN arriving on an active session after that
window and being read as "remote restarted", which restarts the probe.

Phase 2 removes the time heuristic: each probe attempt gets a random
`attempt_id` in `SignalPacket` (a new optional field, ignored by old agents).
Receivers treat a SYN with the current attempt's id as a retransmit whatever the
channel or delay, and a different id as a real restart. Old agents send no id
and keep the grace rule.

### 5. Limits and pre-existing issues to fix with it

- `MaxProbePayload` is 2048 bytes. A SYN carries the sender's peer record; its
  size must be measured against that limit before enabling escalation. If it can
  exceed it, raise the limit (the server allows 64 KiB).
- On an oversized `Probe` frame the TCP client logs a warning and returns
  without reading the payload (`lrp_client_tcp.go`), which desynchronises the
  stream. It must discard the payload instead. Escalation makes the path busier,
  so fix it first.
- Relaying to a peer that is not registered logs `relay target not found` at
  warn level once per packet. During escalation that is one line per 2 s per
  offline peer. Rate-limit it.

### 6. Observability

One info log per attempt when escalation starts (`signaling escalated to relay`,
with the reason: no progress, NATS disconnected, or NATS send error), and one
when the first packet arrives over the relay. `lattice status` is unchanged.

## Alternatives considered

- **Move all signaling to the relay.** Loses the NATS dependency, but also
  NATS's role for netmap pushes and presence, and puts every handshake on the
  relay. Rejected by ADR-0005's non-goals.
- **Harden NATS instead** (a second endpoint on a different port, NATS over
  WebSocket, racing connections). Useful and orthogonal, but it only helps when
  the failure is port-specific. It cannot help when the server side of the flow
  is the problem, and the relay is already the connection we trust for data.
- **Always send on both channels.** See Decision 2.
- **Do nothing beyond faster NATS failure detection.** Shortens the outage to
  about 15 s plus reconnect time when it works, but a reconnect can land on
  another unusable flow, as the six cycles in run 1 show.

## Rollout

1. Client only: `peerSignaler`, `Connected()`, the payload limit and discard fix.
   Servers and old agents are untouched, because they already accept `Probe`
   frames. Old agents receive escalated packets fine; an old agent that is the
   one with dead NATS still cannot send over the relay, so the fix helps when the
   new agent is on either end and the relay reaches the other side.
2. `attempt_id` in `SignalPacket`, then remove the dependence on `lrpSynGrace`.
3. Relay stamps `Probe` (ADR-0005), receivers verify.

## Test plan

- Unit: the escalation timeline with a fake NATS sender that swallows packets
  and a fake relay (nothing before 2 s, both channels after, relay at once when
  NATS is disconnected or errors, stops when the probe leaves `probing`);
  duplicate SYN, ACK and OFFER over both channels leave one session.
- Integration: two agents and one relay, NATS between them dropped in both
  directions, probe reaches `lrp-ready`; then ICE candidates over the relay
  reach `ice-ready`.
- Live, reproducible without waiting for cellular flakiness: on the cloud host,
  drop the container's traffic to `:4222` with an `iptables -t raw` rule (the
  test-environment notes explain why `raw`), restart the Mac agent, and expect
  the container and Mac to reach `lrp-ready` within about 10 s of relay
  registration. Remove the rule and verify it is gone.
- Regression: repeat the phone wifi to cellular switch several times; there
  should be no outage over 30 s.

## Open questions

1. Is `relaySignalAfter` = 2 s right, or should the first attempt of a
   restart (where the remote is more likely to be waiting) go on both channels
   at once?
2. Should escalation keep running for the whole `probing` window (up to 65 s)
   or give up after a bounded number of relay sends?
3. Phase 2 needs a `signal.proto` field. Is the generated Go file
   regenerated in-tree, and do the Apple bindings need a rebuild?
