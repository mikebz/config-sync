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

package initialize

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Renamed to avoid conflict
)

// Cmd is the Cobra object representing the nomos init command
var Cmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a Anthos Configuration Management directory",
	Long: `Initialize a Anthos Configuration Management directory

Set up a working Anthos Configuration Management directory with a default Repo object, documentation,
and directories.

By default, does not initialize directories containing files. Use --force to
initialize nonempty directories.`,
	Example: `  nomos init
  nomos init --path=my/directory
  nomos init --path=/path/to/my/directory`,
	Args: cobra.ExactArgs(0),
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Don't show usage on error, as argument validation passed.
		cmd.SilenceUsage = true
		// Call the execution logic from exec.go
		// nomosflags.Path will provide the value for the --path flag.
		// forceValue is the local flag defined in flags.go.
		return executeInit(nomosflags.Path, forceValue)
	},
	PostRunE: func(_ *cobra.Command, _ []string) error {
		// Print "Done!" message after successful execution.
		_, err := fmt.Fprintf(os.Stdout, "Done!\n")
		return err
	},
}

func init() {
	// Register global flags from the nomosflags package.
	// The --path flag is registered here.
	nomosflags.AddPath(Cmd)

	// Register flags specific to the initialize command.
	// The --force flag is registered here and its value will be stored in forceValue (from flags.go).
	Cmd.Flags().BoolVar(&forceValue, flagForce, false,
		"write to directory even if nonempty, overwriting conflicting files")
}
