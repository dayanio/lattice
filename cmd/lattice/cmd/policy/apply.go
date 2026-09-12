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

package policy

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newApplyCommand() *cobra.Command {
	var (
		file      string
		dryRun    bool
		namespace string
	)
	c := &cobra.Command{
		Use:   "apply -f <bundle.yaml>",
		Short: "Apply a YAML policy bundle (策略即代码)",
		Example: `  lattice policy apply -f policies.yaml
  lattice policy apply -f policies.yaml --dry-run   # validate only`,
		RunE: func(c *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("-f <bundle.yaml> is required")
			}
			content, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read %s: %w", file, err)
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Println("dry-run: validating bundle only")
			}
			return client.ImportPolicies(namespace, string(content), dryRun)
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "YAML policy bundle file")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "validate only, do not apply")
	c.Flags().StringVarP(&namespace, "namespace", "n", "", "workspace namespace (required)")
	return c
}

func newExportCommand() *cobra.Command {
	var namespace string
	c := &cobra.Command{
		Use:   "export",
		Short: "Export active policies as a YAML bundle",
		RunE: func(c *cobra.Command, args []string) error {
			if namespace == "" {
				return fmt.Errorf("namespace is required (-n <namespace>)")
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			return client.ExportPolicies(namespace)
		},
	}
	c.Flags().StringVarP(&namespace, "namespace", "n", "", "workspace namespace (required)")
	return c
}
