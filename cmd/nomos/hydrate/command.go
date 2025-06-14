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

package hydrate

import (
	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
	"kpt.dev/configsync/pkg/api/configsync"
)

// Cmd is the Cobra command for the hydrate command.
var Cmd = &cobra.Command{
	Use:   "hydrate",
	Short: "Compiles the local repository to the exact form that would be sent to the APIServer.",
	Long: `Compiles the local repository to the exact form that would be sent to the APIServer.

The output directory consists of one directory per declared Cluster, and defaultcluster/ for
clusters without declarations. Each directory holds the full set of configs for a single cluster,
which you could kubectl apply -fR to the cluster, or have Config Sync sync to the cluster.`,
	Args: cobra.ExactArgs(0), // Original command takes no direct arguments.
	RunE: func(cmd *cobra.Command, args []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true

		// Pass all necessary flag values to the execution function.
		// Global flags are accessed via the nomosflags package.
		// Local flags (flatOutput, outputPath) are from this package.
		return executeHydrate(
			cmd.Context(),
			nomosflags.Path,
			nomosflags.Clusters,
			nomosflags.AllClusters(),
			nomosflags.SkipAPIServerCheck,
			configsync.SourceFormat(nomosflags.SourceFormat),
			nomosflags.OutputFormat,
			nomosflags.APIServerTimeout,
			flatOutput,
			outputPath,
		)
	},
}

func init() {
	// Register flags specific to the hydrate command
	Cmd.Flags().BoolVar(&flatOutput, flagFlat, false, "If enabled, print all output to a single file.")
	Cmd.Flags().StringVar(&outputPath, flagOutput, nomosflags.DefaultHydrationOutput,
		`Location to write hydrated configuration to.

If --flat is not enabled, writes each resource manifest as a
separate file. You may run "kubectl apply -fR" on the result to apply
the configuration to a cluster. If the repository declares any Cluster
resources, contains a subdirectory for each Cluster.

If --flat is enabled, writes to the, writes a single file holding all
resource manifests. You may run "kubectl apply -f" on the result to
apply the configuration to a cluster.`)

	// Add global flags from the nomosflags package
	nomosflags.AddClusters(Cmd)
	nomosflags.AddPath(Cmd) // This provides nomosflags.Path
	nomosflags.AddSkipAPIServerCheck(Cmd)
	nomosflags.AddSourceFormat(Cmd) // This provides nomosflags.SourceFormat
	nomosflags.AddOutputFormat(Cmd) // This provides nomosflags.OutputFormat
	nomosflags.AddAPIServerTimeout(Cmd)
}
