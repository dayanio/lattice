// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/nats"
)

// PublishRoute is one 对外发布 rule as broadcast by the control plane (see
// docs/superpowers/specs/2026-09-22-mac-client-capabilities-design.md §六).
type PublishRoute struct {
	Name     string `json:"name"`
	PeerName string `json:"peerName"`
	Port     int    `json:"port"`
	Enabled  bool   `json:"enabled"`
}

// publishTable is the gateway's local copy of the workspace's publish rules,
// replaced wholesale on every broadcast.
type publishTable struct {
	mu     sync.RWMutex
	routes map[string]PublishRoute
}

func (t *publishTable) set(routes []PublishRoute) {
	next := make(map[string]PublishRoute, len(routes))
	for _, r := range routes {
		next[r.Name] = r
	}
	t.mu.Lock()
	t.routes = next
	t.mu.Unlock()
}

func (t *publishTable) get(name string) (PublishRoute, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.routes[name]
	return r, ok
}

// StartIngress runs the 对外发布 gateway: it keeps a local publish table fed
// by the lattice.signals.publishes broadcast and serves an HTTP reverse
// proxy that forwards /<name>/… to the target peer's overlay address. v1
// has no initial fetch — the table is empty until the first broadcast (any
// publish mutation triggers one).
func (c *Node) StartIngress(ctx context.Context, httpAddr string) error {
	svc, ok := c.natsService.(*nats.NatsSignalService)
	if !ok {
		return fmt.Errorf("ingress: NATS signal service unavailable")
	}
	table := &publishTable{routes: map[string]PublishRoute{}}
	if err := svc.SubscribeRawPayload(infra.PublishesChangedSubject, func(payload []byte) {
		var msg struct {
			WorkspaceID string         `json:"workspaceId"`
			Publishes   []PublishRoute `json:"publishes"`
		}
		if jsonErr := json.Unmarshal(payload, &msg); jsonErr != nil {
			c.logger.Warn("ingress: undecodable publish table", "err", jsonErr)
			return
		}
		table.set(msg.Publishes)
		c.logger.Info("ingress: publish table updated", "routes", len(msg.Publishes))
	}); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", c.ingressHandler(table))
	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		c.logger.Info("publish ingress listening", "addr", httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			c.logger.Error("ingress server stopped", err)
		}
	}()
	return nil
}

// ingressHandler maps /<name>/… onto the publish table and proxies to the
// target peer over the overlay.
func (c *Node) ingressHandler(table *publishTable) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			http.Error(w, "用法：/{发布名}/…", http.StatusNotFound)
			return
		}
		route, ok := table.get(name)
		if !ok || !route.Enabled {
			http.Error(w, "发布不存在或已下线："+name, http.StatusNotFound)
			return
		}
		target := c.resolvePeerTarget(route)
		if target == "" {
			http.Error(w, "目标节点暂不可达："+route.PeerName, http.StatusBadGateway)
			return
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: target})
				pr.SetXForwarded()
				// 去掉发布名前缀，其余路径原样转发
				pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, "/"+name)
				if pr.Out.URL.Path == "" {
					pr.Out.URL.Path = "/"
				}
			},
		}
		proxy.ServeHTTP(w, r)
	}
}

// resolvePeerTarget maps the rule's peer to a dialable overlay host:port
// from the local netmap; "" when the peer is unknown or has no address yet.
func (c *Node) resolvePeerTarget(route PublishRoute) string {
	for _, p := range c.GetPeerManager().GetAll() {
		if p == nil {
			continue
		}
		if p.Name != route.PeerName && p.AppID != route.PeerName {
			continue
		}
		if p.Address == nil || *p.Address == "" {
			return ""
		}
		return net.JoinHostPort(*p.Address, strconv.Itoa(route.Port))
	}
	return ""
}
