// Copyright 2025 Google LLC
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

package ask

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"kpt.dev/configsync/cmd/nomos/flags"
	"kpt.dev/configsync/pkg/bugreport"
	"kpt.dev/configsync/pkg/client/restconfig"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var question string
var model string
var geminikey string

func init() {
	Cmd.Flags().StringVar(&question, "q", "describe my clusters",
		"The question about your Config Sync clusters")
	Cmd.Flags().StringVar(&model, "model", "gemini-2.0-flash",
		"Model to use in analysis")
	Cmd.Flags().StringVar(&geminikey, "geminikey", "",
		"Gemini key to use, if not provided we will also look for GEMINI_API_KEY env var")

}

// Cmd retrieves readers for all relevant nomos container logs and cluster state commands and writes them to a zip file
var Cmd = &cobra.Command{
	Use:   "ask",
	Short: "Ask gemini a question about your Config Sync clusters",
	Long:  "This command takes all the information from the clusters and allows you to ask a question with context.",
	RunE: func(cmd *cobra.Command, _ []string) error {

		cfg, err := restconfig.NewRestConfig(flags.ClientTimeout)
		if err != nil {
			return fmt.Errorf("failed to create rest config: %w", err)
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return fmt.Errorf("failed to create kubernetes client set: %w", err)
		}
		c, err := client.New(cfg, client.Options{})
		if err != nil {
			return fmt.Errorf("failed to create kubernetes client: %w", err)
		}

		report, err := bugreport.New(cmd.Context(), c, cs)
		if err != nil {
			return fmt.Errorf("failed to initialize bug reporter: %w", err)
		}

		if err = report.Open(); err != nil {
			return err
		}

		/*report.WriteRawInZip(report.FetchLogSources(cmd.Context()))
		report.WriteRawInZip(report.FetchResources(cmd.Context()))
		report.WriteRawInZip(report.FetchCMSystemPods(cmd.Context()))
		report.AddNomosStatusToZip(cmd.Context())
		report.AddNomosVersionToZip(cmd.Context())
		*/
		report.Close()
		return nil
	},
}
