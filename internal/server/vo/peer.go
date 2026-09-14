package vo

import (
	"time"
)

type PeerVo struct {
	ID                  uint64    `json:"id,string"`
	Namespace           string    `json:"namespace"`
	Name                string    `json:"name,omitempty"`
	Description         string    `json:"description,omitempty"`
	NetworkID           string    `json:"networkID,omitempty"` // belong to which group
	CreatedBy           string    `json:"createdBy,omitempty"` // ownerID
	UserId              uint64    `json:"userId,omitempty"`
	Platform            string    `json:"platform"`
	Hostname            string    `json:"hostname,omitempty"`
	AppID               string    `json:"appId,omitempty"`
	Address             *string   `json:"address,omitempty"`
	Endpoint            string    `json:"endpoint,omitempty"`
	PersistentKeepalive int       `json:"persistentKeepalive,omitempty"`
	PublicKey           string    `json:"publicKey,omitempty"`
	AllowedIPs          string    `json:"allowedIps,omitempty"`
	RelayIP             string    `json:"relayIp,omitempty"`
	Pwd                 string    `json:"pwd"`
	GroupName           string    `json:"groupName"`
	Version             uint64    `json:"version"`
	LastUpdatedAt       time.Time `json:"lastUpdatedAt"`

	Labels map[string]string `json:"labels,omitempty"`

	// AdvertisedRoutes lists the CIDRs this peer offers to route for others
	// (see docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md).
	// Empty for peers that haven't declared anything, and always empty in
	// K8s mode (not supported there yet).
	AdvertisedRoutes []string `json:"advertisedRoutes,omitempty"`

	// Status is the real-time online status derived from heartbeats: "online", "offline", or "pending".
	Status string `json:"status,omitempty"`
	// LastSeen is the RFC3339 timestamp of the last received heartbeat. Nil if never seen.
	LastSeen *string `json:"lastSeen,omitempty"`

	// WorkspaceDisplayName is the human-readable name of the workspace this peer belongs to
	WorkspaceDisplayName string `json:"workspaceDisplayName,omitempty"`

	// DisplayName is the user-defined alias for this node, stored as a K8s annotation.
	DisplayName string `json:"displayName,omitempty"`

	// Disabled indicates the node has been administratively disabled by a workspace manager.
	Disabled bool `json:"disabled,omitempty"`
}
