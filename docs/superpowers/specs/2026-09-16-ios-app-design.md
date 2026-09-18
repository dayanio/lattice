# iOS App Design

## Context

The macOS client (`apple/LatticeMac/`) has accumulated a rich feature set across several rounds of work: a menu-bar panel with a peer list, `PeerDetailView` (device details, rename, disable/delete, and a static-endpoint pin), `NetworkPages.swift` (Exit Node / subnet route selection), `QRScanner.swift` (QR + paste join flow), `ChatWindow.swift`/`ChatViewModel.swift` (an AI assistant sidebar), and `DesignComponents.swift` (the shared design system — palette, badges, hover cards).

The iOS side (`apple/Lattice/`) is currently a 3-file scaffold (`LatticeApp.swift`, `TunnelManager.swift`, `ContentView.swift`) that runs the real mesh engine through the shared gomobile-bound `apple/engine` and a Network Extension tunnel (`apple/LatticeTunnel/PacketTunnelProvider.swift`), with no meaningful UI beyond proving the engine runs. This spec designs the iOS app's first real version.

## Goal

Ship an iOS app covering the core end-user connectivity scenarios: join a network, see connection status and the peer list, view a peer's read-only details, and pick an Exit Node. Device management (rename/disable/delete) and the AI assistant are explicitly out of scope for this version — those are admin/power-user features that stay on macOS/the web console for now.

## Scope

**In scope:**
- Join a network via QR code or pasted server URL + token
- Connect/disconnect toggle with live status
- Peer list with connection-quality indicator (direct / relayed)
- Read-only peer detail (name, address, public key, platform, last seen, connection quality)
- Exit Node / subnet route selection
- Leave network

**Out of scope (deferred, not forgotten):**
- Device management actions (rename, disable, delete, static-endpoint pin) — admin-facing, stays on macOS/web for now
- AI assistant (chat sidebar) — power-user feature, no phone-first interaction model designed for it yet
- In-app privacy-policy screen or other App Store submission artifacts — a future submission's concern, not a v1 feature; noted below so it isn't forgotten when that day comes

## Architecture: shared business logic, platform-specific screens

Three options were considered:

1. **Shared logic layer + platform-specific screens (chosen).** Move what's genuinely platform-agnostic into `apple/Shared/`: the `LatticeAPI` HTTP client (already platform-agnostic as written), the `TunnelManager` state-management pattern (NE tunnel lifecycle, connection-quality polling), and `DesignComponents`' design tokens (palette, badges, status dots). Each platform writes its own screen compositions on top — macOS keeps its menu-bar popover, iOS gets new tab-bar + push-navigation screens built for this design.
2. **One adaptive SwiftUI view tree switching on size class.** Rejected: forcing one view hierarchy to serve both a popover and a full-screen tab app produces `if idiom == .pad`-style branching that degrades both platforms and risks destabilizing the working macOS code for marginal reuse.
3. **Fully separate codebases, no sharing.** Rejected: violates DRY: every future feature (e.g. a device-management screen later) would need to be built twice from scratch, including bugs.

Concretely, this means:
- `apple/Shared/TunnelCore.swift` (already exists) gains the iOS `TunnelManager`'s NE-lifecycle logic, refactored out of `apple/Lattice/TunnelManager.swift` where it's currently platform-specific, so both platforms' `TunnelManager` become thin platform wrappers over shared core logic.
- `apple/LatticeMac/LatticeMacApp.swift`'s `LatticeAPI` class moves to `apple/Shared/` unchanged (it's pure `URLSession` + JSON, no AppKit/UIKit dependency) and both platforms reference the same type.
- A new `apple/Shared/DesignComponents.swift` (or the existing macOS one relocated) carries the palette/badge/status-dot tokens; iOS's new screens use these same tokens rather than inventing a second visual language.
- iOS's own screen files (new, under `apple/Lattice/`) are written fresh for tab-bar + push navigation — `ContentView.swift` is NOT ported, it's replaced.

## Navigation structure

`TabView` with two tabs:

- **Status** (home): connection toggle, status text, own overlay IP, peer list. Tapping a peer pushes to Peer Detail.
- **Settings**: server/workspace info (read-only), Exit Node selection (pushes to a route-selection screen), Leave Network, app version.

The join flow is a full-screen modal (`.fullScreenCover`), shown automatically when the app has no saved join state — mirrors macOS's `showingJoin` gate — not part of the tab bar.

## Screens

### Status tab
- Connection toggle (large switch or button) + human-readable state (connecting / connected / disconnected), matching the state vocabulary `TunnelManager` already exposes on macOS.
- Own overlay IP, shown once connected.
- Peer list below: each row shows name, a status dot (`DesignComponents`' `HaloDot` token), and a quality badge sourced from the shared `TunnelManager`'s per-peer connection-state map (the same `ConnectionStates()` data macOS's `PeerRow` already reads) — "direct" for ICE-ready, "relayed" for LRP-ready.
- Pull-to-refresh re-fetches the peer list via `LatticeAPI.shared.listPeers()`; also refresh on `scenePhase == .active` (foreground) since the main app process is suspended in the background and cannot run its own polling timer.

### Peer detail (pushed)
- Read-only: display name, overlay address, public key (tap to copy), platform, last seen, connection quality.
- No action buttons (no rename/disable/delete) — this is the explicit scope boundary from device management.

### Settings tab
- Read-only server/workspace summary (server URL, workspace display name).
- "Exit Node" row → pushes to a route-selection screen, listing peers that have advertised routes (mirrors macOS `NetworkSettingsView`'s data source, `LatticeAPI.shared`'s advertised-routes/route-selection endpoints — already platform-agnostic, no new API surface needed).
- "Leave Network" — clears local join state and tears down the tunnel/VPN profile; a destructive confirmation dialog, matching macOS's delete-confirmation pattern.
- App version / build number footer.

### Join flow (full-screen modal)
- Two entry points: "Scan QR" (camera, `AVFoundation`) and "Paste" (a text field accepting either a raw server-URL+token pair or a full join-link payload, parsed the same way macOS's `QRScanner.swift`/paste-handling already parses it — that parsing logic is UI-framework-agnostic and can move to `apple/Shared/` too).
- On success, saves join state and dismisses into the Status tab.

## iOS-specific engineering constraints

- **NE extension memory ceiling.** iOS Packet Tunnel Provider extensions run under a materially tighter memory budget than macOS's (historically on the order of 15MB, exact figure varies by iOS version and should be re-verified against current documentation before relying on a number). The shared Go engine already runs inside this budget today (proven by the existing iOS scaffold actually tunneling traffic) — this is a constraint to respect when extending engine-side buffering or connection-quality polling frequency, not a blocker for this spec's UI-only scope.
- **Background suspension.** The main app process is suspended when backgrounded; only the NE extension keeps running. The Status tab's peer list must refresh on foreground/pull-to-refresh, never assume a background timer keeps it warm.
- **Camera permission.** QR scanning requires `NSCameraUsageDescription` in the iOS target's Info.plist, added via `apple/project.yml`'s `Lattice` target `info.properties` (or equivalent key already used for other permission strings in that file — check the existing structure before adding).

## App Store considerations (noted, not built now)

VPN apps face additional App Review scrutiny (a stated justification for the `NEPacketTunnelProvider`/VPN entitlement, a privacy policy URL in App Store Connect metadata). None of this requires an in-app screen for a first version — it's submission-time paperwork handled through App Store Connect, not a UI requirement. Flagged here so it isn't forgotten when the app is actually ready to submit, but it adds no tasks to this spec's implementation plan.

## Out of scope, explicitly deferred

- Device management (rename/disable/delete/static-endpoint) on iOS — no design work done here; if/when this becomes needed, it gets its own spec.
- AI assistant on iOS — no phone-first interaction model has been designed for a chat sidebar; deferred entirely.
- Any App Store submission artifacts beyond what's noted above.
