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

package status

import (
	"time"
)

const (
	// flagPoll defines the interval for continuous polling.
	flagPoll = "poll"
	// flagNamespace filters status by RootSync/RepoSync namespace.
	flagNamespace = "namespace"
	// flagResources toggles display of detailed resource status.
	flagResources = "resources"
	// flagName filters status by RootSync/RepoSync name.
	flagName = "name"

	// Note: --contexts and --timeout (ClientTimeout) flags are global flags
	// defined in kpt.dev/configsync/cmd/nomos/flags and added to the command
	// in command.go. They are accessed via the nomosflags package.
)

var (
	// pollingInterval stores the value of the --poll flag.
	pollingInterval time.Duration

	// namespace stores the value of the --namespace flag.
	namespace string

	// resourceStatus stores the value of the --resources flag.
	resourceStatus bool

	// name stores the value of the --name flag.
	name string
)

// Constants for status messages, can be kept here or moved to exec.go if only used there.
// For now, keeping them here as they are related to the presentation part of status.
const (
	pendingMsg     = "PENDING"
	syncedMsg      = "SYNCED"
	stalledMsg     = "STALLED"
	reconcilingMsg = "RECONCILING"
)
