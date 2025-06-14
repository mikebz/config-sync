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

package migrate

import (
	"time"
)

const (
	// flagDryRun defines if the command should only print the migration output without applying changes.
	flagDryRun = "dry-run"
	// flagWaitTimeout defines the timeout duration for waiting for conditions to be true.
	flagWaitTimeout = "wait-timeout"
	// flagRemoveConfigManagement defines if the ConfigManagement operator and CRD should be removed.
	flagRemoveConfigManagement = "remove-configmanagement"

	// Note: --contexts and --connect-timeout flags are global flags defined in
	// kpt.dev/configsync/cmd/nomos/flags and added to the command
	// in command.go. They are accessed via the nomosflags package.
)

var (
	// dryRun stores the value of the --dry-run flag.
	// If true, the command will only print the migration output.
	dryRun bool

	// waitTimeout stores the value of the --wait-timeout flag.
	// It's the duration to wait for various conditions during migration.
	waitTimeout time.Duration

	// removeConfigManagement stores the value of the --remove-configmanagement flag.
	// If true, the ConfigManagement operator and CRD will be removed after migration.
	removeConfigManagement bool
)
