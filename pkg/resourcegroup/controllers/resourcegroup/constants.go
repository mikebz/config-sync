// Copyright 2024 Google LLC
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

package resourcegroup

import "github.com/GoogleContainerTools/config-sync/pkg/api/configsync"

const (
	// ManagerContainerName is the name of the manager container of the resource-group controller.
	ManagerContainerName = "manager"

	// FieldManager is the field manager name used by the resource-group controller.
	// This avoids conflicts with the reconciler and reconciler-manager.
	FieldManager = configsync.ConfigSyncPrefix + "resource-group-controller"
)
