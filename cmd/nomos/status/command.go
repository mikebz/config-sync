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

package status

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
	"kpt.dev/configsync/pkg/client/restconfig"
)

// Cmd runs a loop that fetches ACM objects from all available clusters and prints a summary of the
// status of Config Management for each cluster.
var Cmd = &cobra.Command{
	Use:   "status",
	Short: "Prints the status of all clusters with Configuration Management installed.",
	Long: `Prints the status of all Config Sync and Config Management components
installed on the cluster. This includes the status of RootSyncs, RepoSyncs,
associated resources, and the overall health of the installation for each cluster.`,
	Args: cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true

		fmt.Println("Connecting to clusters...")

		// Call the main execution logic from exec.go
		// Pass all necessary flag values.
		// nomosflags.Contexts, nomosflags.ClientTimeout are global.
		// pollingInterval, namespace, resourceStatus, name are local to this package.
		return executeStatus(cmd.Context(), nomosflags.Contexts, nomosflags.ClientTimeout, pollingInterval, namespace, resourceStatus, name, os.Stdout)
	},
}

func init() {
	// Register global flags
	nomosflags.AddContexts(Cmd) // Provides nomosflags.Contexts
	Cmd.Flags().DurationVar(&nomosflags.ClientTimeout, "timeout", restconfig.DefaultTimeout, "Sets the timeout for connecting to each cluster. Defaults to 15 seconds. Example: --timeout=30s")

	// Register flags specific to the status command
	Cmd.Flags().DurationVar(&pollingInterval, flagPoll, 0*time.Second, "Continuously polls for status updates at the specified interval. If not provided, the command runs only once. Example: --poll=30s for polling every 30 seconds")
	Cmd.Flags().StringVar(&namespace, flagNamespace, "", "Filters the status output by the specified RootSync or RepoSync namespace. If not provided, displays status for all RootSync and RepoSync objects.")
	Cmd.Flags().BoolVar(&resourceStatus, flagResources, true, "Displays detailed status for individual resources managed by RootSync or RepoSync objects. Defaults to true.")
	Cmd.Flags().StringVar(&name, flagName, "", "Filters the status output by the specified RootSync or RepoSync name.")
}
