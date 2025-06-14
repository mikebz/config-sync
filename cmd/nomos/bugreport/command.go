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

package bugreport

import (
	"github.com/spf13/cobra"
	"kpt.dev/configsync/pkg/api/configmanagement"
	"kpt.dev/configsync/pkg/client/restconfig"
	"kpt.dev/configsync/cmd/nomos/flags"
)

// Cmd is the Cobra command for the bugreport command.
var Cmd = &cobra.Command{
	Use:   "bug-report",
	Short: "Collects information to submit with a bug report.",
	Long: `Collects information about the users environment and Nomos installation
to submit with a bug report.

This command will create a zip file containing:
  - The Nomos version.
  - The Nomos status.
  - The Nomos logs.
  - The resource hierarchy from the declared configuration.
  - The full resource anmes from the declared configuration.
  - The full resource names from the cluster.
  - The full resource names that are out of sync.
  - The output of kubectl get all, cluster-info, api-resources, and version.
  - Information about the users OS and shell.
  - The structure of the users config directory.
  - The parse and hydration results for the users config directory.
  - Logs from the Operator, Admission Webhook, and OpenTelemetry Collector.`,
	Args: cobra.ExactArgs(0),
	// Don't show usage on error, as argument validation passed.
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The default value for flags.ClientTimeout is 0, which means no timeout.
		// We preserve the original default timeout for bug-report.
		if flags.ClientTimeout == 0 {
			flags.ClientTimeout = restconfig.DefaultTimeout
		}
		return execute(cmd.Context(), cmd)
	},
}

func init() {
	// Define flags for the bugreport command.
	// These flags are accessible from the rootCmd.
	Cmd.Flags().StringVar(&outputDir, flagOutputDir, ".", "The directory to write the bug report to.")
	Cmd.Flags().BoolVar(&allNamespaces, flagAllNamespaces, false, "If true, collect information from all namespaces. Otherwise, only collect information from the nomos namespace.")
	Cmd.Flags().BoolVar(&includeNomosSystem, flagIncludeNomosSystem, true, "If true, include the nomos-system namespace in the bug report.")
	Cmd.Flags().BoolVar(&includeSecrets, flagIncludeSecrets, false, "If true, include secrets in the bug report. This may include sensitive information.")
	Cmd.Flags().BoolVar(&includeLogs, flagIncludeLogs, true, "If true, include logs in the bug report.")
	Cmd.Flags().BoolVar(&includeYaml, flagIncludeYaml, true, "If true, include yaml in the bug report.")

	// TODO: The following flag is defined in the original bugreport.go, but it's a global flag.
	// It should be handled by the rootCmd.
	Cmd.Flags().DurationVar(&flags.ClientTimeout, "timeout", restconfig.DefaultTimeout, "Timeout for connecting to the cluster")
	// Add an alias for the "bugreport" command for backward compatibility.
	Cmd.Aliases = []string{"bugreport"}
	// Set the help text for the command.
	Cmd.Short = Cmd.Short + " (alias: bugreport)"
	Cmd.Long = Cmd.Long + "\n\n" + configmanagement.CLIName + " bug-report is an alias for " + configmanagement.CLIName + " bugreport."
}
