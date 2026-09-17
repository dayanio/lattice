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

package infra

import (
	"encoding/binary"
	"fmt"
	"sync"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// PeerID is a compact 8-byte identifier derived from the first 8 bytes of a WireGuard public key.
// It is used only for NATS subject routing and wire-protocol sender IDs.
type PeerID [8]byte

func FromKey(key wgtypes.Key) PeerID {
	var id PeerID
	copy(id[:], key[:8])
	return id
}

func (p PeerID) ToUint64() uint64 {
	return binary.BigEndian.Uint64(p[:])
}

func FromUint64(id uint64) PeerID {
	var peerID PeerID
	binary.BigEndian.PutUint64(peerID[:], id)
	return peerID
}

func (id PeerID) String() string {
	return fmt.Sprintf("%d", id.ToUint64())
}

// PeerIdentity unifies the logical identity (AppID) and cryptographic identity (WireGuard public key).
// The management layer looks up by AppID, the transport layer configures WireGuard by PublicKey, and the two layers convert via this struct.
type PeerIdentity struct {
	AppID     string
	PublicKey wgtypes.Key
}

func NewPeerIdentity(appId string, pubKey wgtypes.Key) PeerIdentity {
	return PeerIdentity{AppID: appId, PublicKey: pubKey}
}

// ID returns the compact PeerID used for NATS routing.
func (p PeerIdentity) ID() PeerID {
	return FromKey(p.PublicKey)
}

// String returns a string consistent with PeerID, used for NATS subject construction.
func (p PeerIdentity) String() string {
	return p.ID().String()
}

// PeerManager stores peer metadata indexed by AppID (primary) and PeerID (secondary).
// Both indexes are maintained in sync for O(1) lookup from either key.
type PeerManager struct {
	mu    sync.RWMutex
	peers map[string]*Peer // appId → Peer
	byID  map[PeerID]*Peer // PeerID → Peer (secondary index)
}

func NewPeerManager() *PeerManager {
	return &PeerManager{
		peers: make(map[string]*Peer),
		byID:  make(map[PeerID]*Peer),
	}
}

// AddPeer stores a peer under its AppID (primary index) and WireGuard public
// key (secondary index). If the incoming peer carries a private key, a copy
// with it cleared is stored instead: peers held by this manager are serialized
// into signaling payloads sent to remote peers (SYN/ACK PeerInfo, OFFER
// Current via json.Marshal), so any key material stored here would be
// broadcast on the wire. The caller's own struct is never mutated — the local
// node legitimately holds its private key there; it just must not live in the
// shared, serialized cache.
func (p *PeerManager) AddPeer(appId string, peer *Peer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stored := peer
	if peer != nil && peer.PrivateKey != "" {
		scrubbed := *peer
		scrubbed.PrivateKey = ""
		stored = &scrubbed
	}
	p.peers[appId] = stored
	if stored.PublicKey != "" {
		if key, err := wgtypes.ParseKey(stored.PublicKey); err == nil {
			p.byID[FromKey(key)] = stored
		}
	}
}

func (p *PeerManager) RemovePeer(appId string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if peer := p.peers[appId]; peer != nil && peer.PublicKey != "" {
		if key, err := wgtypes.ParseKey(peer.PublicKey); err == nil {
			delete(p.byID, FromKey(key))
		}
	}
	delete(p.peers, appId)
}

func (p *PeerManager) GetPeer(appId string) *Peer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.peers[appId]
}

// GetByPeerID looks up a peer by WireGuard-derived PeerID. O(1).
func (p *PeerManager) GetByPeerID(peerID PeerID) *Peer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byID[peerID]
}

// GetAll returns a snapshot of all known peers.
func (p *PeerManager) GetAll() []*Peer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Peer, 0, len(p.peers))
	for _, peer := range p.peers {
		result = append(result, peer)
	}
	return result
}

// GetIdentity resolves a PeerID to a full PeerIdentity.
// Used at the NATS boundary where only PeerID is available from the packet.
func (p *PeerManager) GetIdentity(peerID PeerID) (PeerIdentity, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	peer := p.byID[peerID]
	if peer == nil || peer.PublicKey == "" {
		return PeerIdentity{}, false
	}
	key, err := wgtypes.ParseKey(peer.PublicKey)
	if err != nil {
		return PeerIdentity{}, false
	}
	return NewPeerIdentity(peer.AppID, key), true
}
