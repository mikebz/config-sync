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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"kpt.dev/configsync/cmd/nomos/flags"
	"kpt.dev/configsync/pkg/bugreport"
	"kpt.dev/configsync/pkg/client/restconfig"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// execute is the main entry point for the bug-report command.
// It collects information about the user's environment and Nomos installation
// and creates a zip file with the collected data.
func execute(ctx context.Context, cmd *cobra.Command) error {
	// Send all logs to STDERR.
	if err := cmd.InheritedFlags().Lookup("stderrthreshold").Value.Set("0"); err != nil {
		klog.Errorf("failed to increase logging STDERR threshold: %v", err)
	}

	// Create REST config for connecting to the Kubernetes cluster.
	cfg, err := restconfig.NewRestConfig(flags.ClientTimeout)
	if err != nil {
		return fmt.Errorf("failed to create rest config: %w", err)
	}

	// Create Kubernetes clientset.
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client set: %w", err)
	}

	// Create Kubernetes client.
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Create a new bug reporter.
	// The outputDir flag is now handled by the bugreport package.
	// The other flags (allNamespaces, includeNomosSystem, includeSecrets, includeLogs, includeYaml)
	// are not directly used by the bugreport package, but we might need to pass them if we reimplement
	// the logic of bugreport.New or its methods. For now, we rely on the default behavior of bugreport.
	report, err := bugreport.New(ctx, c, cs,
		bugreport.WithOutputDir(outputDir),
		bugreport.WithAllNamespaces(allNamespaces),
		bugreport.WithIncludeNomosSystem(includeNomosSystem),
		bugreport.WithIncludeSecrets(includeSecrets),
		bugreport.WithIncludeLogs(includeLogs),
		bugreport.WithIncludeYAML(includeYaml),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize bug reporter: %w", err)
	}

	// Create a subdirectory for the bug report with a timestamp.
	// This is handled by the bugreport package now.
	// subDir := filepath.Join(outputDir, fmt.Sprintf("nomos-bug-report-%s", time.Now().Format("20060102-150405")))
	// if err := os.MkdirAll(subDir, 0750); err != nil {
	// 	return fmt.Errorf("failed to create sub-directory %q: %w", subDir, err)
	// }
	// report.OutputDir = subDir // This needs to be set if not handled by New.

	// Open the bug report for writing.
	if err = report.Open(); err != nil {
		return fmt.Errorf("failed to open bug report: %w", err)
	}
	defer report.Close() // Ensure the report is closed even if errors occur.

	// Collect and write various pieces of information to the zip file.
	// These calls mirror the logic in the original bugreport.go.
	report.WriteRawInZip(report.FetchLogSources(ctx))
	report.WriteRawInZip(report.FetchResources(ctx))
	report.WriteRawInZip(report.FetchCMSystemPods(ctx))
	report.AddNomosStatusToZip(ctx)
	report.AddNomosVersionToZip(ctx)

	// The report.Close() is handled by defer.
	// The final zip file creation and message are handled by the bugreport package.
	fmt.Printf("Bug report written to %s.zip\n", report.ArchivePath())
	return nil
}

// The following constants and variables were part of the original exec.go scaffold,
// but they are not directly used if we rely on the pkg/bugreport for collection logic.
// They are kept here for reference or if we need to customize collection further.

const (
	// nomosSystemNamespace is the namespace where the nomos components are installed.
	nomosSystemNamespace = "nomos-system"
	// configManagementSystemNamespace is the namespace where the Config Management components are installed.
	configManagementSystemNamespace = "config-management-system"
	// configSyncSystemNamespace is the namespace where the Config Sync components are installed.
	configSyncSystemNamespace = "config-sync-system"
	// resourceGroupSystemNamespace is the namespace where the Resource Group components are installed.
	resourceGroupSystemNamespace = "resource-group-system"
)

// namespaces is the list of namespaces to collect information from.
var defaultNamespaces = []string{
	nomosSystemNamespace,
	configManagementSystemNamespace,
	configSyncSystemNamespace,
	resourceGroupSystemNamespace,
}

// The bugReport struct and its methods (collect, collectNomosVersion, etc.)
// were part of the scaffold but are superseded by the bugreport package's capabilities.
// They are removed to avoid confusion and duplication.
// If fine-grained control over collection is needed in the future,
// these could be reintroduced or the bugreport package could be extended.
