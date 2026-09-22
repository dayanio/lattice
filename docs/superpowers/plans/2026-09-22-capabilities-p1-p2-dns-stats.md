# 客户端能力补全 P1+P2（LatticeDNS 接线 + 连接统计）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 接通引擎内置的 `*.lattice` DNS 应答器（P1），并把每节点的连接状态/流量计数/RTT 打通到设备详情页（P2）。

**Architecture:** P1 只是接线——packet_tun 的拦截器按 peerSource 是否就绪门控，engine 在 node 建立后把 peer 表递进去。P2 沿用现有 2s 轮询管道：Node.PeerStatsSnapshot()（探针状态 + pathPing RTT + 一次 IpcGet 的计数器）→ 引擎 OnPeerStates JSON → NE 透传 → TunnelManager 双通道发布 → PeerDetailView 环形缓冲 + 迷你折线。

**Tech Stack:** Go（internal/agent、internal/server/transport、apple/engine）、Swift/SwiftUI、gomobile（`apple/Scripts/build_framework.sh` 重 bind）。

**Spec:** `docs/superpowers/specs/2026-09-22-mac-client-capabilities-design.md` §二、§三

## Global Constraints

- 每期一个 commit（P1、P2 各一），`git commit -s`，无 Co-Authored-By；提交前跑 lint。
- 改 engine 后必须重跑 `apple/Scripts/build_framework.sh` 重 bind，构建后校验嵌入 framework 哈希。
- UI 文案中文；Swift 文件以 Apache 2.0 头开始。
- 构建命令（`BUILD`，在 `/Users/francis/workspc/lattice/apple` 下）：
  `xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug -destination 'platform=macOS' -derivedDataPath build/mac-dd build 2>&1 | grep -E "error:|BUILD (SUCCEEDED|FAILED)"`
  （注意：**不带** `CODE_SIGNING_ALLOWED=NO`——该产物在本机被 taskgated 拒绝启动。）
- Go 测试：`go test ./internal/agent/wireguard/... ./internal/server/transport/...`；逻辑测试 `bash Scripts/test_apple_logic.sh`。

## File Structure

| 文件 | 职责 | 操作 |
|---|---|---|
| `apple/engine/engine.go` | DNS 接线；pollPeerStates 改发合并指标 | 修改 |
| `apple/engine/packet_tun.go` | 拦截门控从 dnsResolver 改为 peerSource | 修改 |
| `internal/agent/wireguard/ipc_stats.go` | tx 解析；`PeerStatsAllFromIpc` 全 peer 解析 | 修改 |
| `internal/agent/wireguard/ipc_stats_test.go` | 上述两点的单测 | 修改 |
| `internal/server/transport/probe.go` | Probe.rttNano + SetRTT/RTT | 修改 |
| `internal/server/transport/probe_pathping.go` | echo 成功时 SetRTT | 修改 |
| `internal/server/transport/probe_factory.go` | `PeerRTTs()` | 修改 |
| `internal/server/transport/liveness.go` | PeerStats.TxBytes | 修改 |
| `internal/agent/node.go` | GetPeerStats 调用点更新；`PeerStat` + `PeerStatsSnapshot()` | 修改 |
| `apple/LatticeTunnelMac/PacketTunnelProvider.swift` | searchDomains；peerStates 透传；去日志刷屏 | 修改 |
| `apple/LatticeTunnel/PacketTunnelProvider.swift` | 同上（iOS 镜像） | 修改 |
| `apple/Shared/TunnelCore.swift` | `PeerStat` 模型（兼容旧字符串载荷） | 修改 |
| `apple/Shared/TunnelManager.swift` | `peerStats` 发布 + 解析 | 修改 |
| `apple/LatticeMac/ContentView.swift` | 详情页传 stat | 修改 |
| `apple/LatticeMac/PeerDetailView.swift` | 连接质量/流量分区 + 折线 + 速率 | 修改 |
| `apple/LatticeMac/NetworkPages.swift` | LatticeDNS 行解锁 | 修改 |

---

### Task 1（P1）: DNS 接线（Go + Swift 文案）

- [ ] **Step 1: `packet_tun.go` 门控改为 peerSource**

把 `WriteInbound` 里：

```go
	// LatticeDNS: answer *.lattice DNS queries locally instead of tunneling.
	if t.dnsResolver != nil {
```

替换为：

```go
	// LatticeDNS: answer *.lattice DNS queries locally instead of tunneling,
	// once a peer table is wired (engine.go does this right after the node
	// comes up). Without a table there is nothing to resolve from.
	if t.peerSource != nil {
```

并把结构体字段 `dnsResolver` 的注释改为：

```go
	// Custom name answers for server-pushed records — not used yet; the
	// interceptor gates on peerSource (see WriteInbound).
	dnsResolver func(qname string) (string, bool)
```

- [ ] **Step 2: `engine.go` 递 peer 表**

在 `run()` 里 `node.GetNetworkMap = ...` 赋值之前插入：

```go
	// LatticeDNS: the built-in *.lattice responder resolves from the live
	// peer table; without this the interceptor stays dormant.
	t.SetPeerSource(node.GetPeerManager().GetAll)
```

- [ ] **Step 3: 两个 PacketTunnelProvider 加 searchDomains**

Mac 与 iOS 的 `makeSettings` 里，`dns.matchDomains = ["lattice"]` 后各加一行：

```swift
        dns.searchDomains = ["lattice"] // 短名 node-a 自动补全为 node-a.lattice
```

- [ ] **Step 4: `NetworkPages.swift` 解锁 LatticeDNS 行**

把：

```swift
            settingsRow(
                title: "LatticeDNS",
                desc: "用节点名代替 overlay IP 互相访问",
                monoValue: "节点名.mac-demo.lattice.internal",
                trailing: { disabledToggle }
            )
```

替换为：

```swift
            settingsRow(
                title: "LatticeDNS",
                desc: "已开启 · 用节点名代替 overlay IP 互相访问",
                monoValue: "\(DeviceName.normalized(Host.current().localizedName ?? "lattice-mac")).lattice",
                trailing: { EmptyView() }
            )
```

并删除 `disabledToggle`（确认仅此一处使用后）。

- [ ] **Step 5: 验证**

Run: `cd /Users/francis/workspc/lattice && go build ./... && go test ./apple/engine/...`
Expected: 通过（现有 packet_tun DNS 测试两钩子都设，门控切换不受影响）。

---

### Task 2（P2-Go）: 统计数据源（TDD）

- [ ] **Step 1: 写失败测试（`ipc_stats_test.go` 追加）**

```go
func TestPeerStatsAllFromIpc(t *testing.T) {
	ipc := "errno=0\n" +
		"private_key=aaa=\n" +
		"listen_port=51820\n" +
		"public_key=0102\n" +
		"allowed_ip=10.96.0.2/32\n" +
		"last_handshake_time_sec=1700000000\n" +
		"last_handshake_time_nsec=500\n" +
		"rx_bytes=100\n" +
		"tx_bytes=200\n" +
		"public_key=0304\n" +
		"rx_bytes=7\n"
	all := PeerStatsAllFromIpc(ipc)
	if len(all) != 2 {
		t.Fatalf("peers = %d, want 2", len(all))
	}
	a := all["0102"]
	if a.RxBytes != 100 || a.TxBytes != 200 {
		t.Fatalf("peer 0102 counters = %d/%d, want 100/200", a.RxBytes, a.TxBytes)
	}
	if a.LastHandshake.Unix() != 1700000000 {
		t.Fatalf("peer 0102 handshake = %v", a.LastHandshake)
	}
	if b := all["0304"]; b.RxBytes != 7 || b.TxBytes != 0 || !b.LastHandshake.IsZero() {
		t.Fatalf("peer 0304 = %+v", b)
	}
}
```

并在现有 `TestPeerStatsFromIpc`（若存在断言 rx 的用例）旁补 tx 断言：新签名下 `txBytes` 返回值。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/agent/wireguard/...`
Expected: FAIL（`PeerStatsAllFromIpc` 未定义）。

- [ ] **Step 3: 实现 `ipc_stats.go`**

`PeerStatsFromIpc` 签名加 tx（第 4 个返回值 `txBytes uint64`，`case "tx_bytes"` 解析），并新增：

```go
// IpcPeerStats is one peer's WireGuard counters as reported by IpcGet.
type IpcPeerStats struct {
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

// PeerStatsAllFromIpc parses every peer section of an IpcGet dump, keyed by
// the peer public key's hex encoding (IpcGet emits keys hex-encoded). Device
// lines before the first public_key are ignored.
func PeerStatsAllFromIpc(ipc string) map[string]IpcPeerStats {
	out := map[string]IpcPeerStats{}
	var (
		curKey    string
		cur       IpcPeerStats
		sec, nsec int64
	)
	commit := func() {
		if curKey == "" {
			return
		}
		if sec != 0 || nsec != 0 {
			cur.LastHandshake = time.Unix(sec, nsec)
		}
		out[curKey] = cur
		curKey, cur, sec, nsec = "", IpcPeerStats{}, 0, 0
	}
	for _, line := range strings.Split(ipc, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			commit()
			curKey = v
			continue
		}
		if curKey == "" {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				cur.RxBytes = n
			}
		case "tx_bytes":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				cur.TxBytes = n
			}
		}
	}
	commit()
	return out
}
```

同步更新调用方 `node.go` 的 `GetPeerStats` 闭包（4 返回值）与 `transport.PeerStats`（liveness.go 加 `TxBytes uint64`，闭包里填上）。

- [ ] **Step 4: RTT 进 Probe**

`probe.go` 的 Probe 结构（epoch 原子字段附近）加：

```go
	// rttNano records the latest direct-path echo RTT (nanoseconds; 0 =
	// unknown / not measured yet). Written by startPathPing's echo loop.
	rttNano atomic.Int64
```

并加方法：

```go
// SetRTT records the latest direct-path echo RTT (d<=0 clears it).
func (p *Probe) SetRTT(d time.Duration) {
	if d <= 0 {
		p.rttNano.Store(0)
		return
	}
	p.rttNano.Store(int64(d))
}

// RTT returns the latest measured direct-path RTT, 0 if none.
func (p *Probe) RTT() time.Duration { return time.Duration(p.rttNano.Load()) }
```

`probe_pathping.go` 的 echo 成功分支（`armed, unanswered = true, 0` 处）加一行 `p.SetRTT(rtt)`。

`probe_factory.go` 的 `PeerConnectionStates` 后加：

```go
// PeerRTTs snapshots each tracked peer's latest direct-path echo RTT in
// milliseconds, keyed by remote AppID. 0 = not measured (older agent,
// relayed path, or no echo yet).
func (p *ProbeFactory) PeerRTTs() map[string]int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]int64, len(p.probes))
	for appID, probe := range p.probes {
		out[appID] = probe.RTT().Milliseconds()
	}
	return out
}
```

- [ ] **Step 5: `Node.PeerStatsSnapshot`（`node.go`，ConnectionStates 旁）**

```go
// PeerStat is one remote peer's merged connection metrics for embedded
// engine clients (Apple NE UI).
type PeerStat struct {
	State        string
	RxBytes      uint64
	TxBytes      uint64
	HandshakeAgo int64 // seconds since the last handshake; -1 = never
	RttMs        int64 // latest direct-path echo RTT; 0 = unknown
}

// PeerStatsSnapshot merges the probe lifecycle states, direct-path RTTs and
// the WireGuard device's per-peer counters into one snapshot keyed by remote
// AppID. Counters come from a single in-process IpcGet (no UAPI socket —
// same reason as GetPeerStats).
func (c *Node) PeerStatsSnapshot() map[string]PeerStat {
	out := map[string]PeerStat{}
	if c.probeFactory == nil {
		return out
	}
	states := c.probeFactory.PeerConnectionStates()
	rtts := c.probeFactory.PeerRTTs()
	var ipc map[string]wireguard.IpcPeerStats
	if c.iface != nil {
		if conf, err := c.iface.IpcGet(); err == nil {
			ipc = wireguard.PeerStatsAllFromIpc(conf)
		}
	}
	for _, p := range c.GetPeerManager().GetAll() {
		if p == nil || p.AppID == "" {
			continue
		}
		stat := PeerStat{State: "none", HandshakeAgo: -1}
		if s, ok := states[p.AppID]; ok {
			stat.State = s
		}
		if r, ok := rtts[p.AppID]; ok {
			stat.RttMs = r
		}
		if ipc != nil && p.PublicKey != "" {
			if key, kerr := wgtypes.ParseKey(p.PublicKey); kerr == nil {
				if wg, ok := ipc[hex.EncodeToString(key[:])]; ok {
					stat.RxBytes = wg.RxBytes
					stat.TxBytes = wg.TxBytes
					if !wg.LastHandshake.IsZero() {
						stat.HandshakeAgo = int64(time.Since(wg.LastHandshake).Seconds())
					}
				}
			}
		}
		out[p.AppID] = stat
	}
	return out
}
```

（`encoding/hex`/`wgtypes` 若未导入则补。）

- [ ] **Step 6: 验证**

Run: `go test ./internal/agent/wireguard/... ./internal/server/transport/... && go build ./...`
Expected: 全部 PASS / 构建成功。

---

### Task 3（P2-引擎）: `pollPeerStates` 改发合并指标

`engine.go`：`pollPeerStates` 整体替换为：

```go
// peerStatJSON is one peer's merged metrics as pushed to Swift. omitempty
// keeps never-handshaked / unmeasured fields out of the payload.
type peerStatJSON struct {
	State        string `json:"state,omitempty"`
	Rx           uint64 `json:"rx,omitempty"`
	Tx           uint64 `json:"tx,omitempty"`
	HandshakeAgo int64  `json:"handshakeAgo,omitempty"`
	RttMs        int64  `json:"rtt,omitempty"`
}

// pollPeerStates watches each peer's merged connection metrics (lifecycle
// state, WireGuard counters, direct-path RTT) and pushes the snapshot to
// Swift whenever it changes. The handshake age is bucketed to 10 s so an
// idle mesh produces identical snapshots and emits nothing.
func (e *Engine) pollPeerStates(ctx context.Context, node *latticeagent.Node) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			merged := node.PeerStatsSnapshot()
			if len(merged) == 0 {
				continue
			}
			out := make(map[string]peerStatJSON, len(merged))
			for appID, s := range merged {
				hs := s.HandshakeAgo
				if hs < 0 {
					hs = 0
				}
				if hs > 0 {
					hs = hs / 10 * 10
				}
				out[appID] = peerStatJSON{
					State: s.State, Rx: s.RxBytes, Tx: s.TxBytes,
					HandshakeAgo: hs, RttMs: s.RttMs,
				}
			}
			blob, err := json.Marshal(out)
			if err != nil {
				continue
			}
			if string(blob) == last {
				continue
			}
			last = string(blob)
			if e.delegate != nil {
				e.delegate.OnPeerStates(last)
			}
		}
	}
}
```

`EngineDelegate.OnPeerStates` 的 doc 注释同步改为“name → {state,rx,tx,handshakeAgo,rtt} 对象”。

---

### Task 4（P2-Swift）: NE 透传 + TunnelManager + 详情页

- [ ] **Step 1: 两个 PacketTunnelProvider 的 `handleAppMessage` 透传**

Mac（iOS 同步改）把：

```swift
            let states = (try? JSONSerialization.jsonObject(with: Data(latestPeerStates.utf8))) as? [String: String] ?? [:]
```

替换为：

```swift
            // latestPeerStates 是 name→metrics 对象的 JSON——原样解成对象塞进
            // 信封（字段由引擎定义，这里不感知具体结构）。
            let states = (try? JSONSerialization.jsonObject(with: Data(latestPeerStates.utf8))) as? [String: Any] ?? [:]
```

`onPeerStates` 里删除 `TunnelLog.write("peer states: ...")`（2s 一条会把日志刷爆）。

- [ ] **Step 2: `TunnelCore.swift` 加 PeerStat（兼容旧载荷）**

```swift
/// One peer's merged connection metrics from the tunnel process (engine
/// pollPeerStates payload). Absent fields = unknown. Also decodes the older
/// extension payload shape where the value was just the state string.
struct PeerStat: Codable {
    var state: String?
    var rx: UInt64?
    var tx: UInt64?
    var handshakeAgo: Int64?
    var rtt: Int64?

    private enum CodingKeys: String, CodingKey {
        case state, rx, tx, handshakeAgo, rtt
    }

    init(state: String? = nil, rx: UInt64? = nil, tx: UInt64? = nil,
         handshakeAgo: Int64? = nil, rtt: Int64? = nil) {
        self.state = state
        self.rx = rx
        self.tx = tx
        self.handshakeAgo = handshakeAgo
        self.rtt = rtt
    }

    init(from decoder: Decoder) throws {
        if let legacy = try? decoder.singleValueContainer().decode(String.self) {
            state = legacy
            return
        }
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = try c.decodeIfPresent(String.self, forKey: .state)
        rx = try c.decodeIfPresent(UInt64.self, forKey: .rx)
        tx = try c.decodeIfPresent(UInt64.self, forKey: .tx)
        handshakeAgo = try c.decodeIfPresent(Int64.self, forKey: .handshakeAgo)
        rtt = try c.decodeIfPresent(Int64.self, forKey: .rtt)
    }
}
```

- [ ] **Step 3: `TunnelManager.swift`**

`ProviderSnapshot.peerStates` 类型改 `[String: PeerStat]`；加发布属性：

```swift
    /// Per-peer merged metrics (state + counters + RTT) from the tunnel.
    @Published private(set) var peerStats: [String: PeerStat] = [:]
```

`pollPeerStates` 解析处：

```swift
                    self.peerStats = snap.peerStates
                    let states = snap.peerStates.mapValues { $0.state ?? "" }
                    if states != self.peerStates {
                        self.peerStates = states
                    }
```

`removeProfile`/断开清理处同步清 `peerStats`。

- [ ] **Step 4: `ContentView.swift` 详情页传参**

PeerDetailView 调用处（body 的 `if let detail` 分支）加一行参数：

```swift
                    stat: tunnel.peerStats[detail.appID],
```

- [ ] **Step 5: `PeerDetailView.swift` 加连接质量/流量分区**

属性区加：

```swift
    var stat: PeerStat?
    @ObservedObject private var tunnel = TunnelManager.shared
    @State private var rttSamples: [Double] = []
    @State private var prevRx: UInt64?
    @State private var prevTx: UInt64?
    @State private var rxRate: Double = 0
    @State private var txRate: Double = 0
```

body 里 `Divider()` 与 `Text("策略")` 之间插入 `connectionSection`；实现（放在 header MARK 之后）：

```swift
    // MARK: Connection quality & traffic

    private var currentStat: PeerStat? { stat ?? tunnel.peerStats[peer.appID] }

    private var connectionSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("连接质量")
                .font(.caption2.weight(.semibold))
                .foregroundColor(.secondary)
            HStack(spacing: 14) {
                metric("延迟", currentStat?.rtt.flatMap { $0 > 0 ? "\($0) ms" : nil } ?? "—")
                metric("最近握手", handshakeText)
                metric("↑ 速率", rateText(txRate))
                metric("↓ 速率", rateText(rxRate))
                Spacer()
            }
            rttSparkline
            HStack(spacing: 14) {
                metric("累计发送", totalText(currentStat?.tx))
                metric("累计接收", totalText(currentStat?.rx))
                Spacer()
            }
            .font(.caption2)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .onReceive(tunnel.$peerStats) { _ in sample() }
    }

    private func metric(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption2).foregroundColor(.secondary)
            Text(value).font(.system(.caption, design: .monospaced))
        }
    }

    private var handshakeText: String {
        guard let ago = currentStat?.handshakeAgo, ago >= 0 else { return "—" }
        return ago < 60 ? "\(ago)s 前" : "\(ago / 60)m 前"
    }

    private func rateText(_ bytesPerSecond: Double) -> String {
        guard bytesPerSecond > 0 else { return "—" }
        let fmt = ByteCountFormatter()
        fmt.countStyle = .memory
        return fmt.string(fromByteCount: Int64(bytesPerSecond)) + "/s"
    }

    private func totalText(_ bytes: UInt64?) -> String {
        guard let bytes, bytes > 0 else { return "—" }
        let fmt = ByteCountFormatter()
        fmt.countStyle = .file
        return fmt.string(fromByteCount: Int64(bytes))
    }

    /// 60 样本 × 2s 的 RTT 走势；0 表示该样本未测得（probing/中继）。
    private var rttSparkline: some View {
        GeometryReader { geo in
            Path { p in
                let values = rttSamples
                guard values.count > 1, values.max() ?? 0 > 0 else { return }
                let maxV = max(values.max() ?? 1, 1)
                for (i, v) in values.enumerated() {
                    let x = geo.size.width * CGFloat(i) / CGFloat(values.count - 1)
                    let y = geo.size.height * (1 - CGFloat(v / maxV))
                    if i == 0 {
                        p.move(to: CGPoint(x: x, y: y))
                    } else {
                        p.addLine(to: CGPoint(x: x, y: y))
                    }
                }
            }
            .stroke(Color.accentColor, lineWidth: 1.5)
        }
        .frame(height: 26)
    }

    /// 由最新快照采样：RTT 进环形缓冲，计数器差分出瞬时速率（2s 间隔）。
    private func sample() {
        guard let s = tunnel.peerStats[peer.appID] else { return }
        rttSamples.append(Double(s.rtt ?? 0))
        if rttSamples.count > 60 {
            rttSamples.removeFirst(rttSamples.count - 60)
        }
        if let rx = s.rx, let tx = s.tx {
            if let prx = prevRx, let ptx = prevTx {
                rxRate = max(0, Double(rx &- prx) / 2)
                txRate = max(0, Double(tx &- ptx) / 2)
            }
            prevRx = rx
            prevTx = tx
        }
    }
```

- [ ] **Step 6: 验证**

Run: `BUILD` + `bash Scripts/test_apple_logic.sh`
Expected: SUCCEEDED / all checks passed。

---

### Task 5: 重 bind、全量构建、端到端验证、提交

- [ ] **Step 1: 重 bind framework**

Run: `cd /Users/francis/workspc/lattice/apple && bash Scripts/build_framework.sh`
Expected: macos/ios 两个 xcframework 生成成功（iOS 侧 PacketTunnelProvider 改动一同生效）。

- [ ] **Step 2: 全量构建 + 逻辑测试 + lint**

Run: `BUILD`、`bash Scripts/test_apple_logic.sh`、仓库根 `GOTOOLCHAIN=go1.26.8 bin/golangci-lint run ./internal/... ./cmd/...`
Expected: SUCCEEDED / all checks passed / 0 issues。

- [ ] **Step 3: 校验嵌入 framework 哈希 + 重启 App**

```bash
cmp -s apple/Frameworks/MacOS/LatticeCore.xcframework/macos-arm64_x86_64/LatticeCore.framework/Versions/A/LatticeCore \
  apple/build/mac-dd/Build/Products/Debug/LatticeMac.app/Contents/Frameworks/LatticeCore.framework/Versions/A/LatticeCore
```
Expected: 无输出（一致）。然后 pkill + open 新构建。

- [ ] **Step 4: DNS 端到端（P1）**

隧道在位时：`dig node-a.lattice @10.96.0.1 +short`（引擎 peer 表若仍是旧 netmap，则用旧表里存在的名字，如 `cloud-node-1.lattice`）。
Expected: 返回一个 overlay IP。短名补全（searchDomains）若引起解析异常，回退删除 searchDomains 行并在 commit 说明里注明。

- [ ] **Step 5: 提交（两次）**

P1：`feat(apple): wire the built-in *.lattice DNS responder into the engine`——包含 packet_tun.go、engine.go 接线行、两平台 searchDomains、NetworkPages 行解锁。
P2：`feat(apple): per-peer latency and traffic stats on the device detail page`——包含 ipc_stats/probe/node/engine Go 改动、NE 透传、TunnelManager、详情页 UI。均 `-s`、无 Co-Authored-By。

## Self-Review

- Spec §二（接线/搜索域/文案/非目标）：Task 1、Task 5 Step 4 覆盖；不做自定义记录。
- Spec §三（计数器/RTT/管道/UI/环形缓冲）：Task 2–4 覆盖；流量口径文案在 UI 用“累计”而非“应用层”。
- 类型一致性：`PeerStat`（Swift 模型）≠ `node.PeerStat`（Go），名字撞但各层独立；JSON 字段 state/rx/tx/handshakeAgo/rtt 三层一致。
- 已知人工验证项：详情页曲线/速率的观感、iOS 真机——报告为未验证，不声称通过。
