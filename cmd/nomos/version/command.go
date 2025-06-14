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

package version

import (
	"os"

	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
	"kpt.dev/configsync/pkg/client/restconfig"
	pkgversion "kpt.dev/configsync/pkg/version" // Renamed to avoid conflict
)

// clientVersionFunc is a function type for obtaining the client version.
// This allows for easier testing by mocking the version.
var clientVersionFunc = func() string {
	return pkgversion.VERSION
}

// Cmd is the Cobra object representing the nomos version command.
var Cmd = &cobra.Command{
	Use:   "version",
	Short: "Prints the version of ACM for each cluster as well this CLI",
	Long: `Prints the version of Configuration Management installed on each cluster and the version
of the "nomos" client binary for debugging purposes.`,
	Example: `  nomos version`,
	Args:    cobra.ExactArgs(0), // nomos version does not take arguments
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true

		// Call the main execution logic from exec.go
		// nomosflags.Contexts and nomosflags.ClientTimeout are global flags.
		// os.Stdout is passed as the writer for the output.
		return executeVersion(cmd.Context(), nomosflags.Contexts, nomosflags.ClientTimeout, os.Stdout)
	},
}

func init() {
	// Register global flags from the nomosflags package
	nomosflags.AddContexts(Cmd) // Provides nomosflags.Contexts
	Cmd.Flags().DurationVar(&nomosflags.ClientTimeout, "timeout", restconfig.DefaultTimeout, "Timeout for connecting to each cluster")
}
