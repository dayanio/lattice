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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/controller"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// noopVerifier behaves like Community: no license, no node-limit enforcement.
type noopVerifier struct{}

func (noopVerifier) Verify() (*license.License, license.Status, error) {
	return nil, license.StatusNotFound, nil
}
func (noopVerifier) HasFeature(string) bool { return false }

// newRouteSelectionTestServer wires a real PeerController (nil K8s client
// = standalone mode, same trigger internal/server/service/peer.go uses
// everywhere else) over a fresh in-memory SQLite store, seeded with two
// peers in workspace "ws1".
func newRouteSelectionTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "gw-id"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Address: "10.96.0.4", PublicKey: "kgw",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "mac-id"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	return &Server{peerController: controller.NewPeerController(nil, st, nil, noopVerifier{}, nil)}
}

// testContext builds a *gin.Context carrying the workspace-scoped request
// the WorkspaceAuthMiddleware would normally have set up, plus the :name
// URL param — everything the handler itself reads.
func testContext(method, name string, body any) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body != nil {
		blob, _ := json.Marshal(body)
		reader = bytes.NewReader(blob)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/", reader)
	req = req.WithContext(context.WithValue(req.Context(), infra.WorkspaceKey, "ws1"))
	c.Request = req
	c.Params = gin.Params{{Key: "name", Value: name}}
	return c, w
}

// respBody mirrors pkg/utils/resp.Response's JSON shape. Every handler in
// this codebase writes HTTP 200 regardless of outcome (see resp.Error) — the
// real result is the body's "code" field, so tests must decode and assert
// on that, not on w.Code.
type respBody struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) respBody {
	t.Helper()
	var body respBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestRouteSelectionHandlers_DeclareSelectList(t *testing.T) {
	s := newRouteSelectionTestServer(t)

	c, w := testContext(http.MethodPost, "gw", map[string]any{"routes": []string{"192.168.1.0/24"}})
	s.setAdvertisedRoutes(c)
	require.Equal(t, http.StatusOK, decodeResp(t, w).Code)

	c, w = testContext(http.MethodPost, "mac", map[string]any{"provider": "gw", "selected": true})
	s.setRouteSelection(c)
	require.Equal(t, http.StatusOK, decodeResp(t, w).Code)

	c, w = testContext(http.MethodGet, "mac", nil)
	s.listRouteSelections(c)
	require.Equal(t, http.StatusOK, decodeResp(t, w).Code)

	var body struct {
		Data []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, []string{"gw"}, body.Data)
}

// TestRouteSelectionHandlers_InvalidJSON posts an unparseable body and
// confirms it hits the ShouldBindJSON error path (resp.BadRequest), which
// is only visible in the body's "code" field since w.Code is always 200.
func TestRouteSelectionHandlers_InvalidJSON(t *testing.T) {
	s := newRouteSelectionTestServer(t)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("not json")))
	req = req.WithContext(context.WithValue(req.Context(), infra.WorkspaceKey, "ws1"))
	c.Request = req
	c.Params = gin.Params{{Key: "name", Value: "gw"}}

	s.setAdvertisedRoutes(c)
	assert.NotEqual(t, http.StatusOK, decodeResp(t, w).Code)
}

// TestRouteSelectionHandlers_NonexistentPeer posts against a peer name that
// doesn't exist in the seeded store and confirms it hits
// standalonePeerByName's not-found error (resp.Error), again only visible
// in the body's "code" field.
func TestRouteSelectionHandlers_NonexistentPeer(t *testing.T) {
	s := newRouteSelectionTestServer(t)

	c, w := testContext(http.MethodPost, "no-such-peer", map[string]any{"routes": []string{"192.168.1.0/24"}})
	s.setAdvertisedRoutes(c)
	assert.NotEqual(t, http.StatusOK, decodeResp(t, w).Code)
}
