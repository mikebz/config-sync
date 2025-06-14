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

package vet

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/importer/analyzer/validation/system"
)

// Cmd is the Cobra object representing the nomos vet command.
var Cmd = &cobra.Command{
	Use:   "vet",
	Short: "Validate an Anthos Configuration Management directory",
	Long: `Validate an Anthos Configuration Management directory
Checks for semantic and syntactic errors in an Anthos Configuration Management directory
that will interfere with applying resources. Prints found errors to STDERR and
returns a non-zero error code if any issues are found.
`,
	Example: `  nomos vet
  nomos vet --path=my/directory
  nomos vet --path=/path/to/my/directory`,
	Args: cobra.ExactArgs(0), // nomos vet does not take direct arguments
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true

		// Prepare options for the execution logic
		opts := vetOptions{
			// Global flags are accessed via nomosflags package
			Path:             nomosflags.Path,
			SkipAPIServer:    nomosflags.SkipAPIServerCheck,
			SourceFormat:     configsync.SourceFormat(nomosflags.SourceFormat),
			OutputFormat:     nomosflags.OutputFormat, // Though not directly used by vetOptions in vet.go, include for completeness if logic moves
			APIServerTimeout: nomosflags.APIServerTimeout,
			// Local flags are from this package's flags.go
			Namespace:      namespaceValue,
			KeepOutput:     keepOutput,
			MaxObjectCount: threshold,
			OutputPath:     outPath,
			// Clusters flag is also global (nomosflags.Clusters, nomosflags.AllClusters())
			// It seems the original vetOptions didn't explicitly take clusters.
			// The underlying logic might use it directly from nomosflags or it might not be relevant for single-dir vet.
			// For now, sticking to what was passed to runVet.
		}

		// Call the main execution logic from exec.go
		return executeVet(cmd.Context(), cmd.OutOrStderr(), opts)
	},
}

func init() {
	// Register global flags from the nomosflags package
	nomosflags.AddClusters(Cmd)
	nomosflags.AddPath(Cmd)
	nomosflags.AddSkipAPIServerCheck(Cmd)
	nomosflags.AddSourceFormat(Cmd)
	nomosflags.AddOutputFormat(Cmd)
	nomosflags.AddAPIServerTimeout(Cmd)

	// Register flags specific to the vet command
	Cmd.Flags().StringVar(&namespaceValue, flagNamespace, "",
		fmt.Sprintf(
			"If set, validate the repository as a Namespace Repo with the provided name. Automatically sets --source-format=%s",
			configsync.SourceFormatUnstructured))

	Cmd.Flags().BoolVar(&keepOutput, flagKeepOutput, false,
		`If enabled, keep the hydrated output`)

	// The --threshold flag
	Cmd.Flags().IntVar(&threshold, flagThreshold, system.DefaultMaxObjectCountDisabled,
		fmt.Sprintf(`Maximum objects allowed per repository; errors if exceeded. Omit or set to %d to disable. `, system.DefaultMaxObjectCountDisabled)+
			fmt.Sprintf(`Provide flag without value for default (%d), or use --threshold=N for a specific limit.`, system.DefaultMaxObjectCount))
	// Using NoOptDefVal allows the flag to be specified without a value.
	Cmd.Flags().Lookup(flagThreshold).NoOptDefVal = strconv.Itoa(system.DefaultMaxObjectCount)

	Cmd.Flags().StringVar(&outPath, flagOutput, nomosflags.DefaultHydrationOutput, // Default from global flags
		`Location of the hydrated output`)
}
