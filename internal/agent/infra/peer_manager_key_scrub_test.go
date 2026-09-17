package infra

import (
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Peers stored in the manager are serialized into signaling payloads
// (SYN/ACK PeerInfo, OFFER Current) — the private key must never survive
// the store, even when callers hand us a peer that carries one.
func TestPeerManagerAddPeer_ScrubsPrivateKey(t *testing.T) {
	pm := NewPeerManager()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	peer := &Peer{AppID: "self", PublicKey: key.PublicKey().String(), PrivateKey: key.String()}

	pm.AddPeer("self", peer)

	got := pm.GetPeer("self")
	if got == nil {
		t.Fatal("peer not stored")
	}
	if got.PrivateKey != "" {
		t.Fatal("stored peer must not carry the private key")
	}
	if got.PublicKey != peer.PublicKey {
		t.Fatal("public key must be preserved")
	}
	// byID secondary index must still resolve after the scrub-copy.
	if pm.GetByPeerID(FromKey(key.PublicKey())) == nil {
		t.Fatal("byID index broken by scrub")
	}
	// The CALLER's struct is not mutated (the key lives on there by design).
	if peer.PrivateKey == "" {
		t.Fatal("caller's own struct must be left untouched")
	}
}
