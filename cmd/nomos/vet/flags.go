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
	"time"

	"kpt.dev/configsync/pkg/api/configsync"
)

const (
	// flagNamespace defines the namespace for validating a Namespace Repo.
	flagNamespace = "namespace"
	// flagKeepOutput defines if the hydrated output should be kept.
	flagKeepOutput = "keep-output"
	// flagThreshold defines the maximum allowed objects per repository.
	flagThreshold = "threshold"
	// flagOutput defines the location of the hydrated output.
	flagOutput = "output"

	// Note: Other flags like --clusters, --path, --skip-api-server-check,
	// --source-format, --output-format, --api-server-timeout are global flags
	// defined in kpt.dev/configsync/cmd/nomos/flags and added to the command
	// in command.go. They are accessed via the nomosflags package.
)

var (
	// namespaceValue stores the value of the --namespace flag.
	namespaceValue string

	// keepOutput stores the value of the --keep-output flag.
	keepOutput bool

	// threshold stores the value of the --threshold flag.
	threshold int

	// outPath stores the value of the --output flag (location for hydrated files).
	outPath string
)

// vetOptions encapsulates all options for the vet command's execution logic.
// This struct will be populated from flags and passed to executeVet.
type vetOptions struct {
	// Fields from global flags (kpt.dev/configsync/cmd/nomos/flags)
	Path             string
	SkipAPIServer    bool
	SourceFormat     configsync.SourceFormat
	OutputFormat     string // Example: "yaml", "json" - though vet might not use this directly for its primary output
	APIServerTimeout time.Duration
	// Clusters is also global (nomosflags.Clusters, nomosflags.AllClusters())
	// Not explicitly in original vetOptions struct, but might be used by underlying logic.

	// Fields from local flags (defined in this package)
	Namespace      string // namespaceValue
	KeepOutput     bool   // keepOutput
	MaxObjectCount int    // threshold
	OutputPath     string // outPath
}
