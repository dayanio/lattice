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

package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSetPeerApproval_RequestShape stubs the two real endpoints SetPeerApproval
// exercises — GET /api/v1/workspaces/list (namespace→ID resolution) and
// PUT /api/v1/peers/:name/approval (ADR-0003 approval) — and asserts the
// client sends the expected method, path, peer name, workspace header, and body.
func TestSetPeerApproval_RequestShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath, gotName, gotStatus, gotWSID string
	router := gin.New()
	// resolveWorkspaceID hits GET /api/v1/workspaces/list; Client.do unwraps
	// the {"code":0,"data":...} envelope, so the stub returns one.
	router.GET("/api/v1/workspaces/list", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": []gin.H{
			{"id": "ws-1", "namespace": "ws-ns"},
		}})
	})
	// Mirrors internal/server/server/api.go: peerApi := s.Group("/api/v1/peers");
	// peerApi.PUT("/:name/approval", ...).
	router.PUT("/api/v1/peers/:name/approval", func(c *gin.Context) {
		gotPath = c.FullPath()
		gotName = c.Param("name")
		gotWSID = c.GetHeader("X-Workspace-Id")
		var body map[string]string
		_ = c.ShouldBindJSON(&body)
		gotStatus = body["status"]
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})
	ts := httptest.NewServer(router)
	defer ts.Close()

	c, err := NewClient(ts.URL, "token")
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"approved", "revoked"} {
		if err := c.SetPeerApproval("ws-ns", "device-p", status); err != nil {
			t.Fatal(err)
		}
		if gotPath != "/api/v1/peers/:name/approval" || gotName != "device-p" ||
			gotStatus != status || gotWSID != "ws-1" {
			t.Fatalf("status=%q path=%q name=%q got=%q wsID=%q",
				status, gotPath, gotName, gotStatus, gotWSID)
		}
	}

	// Unknown namespace must surface the resolution error.
	if err := c.SetPeerApproval("no-such-ns", "device-p", "approved"); err == nil {
		t.Fatal("expected error for unknown namespace, got nil")
	}
}
