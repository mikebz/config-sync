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

const (
	// flagForce defines if the directory should be initialized even if nonempty.
	flagForce = "force"

	// Note: The --path flag is a global flag defined in
	// kpt.dev/configsync/cmd/nomos/flags and added to the command
	// in command.go. It is accessed via the nomosflags package.
)

var (
	// forceValue stores the value of the --force flag.
	// It's true if the directory should be initialized even if nonempty.
	forceValue bool
)
