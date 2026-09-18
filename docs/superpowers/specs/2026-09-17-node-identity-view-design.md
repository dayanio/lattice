# Node Identity View (Apple Clients) — Design

## Background

While debugging the iOS client's join flow this session, we found and fixed a
real bug: the Go mesh engine (`apple/engine/engine.go`) generated a brand-new
WireGuard keypair on every engine start instead of persisting one, so any
Network Extension restart silently rotated the device's identity. The server
rejects a same-name registration under a different public key ("public key
mismatch ... requires re-enrollment"), so this manifested as a device
permanently locking itself out until an admin deleted its stale peer record.

That bug is fixed (the private key is now persisted to disk inside the
extension's container). This surfaced a separate, pre-existing gap: neither
the macOS nor iOS client has any UI showing a device's own identity
(public key, overlay IP), and there is no user-facing way to rotate that
identity if it's ever suspected to be compromised. This spec covers adding
both.

## Scope

- **Platform**: iOS first (`apple/Lattice/SettingsView.swift`). The
  underlying engine/extension changes are shared with macOS
  (`apple/engine/engine.go`, `apple/LatticeTunnelMac/PacketTunnelProvider.swift`),
  so macOS gains the same capability for free at the engine layer, but wiring
  it into the macOS UI is out of scope for this spec — that's a follow-up if
  wanted.
- **What's shown**: the device's own public key and overlay IP. Not other
  peers' keys (the backend's `/peers/list` already returns each peer's
  `PublicKey`, but wiring that into peer detail views is a separate,
  independent piece of work not covered here).
- **Private key**: never displayed or exportable, anywhere. The UI states
  it's stored securely on-device and stops there.
- **Key rotation**: a manual, infrequent, security-motivated action (suspected
  compromise), not a self-service fix for connectivity problems. It is built
  as a variant of the existing leave-network flow, not a new in-place
  rotation protocol — see Approach below for why.

## Approach

**Why not rotate in place?** The server keys peer identity on
`(name, public_key)` and rejects a same-name re-registration under a
different key outright (see `registerStandalone` in
`internal/server/service/peer.go` — this is the exact check this session hit
repeatedly). Building a "rotate this peer's key via an authenticated API call"
endpoint would need new server-side surface, a client identity-transfer proof,
and careful handling of the transition window. Given rotation is meant to be
rare and security-motivated, reusing the already-correct, already-tested
leave-and-rejoin path is far less code and no new attack surface. The only
thing leave-and-rejoin doesn't already do is discard the old key — that's the
one piece this spec adds.

### 1. Engine: expose the public key, add a reset hook

`apple/engine/engine.go`:
- Store the loaded/generated `privKey` on the `Engine` struct (currently a
  local variable in `run()`).
- Add `func (e *Engine) PublicKey() string` — returns the base64 public key
  string once the engine has started (empty string before then). Gomobile
  exports this automatically as a method on the bound `Engine` type.
- Add `func ResetIdentity() error` (package-level, callable before an engine
  even exists) — deletes the persisted key file at
  `wgIdentityDir()/wg-identity.key` if present. Not an `Engine` method: the
  reset has to happen *before* `NewEngine`/`Start`, when there may be no
  engine instance yet.

### 2. Tunnel extension: serve identity over the existing provider-message channel, honor a reset flag

Both `apple/LatticeTunnel/PacketTunnelProvider.swift` (iOS) and
`apple/LatticeTunnelMac/PacketTunnelProvider.swift` (macOS):
- `handleAppMessage`'s existing `"peerStates"` response envelope gains two
  more fields: `publicKey` (from `engine?.publicKey() ?? ""`) and
  `overlayIP` (the already-tracked `currentOverlayIP`). No new message
  string — one round trip still returns everything the app needs, on the
  same 2-second poll that already exists for connection quality.
- `startTunnel`: before constructing `EngineConfig`/`LatticeEngineEngine`,
  check `pc["resetIdentity"] as? Bool == true`. If set, call the gomobile-
  exported `EngineResetIdentity()` (ignore its error — a missing file is not
  a failure) before proceeding with the normal registration flow. This is a
  one-shot flag: it is not persisted by the extension and the app is
  responsible for only setting it on the one rejoin call that should reset
  identity.

### 3. TunnelManager: poll identity, thread the reset flag through saveJoin

`apple/Shared/TunnelManager.swift`:
- Add `@Published private(set) var localPublicKey: String = ""` and
  `@Published private(set) var localOverlayIP: String = ""`.
- Extend the existing `pollPeerStates()` timer (already ticking every 2s
  while connected) to also send the `"identity"` message and update these
  two properties from the response. One provider-message round trip per
  tick would double the traffic; instead, fold identity into the *same*
  `ProviderSnapshot` response the `"peerStates"` message already returns —
  add `publicKey`/`overlayIP` fields to `ProviderSnapshot` on both the
  Swift (`TunnelManager`) and extension (`PacketTunnelProvider`) sides, and
  drop the separate `"identity"` message type from step 2. (This revises
  step 2: one message type, richer payload, not two message types.)
- `saveJoin(serverURL:token:name:completion:)` gains a fifth parameter
  `resetIdentity: Bool = false`, included in the `providerConfiguration`
  dict passed to `createProfile` only when `true` (omit the key entirely
  when `false`, so existing non-reset joins are byte-for-byte unchanged).

### 4. Settings UI

`apple/Lattice/SettingsView.swift`, new section between the existing
"网络" section and "偏好" section, titled "设备身份" — shown only when
`tunnel.isConfigured` (mirrors the existing "网络" section's guard):

- `LabeledContent("公钥", value: <truncated>)` — truncate to first 8 +
  last 8 base64 chars with `…` between (WireGuard keys are 44 chars,
  too long for a settings row). Row is tappable; tapping copies the full
  key to the clipboard via `UIPasteboard.general.string` and briefly
  flips a `@State` checkmark/label ("已复制").
- `LabeledContent("Overlay IP", value: tunnel.localOverlayIP)`.
- A line of caption text: "私钥已在本机安全存储，不会显示或导出。"
- `Button("重新生成密钥", role: .destructive)` — opens a
  `.confirmationDialog` ("重新生成密钥？", message: "这会清除当前设备身份，
  需要重新扫码或输入令牌入网。仅在怀疑密钥泄露时使用。") with a destructive
  "重新生成" action and a "取消" action. On confirm:
  1. `tunnel.disconnect()`
  2. `tunnel.removeProfile { ... }` (same cleanup `leaveNetwork()` already
     does: clear `lattice.serverURL`/`nodeName`/`authToken`/`adminUser`/
     `workspaceId`, delete the keychain password, `tunnel.load()`)
  3. Set a local `@State pendingIdentityReset = true` and present
     `JoinView` (`showingJoin = true`, `mode: .scan`)
  4. `JoinView.saveAndConnect()` needs a way to know this join should pass
     `resetIdentity: true`. Simplest: add an `initialResetIdentity: Bool =
     false` parameter to `JoinView.init`, stored as a `let`, passed straight
     through to `TunnelManager.shared.saveJoin(...)`. `SettingsView` passes
     `initialResetIdentity: true` only from this one call site; every other
     `JoinView(...)` construction in the codebase keeps the default `false`.

## Data Flow Summary

```
User taps "重新生成密钥" → confirm
  → TunnelManager.disconnect() + removeProfile()
  → JoinView(mode: .scan, initialResetIdentity: true) sheet
  → user scans/enters server+token
  → TunnelManager.saveJoin(..., resetIdentity: true)
  → providerConfiguration["resetIdentity"] = true
  → PacketTunnelProvider.startTunnel reads the flag
  → calls EngineResetIdentity() (deletes wg-identity.key)
  → NewEngine/Start(): loadOrCreatePrivateKey() finds no file → generates
    and persists a brand-new key
  → registers as what the server sees as a fresh device (new key, existing
    name still present server-side — this still requires the server-side
    stale-key peer record situation this session hit; that pre-existing
    server behavior is unchanged by this spec and is not a new failure
    mode introduced here, it's the same "same name, new key" case a normal
    key rotation always has to go through on this backend)
```

## Testing

- Go: unit test for `ResetIdentity()` — write a key file, call it, assert
  the file is gone; call it again with no file present, assert no error.
- Go: unit test that `Engine.PublicKey()` returns `""` before `Start()` and
  the correct derived public key after a `NewEngine` call with a known
  config (no real network needed — `PublicKey()` only reads the in-memory
  key, doesn't require registration to succeed).
- Manual (no XCTest UI harness in this project, per existing project
  convention): build and run on-device, verify the identity section shows a
  plausible-looking public key and matches what the server has on record for
  that peer (cross-check via the admin API/DB), verify copy-to-clipboard,
  verify the reset flow ends with a *different* public key registered for
  the same device name.

## Out of Scope

- Showing other peers' public keys in peer detail views (data already exists
  server-side; separate follow-up).
- macOS Settings/menu-bar UI for the same feature (engine/extension changes
  are shared, but no macOS UI work is included here).
- Any server-side changes. The backend's existing accept/reject behavior for
  key rotation is unchanged.
