# M0 嵌入式投屏直发闭环 Implementation Plan(shim ListenUDP + 引擎 47822 接线)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 嵌入式(gVisor)接收端接住 overlay 直发主路——发送端 UDP 数据报到接收端保留端口 47822 后在引擎内消费并回 ACK,使嵌入式收端与 TUN 收端在命令通道上完全对齐(zyno 智能投屏 v1 设计 §五的 M0)。

**Architecture:** lattice-shim 的 `Netstack` 新增通用 `ListenUDP`(经 `gonet.DialUDP(s, laddr, nil, proto)` 拿到"已绑定、未连接"的 `*UDPConn`,即 `net.PacketConn`),`Server` 暴露同名方法;lattice 侧 `EmbeddedEngine` 在入网后把 overlay IP:47822 绑上去,读循环复用现有 `dispatchCastCommand` 汇点(NATS 路同一个),并按 `packetTUN.interceptCast` 的语义回 `{"ack":<id>}`。NATS 兜底路与发送端双传输逻辑零改动。

**Tech Stack:** Go 1.25、gVisor netstack(pinned fork `github.com/google/gvisor v0.0.0-20260508212337-96dad6a2da94`,经 replace)、wireguard-go(pinned `wireflowio` fork)、lattice-shim(独立仓 `/Users/francis/workspc/lattice-shim`)。

## Global Constraints

- **lattice-shim 零依赖铁律**:shim 只准 import 标准库 + `gvisor.dev/gvisor` + `golang.zx2c4.com/wireguard`,禁止引入任何 Lattice 控制面组件(shim CLAUDE.md 原文)。
- 新建 Go 文件一律带 Apache 2.0 头(两仓同规,照抄现有文件头部)。
- **提交规范(lattice 仓 CLAUDE.md)**:conventional commits(`feat(scope):` 等)、`git commit -s`、禁止 `Co-Authored-By`、提交后立即 `git push`、提交前跑 `make lint`(仅 lattice 仓有此要求;纯 md 改动可跳过)。
- **协议不变量(不可偏离,来源 spec §五与 `apple/engine/packet_tun.go:352-398`)**:
  - 保留端口 = **47822**(`packet_tun.go:98` 同款常量);
  - 命令载荷 = cast 命令 JSON(`{"id","action","url","title","ts"}`,`cmd/castcmd/main.go:61-67` 同款);
  - ACK = `{"ack":"<命令id>"}`,从 47822 原路发回发送方;
  - 保留端口上的非 JSON 数据报**照回 `{"ack":""}`**(与 TUN 实现实际行为一致——`interceptCast` 里 `cmd.ID` 零值即空串);
  - 命令去重是**宿主**职责(收端按 id 去重),引擎不去重、每报必回 ACK;
  - NATS 兜底路(`lattice.cast.<AppID>.cmd`)与发送端双传输逻辑**零改动**。
- 开发期(Task 3-4)lattice 以 replace 指向本地 lattice-shim;Task 5 收口移除。
- 任务粒度:每步一个动作;测试先行(TDD);每个 Task 一个 commit。

---

### Task 1: shim `Netstack.ListenUDP` — 用户态 netstack 上的 UDP PacketConn

**Files:**
- Modify: `lattice-shim/shim/netstack_core.go`(在 `ListenTCP` 之后新增,约 :200)
- Test: `lattice-shim/shim/netstack_core_test.go`(文件尾追加)

**Interfaces:**
- Consumes: 现有 `NewNetstack(localIP)`、`ns.s *stack.Stack`(同文件内)。
- Produces: `func (ns *Netstack) ListenUDP(addr string) (net.PacketConn, error)` — Task 2 的 `Server.ListenUDP` 直接委托它;addr 形如 `"127.0.0.1:9402"`,必须含 IP(与 `ListenTCP` 契约一致)。

- [ ] **Step 1: 写失败测试**(追加到 `netstack_core_test.go` 文件尾,package 为已有的 `shim_test`)

```go
func TestNetstack_ListenUDP_Loopback(t *testing.T) {
	ns, err := shim.NewNetstack("127.0.0.1")
	if err != nil {
		t.Fatalf("NewNetstack: %v", err)
	}
	defer ns.Close()

	pc, err := ns.ListenUDP("127.0.0.1:9402")
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()

	go func() {
		conn, err := ns.DialContext(context.Background(), "udp", "127.0.0.1:9402")
		if err != nil {
			t.Errorf("DialContext udp: %v", err)
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("cast command json")); err != nil {
			t.Errorf("Write: %v", err)
		}
		buf := make([]byte, 128)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Errorf("Read ack: %v", err)
			return
		}
		if string(buf[:n]) != "ack-payload" {
			t.Errorf("expected %q, got %q", "ack-payload", buf[:n])
		}
	}()

	buf := make([]byte, 1024)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, addr, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if string(buf[:n]) != "cast command json" {
		t.Errorf("expected datagram %q, got %q", "cast command json", buf[:n])
	}
	if _, err := pc.WriteTo([]byte("ack-payload"), addr); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
}
```

若文件缺 import(`context`/`time` 已大概率存在),按需补齐。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /Users/francis/workspc/lattice-shim && go test ./shim/ -run TestNetstack_ListenUDP_Loopback -v`
Expected: **编译失败** `ns.ListenUDP undefined (type *shim.Netstack has no field or method ListenUDP)`

- [ ] **Step 3: 最小实现**(`netstack_core.go`,`ListenTCP` 函数之后)

```go
// ListenUDP creates a UDP packet conn bound to addr on the netstack. The
// returned net.PacketConn is unconnected: ReadFrom receives datagrams from
// any remote, WriteTo sends to any remote (routed via the channel NIC /
// WireGuard overlay). This is the embedded-receiver counterpart of
// ListenTCP — inbound overlay UDP terminates here only for explicitly
// bound ports (default-deny elsewhere).
func (ns *Netstack) ListenUDP(addr string) (net.PacketConn, error) {
	if ns.s == nil {
		return nil, fmt.Errorf("netstack closed")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP in address %q", addr)
	}
	port, err := net.LookupPort("udp", portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}
	fullAddr := tcpip.FullAddress{
		Addr: tcpip.AddrFrom4Slice(ip.To4()),
		Port: uint16(port),
	}
	// raddr nil → bound but unconnected: PacketConn semantics (ReadFrom /
	// WriteTo to arbitrary remotes), which the connected DialUDP form used
	// by dialContext does not provide.
	return gonet.DialUDP(ns.s, &fullAddr, nil, header.IPv4ProtocolNumber)
}
```

(pinned gVisor 的 `gonet` 没有 `ListenUDP`;`DialUDP` 文档明示 raddr=nil 即 unconnected,返回的 `*UDPConn` 实现 `net.PacketConn`——`gonet.go:569-600, 635, 651`。)

- [ ] **Step 4: 跑测试确认通过**

Run: `cd /Users/francis/workspc/lattice-shim && go test ./shim/ -run TestNetstack_ListenUDP_Loopback -v`
Expected: PASS

- [ ] **Step 5: 回归全部 shim 测试**

Run: `cd /Users/francis/workspc/lattice-shim && go build ./shim && go test ./shim/`
Expected: 全部 PASS(现有 TCP 路径零回归)

- [ ] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice-shim
git add shim/netstack_core.go shim/netstack_core_test.go
git commit -s -m "feat(shim): netstack ListenUDP — bound unconnected UDP PacketConn"
```

---

### Task 2: shim `Server.ListenUDP` + 跨栈泵测试(含大报文 MTU 用例)

**Files:**
- Modify: `lattice-shim/shim/server.go`(在 `Listen` 之后新增)
- Test: `lattice-shim/shim/server_test.go`(文件尾追加)

**Interfaces:**
- Consumes: Task 1 的 `Netstack.ListenUDP(addr string) (net.PacketConn, error)`;`server_test.go` 现有 `pumpPackets(ctx, from, to)` 双栈泵助手。
- Produces: `func (s *Server) ListenUDP(network, addr string) (net.PacketConn, error)` — network 仅收 `"udp"`/`"udp4"`;Task 3 的 `EmbeddedEngine.startCastOverlayReceiver` 调它。**注意 `Listen` 继续拒绝 "udp"(`TestServer_ListenRejectsUDP` 保持有效)——PacketConn 与 net.Listener 是两个类型,不做联合返回。**

- [ ] **Step 1: 写失败测试**(追加到 `server_test.go` 文件尾)

```go
func TestServer_ListenUDP_RejectsTCP(t *testing.T) {
	srv, err := shim.NewServer("10.50.0.1", nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	_, err = srv.ListenUDP("tcp", "10.50.0.1:9403")
	if err == nil {
		t.Error(`expected error for unsupported network "tcp"`)
	}
}

func TestServer_DialListenUDP(t *testing.T) {
	a, err := shim.NewServer("10.50.0.1", &test.MockPeerManager{})
	if err != nil {
		t.Fatalf("NewServer(a): %v", err)
	}
	defer a.Close()

	b, err := shim.NewServer("10.50.0.2", &test.MockPeerManager{})
	if err != nil {
		t.Fatalf("NewServer(b): %v", err)
	}
	defer b.Close()

	pumpCtx, stopPump := context.WithCancel(context.Background())
	defer stopPump()
	go pumpPackets(pumpCtx, a.Channel(), b.Channel())
	go pumpPackets(pumpCtx, b.Channel(), a.Channel())

	pc, err := b.ListenUDP("udp", "10.50.0.2:9404")
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()

	go func() {
		buf := make([]byte, 1024)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		// in-engine ACK 语义彩排:原路回发
		_, _ = pc.WriteTo([]byte(`{"ack":"id-1"}`), addr)
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := a.Dial(dialCtx, "udp", "10.50.0.2:9404")
	if err != nil {
		t.Fatalf("Dial udp: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(`{"id":"id-1","action":"play"}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read ack: %v", err)
	}
	if string(buf[:n]) != `{"ack":"id-1"}` {
		t.Errorf("expected ack json, got %q", buf[:n])
	}
}

func TestServer_DialListenUDP_LargeDatagram(t *testing.T) {
	a, err := shim.NewServer("10.50.0.1", &test.MockPeerManager{})
	if err != nil {
		t.Fatalf("NewServer(a): %v", err)
	}
	defer a.Close()

	b, err := shim.NewServer("10.50.0.2", &test.MockPeerManager{})
	if err != nil {
		t.Fatalf("NewServer(b): %v", err)
	}
	defer b.Close()

	pumpCtx, stopPump := context.WithCancel(context.Background())
	defer stopPump()
	go pumpPackets(pumpCtx, a.Channel(), b.Channel())
	go pumpPackets(pumpCtx, b.Channel(), a.Channel())

	pc, err := b.ListenUDP("udp", "10.50.0.2:9405")
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()

	big := make([]byte, 8192)
	for i := range big {
		big[i] = byte(i % 251)
	}
	go func() {
		buf := make([]byte, 65536)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		_, _ = pc.WriteTo(buf[:n], addr)
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := a.Dial(dialCtx, "udp", "10.50.0.2:9405")
	if err != nil {
		t.Fatalf("Dial udp: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(big); err != nil {
		t.Fatalf("Write %d B: %v", len(big), err)
	}
	buf := make([]byte, 65536)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read echo: %v", err)
	}
	if n != len(big) {
		t.Fatalf("expected %d B echo, got %d", len(big), n)
	}
	if !bytes.Equal(buf[:n], big) {
		t.Error("large datagram corrupted in transit")
	}
}
```

(`server_test.go` 现有 import 缺 `bytes` 则补。8192B > 链路 MTU 1500,依赖 gVisor 发送侧 IP 分片 + 接收侧重组装车——pinned fork 的 `ipv4.handleFragments` 存在。若实际跑挂且报错为超 MTU 写拒绝,把载荷降到 1400(单包上界)并在 PR 描述记录该 gVisor 行为。)

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /Users/francis/workspc/lattice-shim && go test ./shim/ -run 'TestServer_(ListenUDP|DialListenUDP)' -v`
Expected: **编译失败** `srv.ListenUDP undefined (type *shim.Server has no field or method ListenUDP)`

- [ ] **Step 3: 最小实现**(`server.go`,`Listen` 函数之后)

```go
// ListenUDP creates a UDP packet conn on the netstack at addr. network must
// be "udp" or "udp4". The returned net.PacketConn is unconnected — callers
// ReadFrom/WriteTo with arbitrary remotes. Inbound overlay UDP reaches a
// bound port only; unbound ports stay default-deny, mirroring ListenTCP.
func (s *Server) ListenUDP(network, addr string) (net.PacketConn, error) {
	switch network {
	case "udp", "udp4":
		return s.ns.ListenUDP(addr)
	default:
		return nil, fmt.Errorf("unsupported network: %s", network)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd /Users/francis/workspc/lattice-shim && go test ./shim/ -run 'TestServer_(ListenUDP|DialListenUDP)' -v`
Expected: 三个测试全 PASS

- [ ] **Step 5: 回归 + Push**

Run: `cd /Users/francis/workspc/lattice-shim && go build ./shim && go test ./shim/`
Expected: 全部 PASS

```bash
cd /Users/francis/workspc/lattice-shim
git add shim/server.go shim/server_test.go
git commit -s -m "feat(shim): Server.ListenUDP for overlay UDP listeners"
git push origin dev
```

注:lattice 消费的是 `github.com/alatticeio/lattice-shim` 模块路径(origin 为 winstonfly fork)。若 `alatticeio` 镜像不是自动同步,按现有伪版本(`v0.0.0-20260922113931-3d1c0f17c3c6`)当年的发布方式同步镜像;同步完成前 Task 3/4 用本地 replace 开发,不受阻塞。

---

### Task 3: lattice `EmbeddedEngine` cast 直发接收器(47822 接线)

**Files:**
- Create: `lattice/apple/engine/embedded/cast.go`
- Modify: `lattice/apple/engine/embedded/engine.go`(`EmbeddedEngine` 字段、`Start` 接线、两条错误路径与 ctx 收尾、`dispatchCastCommand` 过期注释)
- Test: `lattice/apple/engine/embedded/cast_test.go`(新建,纯单元测试,零控制面依赖)

**Interfaces:**
- Consumes: Task 2 的 `shim.Server.ListenUDP(network, addr string) (net.PacketConn, error)`;现有 `EmbeddedEngine.dispatchCastCommand(payload []byte)`(NATS 与 overlay 双路共用的汇点)。
- Produces: `(e *EmbeddedEngine) startCastOverlayReceiver(srv *shim.Server, overlayIP string) error`、`(e *EmbeddedEngine) castOverlayLoop(pc net.PacketConn)`、`(e *EmbeddedEngine) stopCastReceiver()`、常量 `castCommandPort = "47822"` — Task 4 的集成测试直接引用 `castCommandPort` 与事件格式 `"cast: {json}"`。

- [ ] **Step 1: 本地 replace 指向刚改好的 shim**

```bash
cd /Users/francis/workspc/lattice
go mod edit -replace github.com/alatticeio/lattice-shim=/Users/francis/workspc/lattice-shim
go mod tidy
go build ./apple/engine/embedded/
```
Expected: 构建通过(replace 会写进 go.mod,Task 5 收口移除)

- [ ] **Step 2: 写失败单元测试**(新建 `cast_test.go`,`package embedded`,与 `engine_test.go` 同包)

```go
// 头部 Apache 2.0 license boilerplate,同 engine_test.go。

package embedded

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRead struct {
	data []byte
	addr net.Addr
}

type fakeWrite struct {
	data []byte
	addr net.Addr
}

// fakePacketConn is a minimal net.PacketConn stand-in: tests push inbound
// datagrams onto readCh and observe outbound datagrams on writeCh. Close
// unblocks a pending ReadFrom so the loop exits.
type fakePacketConn struct {
	readCh    chan fakeRead
	writeCh   chan fakeWrite
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakePacketConn() *fakePacketConn {
	return &fakePacketConn{
		readCh:  make(chan fakeRead, 8),
		writeCh: make(chan fakeWrite, 8),
		closed:  make(chan struct{}),
	}
}

func (f *fakePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case r := <-f.readCh:
		return copy(p, r.data), r.addr, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	}
}

func (f *fakePacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	data := make([]byte, len(p))
	copy(data, p)
	select {
	case f.writeCh <- fakeWrite{data: data, addr: addr}:
		return len(p), nil
	case <-f.closed:
		return 0, net.ErrClosed
	}
}

func (f *fakePacketConn) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakePacketConn) LocalAddr() net.Addr                { return nil }
func (f *fakePacketConn) SetDeadline(time.Time) error       { return nil }
func (f *fakePacketConn) SetReadDeadline(time.Time) error   { return nil }
func (f *fakePacketConn) SetWriteDeadline(time.Time) error  { return nil }

var _ net.PacketConn = (*fakePacketConn)(nil)

func TestCastOverlayLoopAckAndDispatch(t *testing.T) {
	e := &EmbeddedEngine{}
	events := make(chan string, 8)
	e.SetEventHandler(func(ev string) { events <- ev })

	pc := newFakePacketConn()
	go e.castOverlayLoop(pc)

	sender := &net.UDPAddr{IP: net.IPv4(10, 96, 0, 9), Port: 51000}
	payload, _ := json.Marshal(map[string]any{
		"id": "abc-1", "action": "play",
		"url": "http://10.96.0.9:47823/hash/movie.mkv", "title": "movie.mkv", "ts": 1717500000,
	})
	pc.readCh <- fakeRead{data: payload, addr: sender}

	select {
	case w := <-pc.writeCh:
		var ack map[string]string
		if err := json.Unmarshal(w.data, &ack); err != nil {
			t.Fatalf("ack not json: %v (%q)", err, w.data)
		}
		if ack["ack"] != "abc-1" {
			t.Errorf("expected ack id abc-1, got %q", ack["ack"])
		}
		if w.addr.String() != sender.String() {
			t.Errorf("ack routed to %s, want %s", w.addr, sender)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-engine ACK")
	}

	select {
	case ev := <-events:
		if !strings.HasPrefix(ev, "cast: ") || !strings.Contains(ev, "abc-1") {
			t.Errorf("unexpected dispatched event %q", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dispatched cast event")
	}
}

func TestCastOverlayLoopGarbageAckedWithEmptyID(t *testing.T) {
	e := &EmbeddedEngine{}
	pc := newFakePacketConn()
	go e.castOverlayLoop(pc)

	sender := &net.UDPAddr{IP: net.IPv4(10, 96, 0, 9), Port: 51001}
	pc.readCh <- fakeRead{data: []byte("not json"), addr: sender}

	select {
	case w := <-pc.writeCh:
		var ack map[string]string
		_ = json.Unmarshal(w.data, &ack)
		if ack["ack"] != "" {
			t.Errorf(`expected {"ack":""} for non-JSON garbage, got %q`, w.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for garbage ACK")
	}
}

func TestCastOverlayLoopExitOnClose(t *testing.T) {
	e := &EmbeddedEngine{}
	pc := newFakePacketConn()
	done := make(chan struct{})
	go func() {
		e.castOverlayLoop(pc)
		close(done)
	}()
	pc.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("castOverlayLoop did not exit after conn close")
	}
}
```

(`errors` import 若未用到则去掉;`var _ net.PacketConn` 编译期锁死接口契约。)

- [ ] **Step 3: 跑测试确认失败**

Run: `cd /Users/francis/workspc/lattice && go test ./apple/engine/embedded/ -run TestCastOverlay -v`
Expected: **编译失败** `e.castOverlayLoop undefined`

- [ ] **Step 4: 实现**(新建 `apple/engine/embedded/cast.go`)

```go
// Copyright 2026 The Lattice Authors, Inc.
//
// (Apache 2.0 boilerplate,照抄 engine.go 头部)

package embedded

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/alatticeio/lattice-shim/shim"
)

// castCommandPort is the reserved overlay UDP port for cast commands —
// the same constant as packet_tun.go:98 for TUN-mode engines. The embedded
// engine binds it on the user-space netstack to receive the cast overlay
// primary transport (design: 2026-10-09-zyno-smart-cast-design.md §五).
const castCommandPort = "47822"

// startCastOverlayReceiver binds the reserved cast port on the overlay
// netstack and pumps datagrams into the same dispatch sink as the NATS
// fallback transport, ACKing every datagram in-engine ({"ack":<id>}) the
// way packetTUN.interceptCast does for TUN-mode engines.
func (e *EmbeddedEngine) startCastOverlayReceiver(srv *shim.Server, overlayIP string) error {
	pc, err := srv.ListenUDP("udp4", net.JoinHostPort(overlayIP, castCommandPort))
	if err != nil {
		return fmt.Errorf("cast overlay receiver: %w", err)
	}
	e.mu.Lock()
	e.castConn = pc
	e.mu.Unlock()
	go e.castOverlayLoop(pc)
	return nil
}

// castOverlayLoop reads cast command datagrams until the conn is closed by
// engine teardown. Every datagram is ACKed from the bound port (source
// 47822, original-route reply) and dispatched on its own goroutine —
// parity with TUN mode. Dedup by command id is the host's job, identical
// to the NATS path.
func (e *EmbeddedEngine) castOverlayLoop(pc net.PacketConn) {
	buf := make([]byte, 65536)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return // teardown closed the conn
		}
		body := make([]byte, n)
		copy(body, buf[:n])
		var cmd struct {
			ID string `json:"id"`
		}
		// 非 JSON 数据报照回 {"ack":""}(cmd.ID 零值),与 interceptCast 行为一致
		_ = json.Unmarshal(body, &cmd)
		go e.dispatchCastCommand(body)
		if ack, err := json.Marshal(map[string]string{"ack": cmd.ID}); err == nil {
			_, _ = pc.WriteTo(ack, addr)
		}
	}
}

// stopCastReceiver closes the bound cast conn, unblocking castOverlayLoop.
func (e *EmbeddedEngine) stopCastReceiver() {
	e.mu.Lock()
	pc := e.castConn
	e.castConn = nil
	e.mu.Unlock()
	if pc != nil {
		_ = pc.Close()
	}
}
```

- [ ] **Step 5: 接线 `engine.go`**(五处精确编辑)

(a) 字段——`overlay string` 之后(`engine.go:43` 附近)加:

```go
	// castConn is the UDP PacketConn bound to the reserved cast port on the
	// overlay netstack; nil while the engine is not running.
	castConn net.PacketConn
```

(b) `Start` 里 `shim.NewServer` 成功后(`engine.go:124-127`)插入:

```go
	srv, err := shim.NewServer(overlayIP, nil)
	if err != nil {
		return fmt.Errorf("netstack: %w", err)
	}
	if err := e.startCastOverlayReceiver(srv, overlayIP); err != nil {
		_ = srv.Close()
		return fmt.Errorf("cast receiver: %w", err)
	}
```

(c) `NewNode` 失败路径(`engine.go:141-144`)在 `srv.Close()` 前加 `e.stopCastReceiver()`:

```go
	if err != nil {
		e.stopCastReceiver()
		_ = srv.Close()
		return fmt.Errorf("create node: %w", err)
	}
```

(d) `node.Start` 失败路径(`engine.go:150-153`)同样加:

```go
	if err := node.Start(ctx); err != nil {
		e.stopCastReceiver()
		_ = srv.Close()
		return fmt.Errorf("start node: %w", err)
	}
```

(e) ctx 收尾(`engine.go:162-165`)在 `node.Stop()` 前加:

```go
	<-ctx.Done()

	e.stopCastReceiver()
	_ = node.Stop()
	_ = srv.Close()
```

(f) 更新 `dispatchCastCommand` 的过期注释(`engine.go:255-258`,注释说"主路不存在"已不成立):

```go
// dispatchCastCommand is the cast command sink — both transports feed it:
// the NATS fallback transport (NodeConfig.CastCommandHandler) and the
// overlay primary transport (castOverlayLoop on reserved port 47822).
// Dedup by command id is the host's job.
func (e *EmbeddedEngine) dispatchCastCommand(payload []byte) {
```

- [ ] **Step 6: 跑测试确认通过 + 回归**

Run: `cd /Users/francis/workspc/lattice && go test ./apple/engine/embedded/ -run 'TestCastOverlay' -v && go build ./...`
Expected: 三个测试 PASS,全仓构建通过

- [ ] **Step 7: lint + Commit + Push**

Run: `cd /Users/francis/workspc/lattice && make lint`
Expected: 无 error(有则先修)

```bash
cd /Users/francis/workspc/lattice
git add apple/engine/embedded/cast.go apple/engine/embedded/cast_test.go apple/engine/embedded/engine.go go.mod go.sum
git commit -s -m "feat(embedded): cast overlay receiver on reserved port 47822"
git push
```

---

### Task 4: 集成测试 — 双嵌入式引擎真实数据面直发闭环

**Files:**
- Test: `lattice/apple/engine/embedded/cast_test.go`(追加,复用 `engine_test.go` 的控制面测试基建)

**Interfaces:**
- Consumes: `engine_test.go` 的 `requireIntegration` / `adminToken` / `mintEnrollmentToken` / `approvePeer`;Task 3 的 `castCommandPort`、事件格式 `"cast: {json}"`。
- Produces: M0 的**验收测试**——不设 `LATTICE_EMBED_INTEGRATION=1` 时自动 skip,`go test ./...` 保持全绿。

**前置(与现有 embedded 集成测试同款):** 本地 mac-demo standalone 控制面(默认 `http://127.0.0.1:8080`,admin/123456)、workspace `mac-demo`、relay 可达;可用 `LATTICE_EMBED_TEST_SERVER` 等环境变量覆盖(见 `engine_test.go:28-56`)。

- [ ] **Step 1: 写集成测试**(追加到 `cast_test.go`)

```go
// startCastTestEngine mints an enrollment token, starts an EmbeddedEngine
// and blocks until it holds an overlay address (approving the pending peer
// on the way). Cleanup tears the engine down via cancel.
func startCastTestEngine(t *testing.T, bearer, workspaceID, name string) *EmbeddedEngine {
	t.Helper()
	token := mintEnrollmentToken(t, bearer, workspaceID, name)
	configJSON, _ := json.Marshal(Config{ServerURL: testControlPlane(), Token: token, Name: name})
	e, err := New(string(configJSON))
	if err != nil {
		t.Fatalf("New(%s): %v", name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- e.Start(ctx) }()

	deadline := time.Now().Add(45 * time.Second)
	for e.OverlayAddress() == "" && time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, name)
		time.Sleep(500 * time.Millisecond)
	}
	if e.OverlayAddress() == "" {
		cancel()
		t.Fatal("timed out waiting for overlay address")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-startErr:
			if err != nil {
				t.Errorf("Start(%s): %v", name, err)
			}
		case <-time.After(20 * time.Second):
			t.Errorf("timed out waiting for Start(%s) to return", name)
		}
	})
	return e
}

func TestEmbeddedEngine_CastOverlayCommandE2E(t *testing.T) {
	requireIntegration(t)

	bearer, workspaceID := adminToken(t)
	suffix := time.Now().UnixNano()
	sender := startCastTestEngine(t, bearer, workspaceID, fmt.Sprintf("embed-cast-send-%d", suffix))
	receiver := startCastTestEngine(t, bearer, workspaceID, fmt.Sprintf("embed-cast-recv-%d", suffix))

	events := make(chan string, 8)
	receiver.SetEventHandler(func(ev string) { events <- ev })

	target := net.JoinHostPort(receiver.OverlayAddress(), castCommandPort)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := sender.Dial(ctx, "udp", target)
	if err != nil {
		t.Fatalf("sender dial udp %s: %v", target, err)
	}
	defer conn.Close()

	cmdID := fmt.Sprintf("e2e-%d", suffix)
	payload, _ := json.Marshal(map[string]any{
		"id": cmdID, "action": "play",
		"url": "http://10.96.0.9:47823/hash/movie.mkv", "title": "movie.mkv", "ts": time.Now().UnixMilli(),
	})
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("send command: %v", err)
	}

	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no in-engine ACK within 20s: %v", err)
	}
	var ack struct {
		Ack string `json:"ack"`
	}
	if err := json.Unmarshal(buf[:n], &ack); err != nil {
		t.Fatalf("ACK not json: %v (%q)", err, buf[:n])
	}
	if ack.Ack != cmdID {
		t.Fatalf("ACK id mismatch: got %q want %q", ack.Ack, cmdID)
	}

	select {
	case ev := <-events:
		if !strings.Contains(ev, cmdID) {
			t.Fatalf("dispatched event missing command id: %q", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver never dispatched the cast event")
	}
}
```

(import 需补 `context`、`fmt`;其余与文件既有 import 合并。)

- [ ] **Step 2: 无控制面环境下确认 skip 不破坏 CI**

Run: `cd /Users/francis/workspc/lattice && go test ./apple/engine/embedded/ -run TestEmbeddedEngine_CastOverlayCommandE2E -v`
Expected: `SKIP` ("set LATTICE_EMBED_INTEGRATION=1 ...")

- [ ] **Step 3: 有控制面环境下跑真实验收**

Run: `cd /Users/francis/workspc/lattice && LATTICE_EMBED_INTEGRATION=1 go test ./apple/engine/embedded/ -run TestEmbeddedEngine_CastOverlayCommandE2E -v -timeout 300s`
Expected: **PASS**,日志含两台设备的 overlay 地址与 ACK 匹配(`ack.Ack == cmdID`)。这是 M0 的闭环证明:命令 JSON 经 A 引擎 netstack → WG/ICE 或 relay → B 引擎 netstack → 47822 端点 → dispatch + 原路 ACK。

- [ ] **Step 4: lint + Commit + Push**

Run: `cd /Users/francis/workspc/lattice && make lint`
Expected: 无 error

```bash
cd /Users/francis/workspc/lattice
git add apple/engine/embedded/cast_test.go
git commit -s -m "test(embedded): cast overlay command e2e across real data plane"
git push
```

---

### Task 5: 依赖收口 — 发布 shim、移除 replace、全量回归

**Files:**
- Modify: `lattice/go.mod`、`lattice/go.sum`
- (前置:Task 2 已 push lattice-shim `dev`)

**Interfaces:**
- Consumes: lattice-shim 远端(镜像同步完成后)可 `go get` 的新伪版本。
- Produces: lattice go.mod 指向含 `Server.ListenUDP` 的 shim 版本,无本地 replace。

- [ ] **Step 1: 同步 alatticeio 镜像并确认可拉取**

```bash
SHA=$(git -C /Users/francis/workspc/lattice-shim rev-parse HEAD)
echo "shim head: $SHA"
```
若 `alatticeio/lattice-shim` 镜像非自动同步,按现行发布方式把 winstonfly fork 的 `dev` 同步过去(现有伪版本即由此而来);然后用 `GOPROXY=direct go list -m github.com/alatticeio/lattice-shim@$SHA` 确认能解析。镜像未就绪时本 Task 挂起,不硬闯。

- [ ] **Step 2: 移除 replace、升级依赖**

```bash
cd /Users/francis/workspc/lattice
go mod edit -dropreplace=github.com/alatticeio/lattice-shim
go get github.com/alatticeio/lattice-shim@$SHA
go mod tidy
```
Expected: go.mod 中 shim 版本更新为含 `ListenUDP` 的新伪版本,replace 块不再含 lattice-shim

- [ ] **Step 3: 全量回归(两仓)**

```bash
cd /Users/francis/workspc/lattice-shim && go build ./shim && go test ./shim/
cd /Users/francis/workspc/lattice && go build ./... && go test ./apple/engine/embedded/ -run 'TestCastOverlay|TestEmbeddedEngine_Start' -v
```
Expected: shim 全 PASS;lattice 单元测试 PASS、集成测试 SKIP(无 env)

- [ ] **Step 4: lint + Commit + Push**

Run: `cd /Users/francis/workspc/lattice && make lint`
Expected: 无 error

```bash
cd /Users/francis/workspc/lattice
git add go.mod go.sum
git commit -s -m "build(deps): consume lattice-shim with Server.ListenUDP"
git push
```

- [ ] **Step 5(手工验收,需真机,非 CI):castcmd 对照真实嵌入式收端**

在已入网、跑着 NE(TUN)引擎的 Mac 上(参考 cast.md 调试工具节):

```bash
go run ./cmd/castcmd \
  -transport overlay \
  -target <嵌入式收端设备overlay-ip>:47822 \
  -media "http://<发送端overlay-ip>:47823/<hash>/movie.mkv" \
  -title "M0 验收"
```
Expected: 输出 `overlay <target>: acked id="<uuid>" after 1 attempt(s)` — 嵌入式收端真实设备(如 LatticeMac 嵌入式引擎页或 tvOS debug 包)在引擎内消费命令并回 ACK,不再依赖 NATS。此步通过即 M0 关单。

---

## 非目标(明确不做,防蔓延)

- 发送端双传输逻辑改动(lattice-apple 侧已有,零改动);
- NATS 路任何改动(它是兜底与即插即用通道);
- 命令去重逻辑进引擎(宿主职责,与 NATS 路一致);
- 8KB 大报文以外的分片行为调优;P3 的通配 Forwarder(`udp.NewForwarder`)不在本期。
