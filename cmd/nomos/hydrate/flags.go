// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable lawolicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hydrate

import (
	nomosflags "kpt.dev/configsync/cmd/nomos/flags"
)

const (
	// flagFlat defines if the output should be a single file.
	flagFlat = "flat"
	// flagOutput defines the location to write hydrated configuration to.
	flagOutput = "output"

	// Note: Other flags like --clusters, --path, --skip-api-server-check,
	// --source-format, --output-format, --api-server-timeout are defined
	// globally in kpt.dev/configsync/cmd/nomos/flags and added to the command
	// in command.go. They are accessed via the nomosflags package.
)

var (
	// flatOutput, if enabled, prints all output to a single file.
	flatOutput bool
	// outputPath specifies the location to write hydrated configuration to.
	// Default is handled by the original package, typically "hydrated/".
	outputPath string = nomosflags.DefaultHydrationOutput
)
