// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package migrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
	"kpt.dev/configsync/cmd/nomos/status"
	"kpt.dev/configsync/pkg/client/restconfig"
)

const (
	defaultWaitTime = 10 * time.Minute // Matches original defaultWaitTimeout
)

// Cmd performs the migration from mono-repo to multi-repo for all the provided contexts.
var Cmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrates to the new Config Sync architecture by enabling the multi-repo mode.",
	Long:  "Migrates to the new Config Sync architecture by enabling the multi-repo mode. It provides you with additional features and gives you the flexibility to sync to a single repository, or multiple repositories.",
	Args:  cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true

		var contexts []string
		if len(nomosflags.Contexts) == 0 {
			currentContext, err := restconfig.CurrentContextName()
			if err != nil {
				return fmt.Errorf("failed to get current context name: %w", err)
			}
			contexts = append(contexts, currentContext)
		} else if len(nomosflags.Contexts) == 1 && nomosflags.Contexts[0] == "all" {
			// "all" contexts will be handled by executeMigrate based on clientMap
			contexts = nil // Pass nil to indicate all contexts
		} else {
			contexts = nomosflags.Contexts
		}

		// Call the main execution logic from exec.go
		// Pass all necessary flag values.
		err := executeMigrate(cmd.Context(), contexts, dryRun, waitTimeout, removeConfigManagement, nomosflags.ClientTimeout)

		// Error handling and final messages are now part of executeMigrate,
		// but we can add a generic error message here if needed, or ensure executeMigrate handles all user feedback.
		if err != nil {
			// It's better if executeMigrate prints specific errors.
			// This is a fallback.
			return fmt.Errorf("migration failed: %w", err)
		}
		return nil
	},
}

func init() {
	// Register global flags from the nomosflags package
	Cmd.Flags().StringSliceVar(&nomosflags.Contexts, "contexts", nil,
		`Accepts a comma-separated list of contexts to use in multi-cluster environments. Defaults to the current context. Use "all" for all contexts.`)
	Cmd.Flags().DurationVar(&nomosflags.ClientTimeout, "connect-timeout", restconfig.DefaultTimeout, "Timeout for connecting to each cluster")

	// Register flags specific to the migrate command
	Cmd.Flags().BoolVar(&dryRun, flagDryRun, false, "If enabled, only prints the migration output.")
	Cmd.Flags().DurationVar(&waitTimeout, flagWaitTimeout, defaultWaitTime, "Timeout for waiting for condition to be true")
	Cmd.Flags().BoolVar(&removeConfigManagement, flagRemoveConfigManagement, false,
		`If enabled, removes the ConfigManagement operator and CRD. This establishes a standalone OSS Config Sync install.`)
}
