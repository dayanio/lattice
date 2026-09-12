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
	"github.com/alatticeio/lattice/internal/agent"
	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/spf13/cobra"
)

func downCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "down",
		Short:   "Disconnect and stop the Lattice node daemon",
		Example: `  lattice down`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return agent.Stop(config.Conf)
		},
	}
}

func serviceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "service <install|uninstall>",
		Short: "Install or remove the node as a system service (systemd/launchd)",
	}
	c.AddCommand(serviceInstallCmd(), serviceUninstallCmd())
	return c
}

func serviceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the node as a system service (enables start on boot)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return agent.InstallService(config.Conf)
		},
	}
}

func serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the node system service",
		RunE: func(cmd *cobra.Command, args []string) error {
			return agent.UninstallService(config.Conf)
		},
	}
}
