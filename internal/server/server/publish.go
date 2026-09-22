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
	"github.com/alatticeio/lattice/pkg/utils/resp"

	"github.com/gin-gonic/gin"
)

// listPublishes returns the workspace's 对外发布 rules.
func (s *Server) listPublishes(c *gin.Context) {
	list, err := s.peerController.ListPublishes(c.Request.Context())
	if err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, list)
}

// createPublish registers one publish rule and announces the new table to
// gateways. The workspace comes from the auth context.
func (s *Server) createPublish(c *gin.Context) {
	var body struct {
		Name     string `json:"name"`
		PeerName string `json:"peerName"`
		Port     int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Error(c, err.Error())
		return
	}
	if err := s.peerController.CreatePublish(c.Request.Context(), body.Name, body.PeerName, body.Port); err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, nil)
}

// deletePublish removes one publish rule (下线) and announces the table.
func (s *Server) deletePublish(c *gin.Context) {
	if err := s.peerController.DeletePublish(c.Request.Context(), c.Param("name")); err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, nil)
}
