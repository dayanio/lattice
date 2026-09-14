# Exit Node / 子网路由设计（standalone 模式）

**日期**：2026-09-14
**状态**：Draft
**范围**：`internal/server`（standalone 控制面）+ `apple/`（macOS 客户端）
**关联文档**：[对标 Tailscale 的功能差距与路线图](./2026-09-13-apple-client-vs-tailscale-gap-roadmap.md)（阶段二）、[UI mockup](./2026-09-13-apple-client-ui-mockups.md)（§02 网络设置）

## 一、背景

路线图阶段二把 Exit Node 和子网路由列为一组：两者都是"某个 peer 声明自己能代理一段 CIDR"，区别只是 Exit Node 声明 `0.0.0.0/0`（全部流量），子网路由声明具体网段（比如 `192.168.1.0/24`）。

摸代码后发现这不是一张白纸，但也不能直接照抄现成的：

- `AllowedIPs` 在 CRD 和 agent 侧（`internal/agent/provision/provisioner.go:116`、`internal/agent/infra/device_conf.go:121`）本来就支持一个 peer 挂多段 CIDR，链路是通的
- 已有的 CIDR 广播机制——`internal/agent/controller/peering_controller.go` 的 `NetworkPeeringReconciler`（网关 + 影子 peer）——只在 K8s（`manager`）模式下跑，且解决的是"两个不同 `LatticeNetwork` 互通"这个更复杂的问题
- macOS 客户端连的是 `latticed --standalone`，走 `internal/server/reconcilers/netmap_builder.go`，这条路径上现在是写死 `peer.AllowedIPs = p.Address + "/32"`（`dbToInfraPeer`），没有任何路由广播逻辑
- `apple/LatticeMac/NetworkPages.swift` 的 `NetworkSettingsView` 已经是照 mockup 做好的 UI 壳子（禁用态 + "即将推出"角标），等这次把背后的行为接上

## 二、目标 / 非目标

**目标**：standalone 模式下，一个 peer 可以声明"我能代理 X 网段"，其它 peer 可以各自选择"我要用谁的这段路由"，选中后这段路由体现在选择者的 WireGuard `AllowedIPs` 里，macOS 客户端能把它转成系统路由。

**非目标**：
- 不碰 K8s 的 `LatticeNetworkPeering`/`NetworkPeeringReconciler`——那是跨 `LatticeNetwork` 的场景，比这次要做的复杂，保持独立
- 不做"管理员强制下发路由"（Tailscale 企业版的 auto-approve 策略）——这次只做"提供方声明 + 使用方手动选择"两段式
- 不做 MagicDNS——路线图里是独立一项，另起一份设计

## 三、关键设计决定：广播 vs 逐个选择

**决定：逐个选择，不广播。** 一个 peer 声明自己能代理某段路由后，不会自动出现在所有人的路由表里——必须由每个"使用方"主动选择"我要用 provider X 的这条路由"，才会体现在使用方自己的 `AllowedIPs` 里。

理由：如果自动广播，任何人打开 Exit Node 开关，都会把全网所有设备的默认路由改到他机器上，这不是任何人期望的行为，也和已经做好的 mockup（`NetworkSettingsView` 里"使用退出节点 › 点击选择"是个选择器，不是全局开关）对不上。

## 四、数据模型

`internal/server/models/peer.go` 的 `Peer` 结构体加一个字段，跟 `Labels` 同样的 JSON-in-text 模式：

```go
// AdvertisedRoutes is a JSON array of CIDRs this peer offers to route for
// other peers, e.g. ["0.0.0.0/0"] for exit-node, ["192.168.1.0/24"] for a
// subnet route. Empty/absent means this peer offers nothing.
AdvertisedRoutes string `gorm:"type:text" json:"advertised_routes,omitempty"`
```

新增一张表，记录"谁选了用谁"：

```go
// PeerRouteSelection records that ConsumerPeerID has opted to accept
// ProviderPeerID's AdvertisedRoutes. One row per (consumer, provider) pair;
// a consumer may select more than one provider (e.g. a subnet route from
// one peer and an exit node from another) but a peer cannot select itself.
type PeerRouteSelection struct {
	Model
	WorkspaceID    string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ConsumerPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ProviderPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
}

func (PeerRouteSelection) TableName() string { return "t_peer_route_selection" }
```

两个字段都走 GORM `AutoMigrate`（`internal/db/gormstore/store.go` 现有模式），不需要手写迁移脚本。

## 五、服务端行为

### 5.1 `netmap_builder.go` 改成按接收方个性化展开

`BuildForPeer(ctx, peer *models.Peer)`（`internal/server/reconcilers/netmap_builder.go:96`）现在对 workspace 里每个 `row` 都调用 `dbToInfraPeer(row)` 得到统一的 `AllowedIPs = row.Address + "/32"`，不区分是给谁看的。

改法：在 `BuildForPeer` 的循环里，对每个 `row`，查 `row.ID` 是否是 `peer.ID`（当前接收方）在 `t_peer_route_selection` 里选中的 provider；如果是，把 `dbToInfraPeer(row)` 算出的 `AllowedIPs` 从 `/32` 追加上 `row.AdvertisedRoutes` 里的 CIDR。**只影响这一份给 `peer` 的 netmap**，其它没选中这个 provider 的接收方，看到的 `row.AllowedIPs` 还是原来的 `/32`。

一次查询即可（`SELECT provider_peer_id FROM t_peer_route_selection WHERE workspace_id=? AND consumer_peer_id=?`），不会在循环里对每个 row 单独查库。

### 5.2 新增两个 API（`internal/server/service/peer.go`）

```
POST /api/v1/peers/{name}/advertised-routes
  body: { "routes": ["0.0.0.0/0"] }        # 设为 [] 即取消声明

POST /api/v1/peers/{name}/route-selection
  body: { "provider": "node-b", "selected": true }
```

第一个只能由 peer 自己（或其归属用户）调用，写 `AdvertisedRoutes`；第二个由消费方调用，写/删 `t_peer_route_selection` 一行。两者都应该在写入后让该 peer 的 `ConfigVersion` 失效，触发下一次心跳/轮询拿到新 netmap——复用现有的 `versionFor` 机制，不需要新的推送通道。

## 六、macOS 客户端行为

### 6.1 `PacketTunnelProvider.swift` 的 `makeSettings`

现在（`apple/LatticeTunnelMac/PacketTunnelProvider.swift`）`NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")` 是写死的一行，只覆盖 overlay 网段本身。改法：`makeSettings` 从 Go 引擎已经收到的 netmap（`ComputedPeers`，逐个带 `AllowedIPs`）里，把每个 CIDR 转成一条 `NEIPv4Route` 加进 `includedRoutes`，覆盖：
- 自己的 overlay 网段（不变）
- 任何自己选中的 provider 的额外路由（新的这部分）

Exit Node（`0.0.0.0/0`）需要特殊处理——不能真的把 `0.0.0.0/0` 塞进 `includedRoutes` 之外还留着别的路由，否则会跟系统默认路由冲突；`NEPacketTunnelNetworkSettings` 本身支持把 `0.0.0.0/0` 设为 includedRoute 来接管默认路由，这种情况下要同时排除跟控制面/中继服务器通信的地址（`excludedRoutes`），避免自己把去 `latticed`/relay 服务器的流量也绕进隧道形成死锁——这是 Tailscale/WireGuard 官方客户端处理 exit-node 的标准做法，需要在实现时对着抓包验证一次。

### 6.2 `NetworkPages.swift` 的 `NetworkSettingsView`

现在"使用退出节点"那一行是纯静态展示。接上：
- 拉取 workspace 内所有 `AdvertisedRoutes` 非空的 peer，按"是否是 `0.0.0.0/0`"分成"可作为 Exit Node 的设备"和"可提供子网路由的设备"两组
- 选择器选中后调用 `POST .../route-selection`，成功后 `TunnelManager` 触发一次 `RefreshConfig`（已有）
- "广播子网路由"那个 toggle（本机把自己的网段开放出去）打开后调用 `POST .../advertised-routes`，先做手填 CIDR，不做自动探测本机网段（自动探测是可以做但价值不大，手填足够且实现简单）

## 七、测试

- `netmap_builder_test.go`（已有同名测试文件的目录）加用例：consumer 选中 provider 后，只有 consumer 收到展开后的 `AllowedIPs`，第三方 peer 收到的还是 `/32`
- 边界：peer 不能选自己（`ConsumerPeerID == ProviderPeerID` 时 API 拒绝）；provider 取消声明（`AdvertisedRoutes` 清空）后，已经选中它的 consumer 下一次 netmap 应该自动回落到 `/32`（不需要级联删除 `t_peer_route_selection`，只是 `AllowedIPs` 展开条件里多判一下 provider 当前是否还有声明）

## 八、实现顺序建议

1. DB 模型 + 两个 API（服务端，可独立测试）
2. `netmap_builder.go` 的个性化展开逻辑 + 单测
3. Swift `makeSettings` 动态路由 + `NetworkSettingsView` 接上真实数据
4. 真机验证 Exit Node 的 `excludedRoutes` 边界情况（控制面/relay 地址不能被自己绕进隧道）
