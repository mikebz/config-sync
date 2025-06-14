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

// This file is for defining flags specific to the `nomos version` command.
// Currently, the `version` command uses global flags:
//   - `--contexts` (from kpt.dev/configsync/cmd/nomos/flags.Contexts)
//   - `--timeout` (from kpt.dev/configsync/cmd/nomos/flags.ClientTimeout)
// These are registered in command.go and accessed via the nomosflags package.
// Therefore, this file does not declare new `var`s for flag storage
// but can be used for constants related to these flags if needed elsewhere in this package.

const (
	// flagContextsName is an example if we needed to reference the global flag name.
	// flagContextsName = "contexts"
	// flagTimeoutName = "timeout"
)

// No command-specific flag variables are needed here yet.
