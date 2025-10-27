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

package parse

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoogleContainerTools/config-sync/pkg/api/configmanagement"
	"github.com/GoogleContainerTools/config-sync/pkg/api/configsync"
	"github.com/GoogleContainerTools/config-sync/pkg/api/configsync/v1beta1"
	"github.com/GoogleContainerTools/config-sync/pkg/applier"
	applierfake "github.com/GoogleContainerTools/config-sync/pkg/applier/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/applyset"
	"github.com/GoogleContainerTools/config-sync/pkg/core"
	"github.com/GoogleContainerTools/config-sync/pkg/core/k8sobjects"
	"github.com/GoogleContainerTools/config-sync/pkg/declared"
	"github.com/GoogleContainerTools/config-sync/pkg/diff/difftest"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/analyzer/ast"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/filesystem"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/filesystem/cmpath"
	fsfake "github.com/GoogleContainerTools/config-sync/pkg/importer/filesystem/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/reader"
	"github.com/GoogleContainerTools/config-sync/pkg/kinds"
	"github.com/GoogleContainerTools/config-sync/pkg/metadata"
	"github.com/GoogleContainerTools/config-sync/pkg/metrics"
	"github.com/GoogleContainerTools/config-sync/pkg/remediator/conflict"
	remediatorfake "github.com/GoogleContainerTools/config-sync/pkg/remediator/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/rootsync"
	"github.com/GoogleContainerTools/config-sync/pkg/status"
	"github.com/GoogleContainerTools/config-sync/pkg/syncer/reconcile/fight"
	syncertest "github.com/GoogleContainerTools/config-sync/pkg/syncer/syncertest/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/openapitest"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testerrors"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testmetrics"
	discoveryutil "github.com/GoogleContainerTools/config-sync/pkg/util/discovery"
	webhookconfiguration "github.com/GoogleContainerTools/config-sync/pkg/webhook/configuration"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/clock"
	fakeclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cli-utils/pkg/common"
	"sigs.k8s.io/cli-utils/pkg/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	rootSyncName       = "my-rs"
	rootReconcilerName = "root-reconciler-my-rs"
	nilGitContext      = `{"repo":""}`
	testGitCommit      = "example-commit"
	otherRootSyncName  = "other-root-sync"
)

func gitSpec(repo string, auth configsync.AuthType) core.MetaMutator {
	return func(o client.Object) {
		if rs, ok := o.(*v1beta1.RootSync); ok {
			rs.Spec.Git = &v1beta1.Git{
				Repo: repo,
				Auth: auth,
			}
		}
	}
}

func TestRootReconciler_ParseAndUpdate(t *testing.T) {
	fakeMetaTime := metav1.Now().Rfc3339Copy() // truncate to second precision
	fakeTime := fakeMetaTime.Time
	fakeClock := fakeclock.NewFakeClock(fakeTime)

	applySetID := applyset.IDFromSync(rootSyncName, declared.RootScope)
	otherRootSyncApplySetID := applyset.IDFromSync(otherRootSyncName, declared.RootScope)

	// TODO: Test different source types and inputs
	fileSource := FileSource{
		SourceType:   configsync.GitSource,
		SourceRepo:   "example-repo",
		SourceRev:    testGitCommit,
		SourceBranch: "example-branch",
		SourceDir:    "example-dir",
	}
	sourceStatusOutput := &v1beta1.GitStatus{
		Repo:     fileSource.SourceRepo,
		Revision: testGitCommit,
		Branch:   fileSource.SourceBranch,
		Dir:      fileSource.SourceDir.SlashPath(),
	}
	gitContextOutput := fmt.Sprintf(`{"repo":%q,"branch":%q,"rev":%q}`,
		fileSource.SourceRepo, fileSource.SourceBranch, fileSource.SourceRev)

	reconcilerStatus := &ReconcilerStatus{
		SourceStatus: &SourceStatus{
			Commit:     testGitCommit,
			Errs:       nil,
			LastUpdate: fakeMetaTime,
		},
		RenderingStatus: &RenderingStatus{
			Commit:            testGitCommit,
			Message:           RenderingSkipped,
			Errs:              nil,
			LastUpdate:        fakeMetaTime,
			RequiresRendering: false,
		},
		// TODO: test different sync status inputs, like retries
		SyncStatus: nil,
	}

	// parseAndUpdate doesn't read the spec from the RSync. It uses the
	// Parser.Options, which are populated from the Reconciler's input flags.
	// But it does read from the RSync status, before updating it.
	rootSyncInput := &v1beta1.RootSync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rootSyncName,
			Namespace: configmanagement.ControllerNamespace,
		},
		// Status is normally initialized before calling parseAndUpdate
		Status: v1beta1.RootSyncStatus{
			Status: v1beta1.Status{
				// Run updates the source status after fetching
				Source: v1beta1.SourceStatus{
					Commit:       testGitCommit,
					LastUpdate:   fakeMetaTime,
					ErrorSummary: &v1beta1.ErrorSummary{},
					Git:          sourceStatusOutput.DeepCopy(),
				},
				// Read updates the rendering status.
				// TODO: Test different rendering status inputs
				Rendering: v1beta1.RenderingStatus{
					Message:      RenderingSkipped,
					Commit:       testGitCommit,
					LastUpdate:   fakeMetaTime,
					ErrorSummary: &v1beta1.ErrorSummary{},
					Git:          sourceStatusOutput.DeepCopy(),
				},
				// Sync is normally empty on the first call to parseAndUpdate.
				// TODO: Test different sync status inputs, like retries
			},
			// Run & read both update the Syncing condition
			// TODO: Test different sync status inputs, like retries and stalled
			Conditions: []v1beta1.RootSyncCondition{
				{
					Type:               v1beta1.RootSyncSyncing,
					Status:             metav1.ConditionTrue,
					LastUpdateTime:     fakeMetaTime,
					LastTransitionTime: fakeMetaTime,
					Reason:             "Rendering",
					Message:            RenderingSkipped,
					Commit:             testGitCommit,
					ErrorSummary:       &v1beta1.ErrorSummary{},
				},
			},
		},
	}

	rootSyncOutput := &v1beta1.RootSync{
		// FakeClient populates TypeMeta when converting from unstructured to typed.
		TypeMeta: metav1.TypeMeta{
			Kind:       configsync.RootSyncKind,
			APIVersion: v1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rootSyncName,
			Namespace: configmanagement.ControllerNamespace,
			// FakeClient populates a few meta values
			UID: "1",
			// Create + Update (source status + syncing condition) + Update (sync status + syncing condition)
			ResourceVersion: "3",
			Generation:      1, // Spec is never updated after creation
		},
		// parseAndUpdate doesn't update the RSync spec.
		Status: v1beta1.RootSyncStatus{
			Status: v1beta1.Status{
				LastSyncedCommit: testGitCommit,
				// Fetch updates the Source status (skipped, because no change)
				// Parse updates the Source status (only Syncing condition changed)
				// TODO: Test parse errors
				Source: v1beta1.SourceStatus{
					Commit:       testGitCommit,
					LastUpdate:   fakeMetaTime,
					ErrorSummary: &v1beta1.ErrorSummary{},
					Git:          sourceStatusOutput.DeepCopy(),
				},
				// Rendering shouldn't change
				Rendering: *rootSyncInput.Status.Rendering.DeepCopy(),
				// Update updates the Sync status
				// TODO: Test update errors
				Sync: v1beta1.SyncStatus{
					Commit:       testGitCommit,
					LastUpdate:   fakeMetaTime,
					ErrorSummary: &v1beta1.ErrorSummary{},
					Git:          sourceStatusOutput.DeepCopy(),
				},
			},
			// Both parsing and updating should update the Syncing condition,
			// but we're only testing the final status here.
			Conditions: []v1beta1.RootSyncCondition{
				{
					Type:               v1beta1.RootSyncSyncing,
					Status:             metav1.ConditionFalse,
					LastUpdateTime:     fakeMetaTime,
					LastTransitionTime: fakeMetaTime,
					Reason:             "Sync",
					Message:            "Sync Completed",
					Commit:             testGitCommit,
					ErrorSummary:       &v1beta1.ErrorSummary{},
				},
			},
		},
	}

	testCases := []struct {
		name                string
		format              configsync.SourceFormat
		namespaceStrategy   configsync.NamespaceStrategy
		existingObjects     []client.Object
		parseOutputs        []fsfake.ParserOutputs
		expectedObjsToApply []ast.FileObject
		expectedRootSync    *v1beta1.RootSync
	}{
		{
			name:   "no objects",
			format: configsync.SourceFormatUnstructured,
			existingObjects: []client.Object{
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{}, // One Parse call, no results or errors
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "implicit namespace if unstructured and not present",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "no implicit namespace if namespaceStrategy is explicit",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyExplicit,
			existingObjects: []client.Object{
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "implicit namespace if unstructured, present and self-managed",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				k8sobjects.NamespaceObject("foo",
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, rootSyncName)),
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "no implicit namespace if unstructured, present, but managed by others",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{k8sobjects.NamespaceObject("foo",
				core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
				core.Label(metadata.ApplySetPartOfLabel, otherRootSyncApplySetID),
				core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
				metadata.WithManagementMode(metadata.ManagementEnabled),
				core.Annotation(metadata.GitContextKey, gitContextOutput),
				core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
				core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(otherRootSyncName, configmanagement.ControllerNamespace)),
				core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
				difftest.ManagedBy(declared.RootScope, otherRootSyncName)),
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "no implicit namespace if unstructured, present, but unmanaged",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				k8sobjects.NamespaceObject("foo"),
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "no implicit namespace if unstructured and namespace is config-management-system",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.RootSyncV1Beta1("test", k8sobjects.WithRootSyncSourceType(configsync.GitSource), gitSpec("https://github.com/test/test.git", configsync.AuthNone)),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.RootSyncV1Beta1("test", gitSpec("https://github.com/test/test.git", configsync.AuthNone),
					k8sobjects.WithRootSyncSourceType(configsync.GitSource),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1beta1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, fmt.Sprintf("namespaces/%s/test.yaml", configsync.ControllerNamespace)),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "configsync.gke.io_rootsync_config-management-system_test"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "multiple objects share a single implicit namespace",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("bar")),
						k8sobjects.ConfigMap(core.Namespace("bar")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_bar_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.ConfigMap(core.Namespace("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/configmap.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_configmap_bar_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_bar"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
		{
			name:              "multiple implicit namespaces",
			format:            configsync.SourceFormatUnstructured,
			namespaceStrategy: configsync.NamespaceStrategyImplicit,
			existingObjects: []client.Object{
				k8sobjects.NamespaceObject("foo"), // foo exists but not managed, should NOT be added as an implicit namespace
				// bar not exists, should be added as an implicit namespace
				k8sobjects.NamespaceObject("baz", // baz exists and self-managed, should be added as an implicit namespace
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_baz"),
					difftest.ManagedBy(declared.RootScope, rootSyncName)),
				rootSyncInput.DeepCopy(),
			},
			parseOutputs: []fsfake.ParserOutputs{
				{
					FileObjects: []ast.FileObject{
						k8sobjects.Role(core.Namespace("foo")),
						k8sobjects.Role(core.Namespace("bar")),
						k8sobjects.ConfigMap(core.Namespace("bar")),
						k8sobjects.Role(core.Namespace("baz")),
						k8sobjects.ConfigMap(core.Namespace("baz")),
					},
				},
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.Role(core.Namespace("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_bar_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.ConfigMap(core.Namespace("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/configmap.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_configmap_bar_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.Role(core.Namespace("baz"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_baz_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.ConfigMap(core.Namespace("baz"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/configmap.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_configmap_baz_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("bar"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_bar"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("baz"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, gitContextOutput),
					core.Annotation(metadata.SyncTokenAnnotationKey, testGitCommit),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_baz"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
			expectedRootSync: rootSyncOutput.DeepCopy(),
		},
	}

	converter, err := openapitest.ValueConverterForTest()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			exporter, err := testmetrics.NewTestExporter()
			if err != nil {
				t.Fatalf("Failed to create test exporter: %v", err)
			}
			defer exporter.ClearMetrics()
			fakeClient := syncertest.NewClient(t, core.Scheme, tc.existingObjects...)
			// We're not testing the Parser here, just how the `root` calls the
			// Parser. So the outputs are faked.
			// Then we validate the inputs at the end of the test.
			fakeConfigParser := &fsfake.ConfigParser{
				Outputs: tc.parseOutputs,
			}
			// We're not testing the applier here, so the output doesn't matter.
			// We just need to make sure it only gets called once.
			// Then we validate the inputs at the end of the test.
			fakeApplier := &applierfake.Applier{
				ApplyOutputs: []applierfake.ApplierOutputs{
					{}, // One Apply call
				},
				ApplyClient: syncertest.NewClient(t, core.Scheme, tc.existingObjects...),
			}
			tc.existingObjects = append(tc.existingObjects, k8sobjects.RootSyncObjectV1Beta1(rootSyncName))
			files := []cmpath.Absolute{
				"example.yaml",
			}
			state := &ReconcilerState{
				status: reconcilerStatus.DeepCopy(),
				cache: cacheForCommit{
					source: &sourceState{
						spec: GitSourceSpec{
							Repo:     fileSource.SourceRepo,
							Revision: fileSource.SourceRev,
							Branch:   fileSource.SourceBranch,
							Dir:      fileSource.SourceDir.SlashPath(),
						},
						commit:   testGitCommit,
						syncPath: "/",
						files:    files,
					},
				},
				syncErrorCache: NewSyncErrorCache(conflict.NewHandler(), fight.NewHandler()),
			}
			opts := &Options{
				Clock:             fakeClock,
				ConfigParser:      fakeConfigParser,
				SyncName:          rootSyncName,
				Scope:             declared.RootScope,
				ReconcilerName:    rootReconcilerName,
				Client:            fakeClient,
				DiscoveryClient:   syncertest.NewDiscoveryClient(kinds.Namespace(), kinds.Role()),
				Converter:         converter,
				Files:             Files{FileSource: fileSource},
				DeclaredResources: &declared.Resources{},
			}
			rootOpts := &RootOptions{
				Options:           opts,
				SourceFormat:      tc.format,
				NamespaceStrategy: tc.namespaceStrategy,
			}
			recOpts := &ReconcilerOptions{
				Options: opts,
				Updater: &Updater{
					Scope:          opts.Scope,
					Resources:      opts.DeclaredResources,
					Remediator:     &remediatorfake.Remediator{},
					Applier:        fakeApplier,
					SyncErrorCache: state.syncErrorCache,
				},
				FullSyncPeriod:     configsync.DefaultReconcilerFullSyncPeriod,
				StatusUpdatePeriod: configsync.DefaultReconcilerSyncStatusUpdatePeriod,
				RenderingEnabled:   false,
			}
			reconciler := &reconciler{
				options: recOpts,
				syncStatusClient: &rootSyncStatusClient{
					options: opts,
				},
				parser: &rootSyncParser{
					options: rootOpts,
				},
				reconcilerState: state,
			}

			ctx := context.Background()
			trigger := triggerSync
			errs := reconciler.parse(ctx, trigger)
			require.NoError(t, errs, "expected parse not to error")
			errs = reconciler.update(ctx, trigger)
			require.NoError(t, errs, "expected update not to error")

			// Validate that Parser.Parse got the expected inputs from the cached source state.
			expectedParseInputs := []fsfake.ParserInputs{
				{
					FilePaths: reader.FilePaths{
						RootDir:   "/",
						PolicyDir: "",
						Files:     files,
					},
				},
			}
			testutil.AssertEqual(t, expectedParseInputs, fakeConfigParser.Inputs, "unexpected Parser.Parse call inputs")

			// After parsing, the objects set is processed and modified.
			// Validate that the result is stored in state.cache.objsToApply.
			testutil.AssertEqual(t, tc.expectedObjsToApply, state.cache.parse.objsToApply, "unexpected state.cache.objsToApply contents")

			// Build expected apply inputs from the expectedFileObjects
			expectedApplyInputs := []applierfake.ApplierInputs{
				// One Apply call
				{Objects: filesystem.AsCoreObjects(tc.expectedObjsToApply)},
			}
			testutil.AssertEqual(t, expectedApplyInputs, fakeApplier.ApplyInputs, "unexpected Applier.Apply call inputs")

			rs := &v1beta1.RootSync{}
			err = fakeClient.Get(ctx, rootsync.ObjectKey(rootSyncName), rs)
			require.NoError(t, err, "expected get not to error")
			testutil.AssertEqual(t, tc.expectedRootSync, rs)
		})
	}
}

func TestRootReconciler_Parse_DeclaredFields(t *testing.T) {
	applySetID := applyset.IDFromSync(rootSyncName, declared.RootScope)

	testCases := []struct {
		name                string
		webhookEnabled      bool
		existingObjects     []client.Object
		parsed              []ast.FileObject
		expectedObjsToApply []ast.FileObject
	}{
		{
			name:           "has declared-fields annotation, admission webhook is disabled",
			webhookEnabled: false,
			existingObjects: []client.Object{k8sobjects.NamespaceObject("foo",
				core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
				core.Label(metadata.ApplySetPartOfLabel, applySetID),
				metadata.WithManagementMode(metadata.ManagementEnabled),
				core.Annotation(metadata.DeclaredFieldsKey, `{"f:metadata":{"f:annotations":{},"f:labels":{}},"f:rules":{}}`),
				core.Annotation(metadata.GitContextKey, nilGitContext),
				core.Annotation(metadata.SyncTokenAnnotationKey, ""),
				core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
				core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
				difftest.ManagedBy(declared.RootScope, otherRootSyncName))},
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
		{
			name:           "has declared-fields annotation, admission webhook enabled",
			webhookEnabled: true,
			existingObjects: []client.Object{k8sobjects.NamespaceObject("foo",
				core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
				core.Label(metadata.ApplySetPartOfLabel, applySetID),
				metadata.WithManagementMode(metadata.ManagementEnabled),
				core.Annotation(metadata.DeclaredFieldsKey, `{"f:metadata":{"f:annotations":{},"f:labels":{}},"f:rules":{}}`),
				core.Annotation(metadata.GitContextKey, nilGitContext),
				core.Annotation(metadata.SyncTokenAnnotationKey, ""),
				core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
				core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
				difftest.ManagedBy(declared.RootScope, otherRootSyncName)),
			},
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.DeclaredFieldsKey, `{"f:metadata":{"f:annotations":{"f:configmanagement.gke.io/source-path":{}},"f:labels":{"f:configsync.gke.io/declared-version":{}}},"f:rules":{}}`),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
		{
			name:           "has no declared-fields annotation, admission webhook is enabled",
			webhookEnabled: true,
			existingObjects: []client.Object{
				k8sobjects.NamespaceObject("foo",
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, otherRootSyncName)),
				k8sobjects.AdmissionWebhookObject(webhookconfiguration.Name),
			},
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.DeclaredFieldsKey, `{"f:metadata":{"f:annotations":{"f:configmanagement.gke.io/source-path":{}},"f:labels":{"f:configsync.gke.io/declared-version":{}}},"f:rules":{}}`),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
		{
			name:           "has no declared-fields annotation, admission webhook disabled",
			webhookEnabled: false,
			existingObjects: []client.Object{
				k8sobjects.NamespaceObject("foo",
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, otherRootSyncName)),
			},
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
	}

	converter, err := openapitest.ValueConverterForTest()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeConfigParser := &fsfake.ConfigParser{
				Outputs: []fsfake.ParserOutputs{
					// One Parse call, no errors
					{FileObjects: tc.parsed},
				},
			}
			fakeApplier := &applierfake.Applier{
				ApplyOutputs: []applierfake.ApplierOutputs{
					{}, // One Apply call
				},
			}
			tc.existingObjects = append(tc.existingObjects, k8sobjects.RootSyncObjectV1Beta1(rootSyncName))
			state := &ReconcilerState{
				status: &ReconcilerStatus{},
				cache: cacheForCommit{
					source: &sourceState{},
				},
				syncErrorCache: NewSyncErrorCache(conflict.NewHandler(), fight.NewHandler()),
			}
			opts := &Options{
				Clock:             clock.RealClock{}, // TODO: Test with fake clock
				ConfigParser:      fakeConfigParser,
				SyncName:          rootSyncName,
				Scope:             declared.RootScope,
				ReconcilerName:    rootReconcilerName,
				Client:            syncertest.NewClient(t, core.Scheme, tc.existingObjects...),
				DiscoveryClient:   syncertest.NewDiscoveryClient(kinds.Namespace(), kinds.Role()),
				Converter:         converter,
				WebhookEnabled:    tc.webhookEnabled,
				DeclaredResources: &declared.Resources{},
			}
			rootOpts := &RootOptions{
				Options:           opts,
				SourceFormat:      configsync.SourceFormatUnstructured,
				NamespaceStrategy: configsync.NamespaceStrategyExplicit,
			}
			recOpts := &ReconcilerOptions{
				Options: opts,
				Updater: &Updater{
					Scope:          opts.Scope,
					Resources:      opts.DeclaredResources,
					Remediator:     &remediatorfake.Remediator{},
					Applier:        fakeApplier,
					SyncErrorCache: state.syncErrorCache,
				},
				FullSyncPeriod:     configsync.DefaultReconcilerFullSyncPeriod,
				StatusUpdatePeriod: configsync.DefaultReconcilerSyncStatusUpdatePeriod,
				RenderingEnabled:   false,
			}
			reconciler := &reconciler{
				options: recOpts,
				syncStatusClient: &rootSyncStatusClient{
					options: opts,
				},
				parser: &rootSyncParser{
					options: rootOpts,
				},
				reconcilerState: state,
			}

			ctx := context.Background()
			trigger := triggerSync
			errs := reconciler.parse(ctx, trigger)
			require.NoError(t, errs, "expected parse not to error")

			// After parsing, the objects set is processed and modified.
			// Validate that the result is stored in state.cache.objsToApply.
			testutil.AssertEqual(t, tc.expectedObjsToApply, state.cache.parse.objsToApply, "unexpected state.cache.objsToApply contents")
		})
	}
}

func fakeCRD(opts ...core.MetaMutator) ast.FileObject {
	crd := k8sobjects.CustomResourceDefinitionV1Object(opts...)
	crd.Spec.Group = "acme.com"
	crd.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{
		Plural:   "anvils",
		Singular: "anvil",
		Kind:     "Anvil",
	}
	crd.Spec.Scope = apiextensionsv1.NamespaceScoped
	crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{
		{
			Name:    "v1",
			Served:  true,
			Storage: false,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec": {
							Type:     "object",
							Required: []string{"lbs"},
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"lbs": {
									Type:    "integer",
									Minimum: ptr.To(1.0),
									Maximum: ptr.To(9000.0),
								},
							},
						},
					},
				},
			},
		},
	}
	return k8sobjects.FileObject(crd, "cluster/crd.yaml")
}

func fakeFileObjects() []ast.FileObject {
	var fileObjects []ast.FileObject
	for i := 0; i < 500; i++ {
		fileObjects = append(fileObjects, k8sobjects.RoleAtPath(fmt.Sprintf("namespaces/foo/role%v.yaml", i), core.Namespace("foo")))
	}
	return fileObjects
}

func fakeGVKs() []schema.GroupVersionKind {
	var gvks []schema.GroupVersionKind
	for i := 0; i < 500; i++ {
		gvks = append(gvks, schema.GroupVersionKind{Group: fmt.Sprintf("acme%v.com", i), Version: "v1", Kind: "APIService"})
	}
	return gvks
}

func fakeParseError(err error, gvks ...schema.GroupVersionKind) status.MultiError {
	groups := make(map[schema.GroupVersion]error)
	for _, gvk := range gvks {
		gv := gvk.GroupVersion()
		groups[gv] = err
	}
	return status.APIServerError(&discovery.ErrGroupDiscoveryFailed{Groups: groups}, "API discovery failed")
}

func TestRootReconciler_Parse_Discovery(t *testing.T) {
	applySetID := applyset.IDFromSync(rootSyncName, declared.RootScope)

	testCases := []struct {
		name                string
		parsed              []ast.FileObject
		discoveryClient     discoveryutil.ServerResourcer
		expectedError       status.MultiError
		expectedObjsToApply []ast.FileObject
	}{
		{
			// unknown scoped object should not be skipped when sending to applier when discovery call fails
			name:            "unknown scoped object with discovery failure of deadline exceeded error",
			discoveryClient: syncertest.NewDiscoveryClientWithError(context.DeadlineExceeded, kinds.Namespace(), kinds.Role()),
			expectedError:   fakeParseError(context.DeadlineExceeded, kinds.Namespace(), kinds.Role()),
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
				// add a faked obect in parser.parsed without CRD so it's scope will be unknown when validating
				k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy")),
			},
			expectedObjsToApply: nil,
		},
		{
			// unknown scoped object should not be skipped when sending to applier when discovery call fails
			name:            "unknown scoped object with discovery failure of http request canceled failure",
			discoveryClient: syncertest.NewDiscoveryClientWithError(errors.New("net/http: request canceled (Client.Timeout exceeded while awaiting headers)"), kinds.Namespace(), kinds.Role()),
			expectedError:   fakeParseError(errors.New("net/http: request canceled (Client.Timeout exceeded while awaiting headers)"), kinds.Namespace(), kinds.Role()),
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
				// add a faked obect in parser.parsed without CRD so it's scope will be unknown when validating
				k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy")),
			},
			expectedObjsToApply: nil,
		},
		{
			// unknown scoped object should not be skipped when sending to applier when discovery call fails
			name:                "unknown scoped object with discovery failure of 500 deadline exceeded failure",
			discoveryClient:     syncertest.NewDiscoveryClientWithError(context.DeadlineExceeded, fakeGVKs()...),
			expectedError:       fakeParseError(context.DeadlineExceeded, fakeGVKs()...),
			parsed:              append(fakeFileObjects(), k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy"))),
			expectedObjsToApply: nil,
		},
		{
			// unknown scoped object get skipped when sending to applier when discovery call is good
			name:            "unknown scoped object without discovery failure",
			discoveryClient: syncertest.NewDiscoveryClientWithError(nil, kinds.Namespace(), kinds.Role()),
			expectedError: status.UnknownObjectKindError(
				k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy"),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/obj.yaml"),
				)),
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
				// add a faked obect in parser.parsed without CRD so it's scope will be unknown when validating
				k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy")),
			},
			expectedObjsToApply: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
		{
			// happy path condition
			name:            "known scoped object without discovery failure",
			discoveryClient: syncertest.NewDiscoveryClient(kinds.Namespace(), kinds.Role()),
			expectedError:   nil,
			parsed: []ast.FileObject{
				k8sobjects.Role(core.Namespace("foo")),
				fakeCRD(core.Name("anvils.acme.com")),
				k8sobjects.Unstructured(kinds.Anvil(), core.Name("deploy"), core.Namespace("foo")),
			},
			expectedObjsToApply: []ast.FileObject{
				fakeCRD(core.Name("anvils.acme.com"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "cluster/crd.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "apiextensions.k8s.io_customresourcedefinition_anvils.acme.com"),
					difftest.ManagedBy(declared.RootScope, rootSyncName)),
				k8sobjects.Role(core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/foo/role.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_role_foo_default-name"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
				k8sobjects.Unstructured(kinds.Anvil(),
					core.Name("deploy"),
					core.Namespace("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.DeclaredVersionLabel, "v1"),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(metadata.ResourceManagerKey, ":root_my-rs"),
					core.Annotation(metadata.SourcePathAnnotationKey, "namespaces/obj.yaml"),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "acme.com_anvil_foo_deploy"),
				),
				k8sobjects.UnstructuredAtPath(kinds.Namespace(),
					"",
					core.Name("foo"),
					core.Label(metadata.ManagedByKey, metadata.ManagedByValue),
					core.Label(metadata.ApplySetPartOfLabel, applySetID),
					core.Annotation(common.LifecycleDeleteAnnotation, common.PreventDeletion),
					metadata.WithManagementMode(metadata.ManagementEnabled),
					core.Annotation(metadata.GitContextKey, nilGitContext),
					core.Annotation(metadata.SyncTokenAnnotationKey, ""),
					core.Annotation(metadata.OwningInventoryKey, applier.InventoryID(rootSyncName, configmanagement.ControllerNamespace)),
					core.Annotation(metadata.ResourceIDKey, "_namespace_foo"),
					difftest.ManagedBy(declared.RootScope, rootSyncName),
				),
			},
		},
	}

	converter, err := openapitest.ValueConverterForTest()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeConfigParser := &fsfake.ConfigParser{
				Outputs: []fsfake.ParserOutputs{
					// One Parse call, no errors
					{FileObjects: tc.parsed},
				},
			}
			fakeApplier := &applierfake.Applier{
				ApplyOutputs: []applierfake.ApplierOutputs{
					{}, // One Apply call
				},
			}
			state := &ReconcilerState{
				status: &ReconcilerStatus{},
				cache: cacheForCommit{
					source: &sourceState{},
				},
				syncErrorCache: NewSyncErrorCache(conflict.NewHandler(), fight.NewHandler()),
			}
			opts := &Options{
				Clock:             clock.RealClock{}, // TODO: Test with fake clock
				ConfigParser:      fakeConfigParser,
				SyncName:          rootSyncName,
				Scope:             declared.RootScope,
				ReconcilerName:    rootReconcilerName,
				Client:            syncertest.NewClient(t, core.Scheme, k8sobjects.RootSyncObjectV1Beta1(rootSyncName)),
				DiscoveryClient:   tc.discoveryClient,
				Converter:         converter,
				DeclaredResources: &declared.Resources{},
			}
			rootOpts := &RootOptions{
				Options:           opts,
				SourceFormat:      configsync.SourceFormatUnstructured,
				NamespaceStrategy: configsync.NamespaceStrategyImplicit,
			}
			recOpts := &ReconcilerOptions{
				Options: opts,
				Updater: &Updater{
					Scope:          opts.Scope,
					Resources:      opts.DeclaredResources,
					Remediator:     &remediatorfake.Remediator{},
					Applier:        fakeApplier,
					SyncErrorCache: state.syncErrorCache,
				},
				FullSyncPeriod:     configsync.DefaultReconcilerFullSyncPeriod,
				StatusUpdatePeriod: configsync.DefaultReconcilerSyncStatusUpdatePeriod,
				RenderingEnabled:   false,
			}
			reconciler := &reconciler{
				options: recOpts,
				syncStatusClient: &rootSyncStatusClient{
					options: opts,
				},
				parser: &rootSyncParser{
					options: rootOpts,
				},
				reconcilerState: state,
			}

			ctx := context.Background()
			trigger := triggerSync
			errs := reconciler.parse(ctx, trigger)
			testerrors.AssertEqual(t, tc.expectedError, errs, "expected parse errors to match")

			// After parsing, the objects set is processed and modified.
			// Validate that the result is stored in state.cache.objsToApply.
			testutil.AssertEqual(t, tc.expectedObjsToApply, state.cache.parse.objsToApply, "unexpected state.cache.objsToApply contents")
		})
	}
}

func TestRootReconciler_Parse_SourceErrorMetricValidation(t *testing.T) {
	testCases := []struct {
		name            string
		parseErrors     status.MultiError
		expectedError   status.MultiError
		expectedMetrics []testmetrics.MetricData
	}{
		{
			name: "single reconciler error in source component",
			parseErrors: status.Wrap(
				status.SourceError.Sprintf("source error").Build(),
			),
			expectedError: status.Wrap(
				status.SourceError.Sprintf("source error").Build(),
			),
			expectedMetrics: []testmetrics.MetricData{
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "source", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "9xxx"}},
				{Name: metrics.PipelineErrorName, Value: 1, Labels: map[string]string{"component": "source", "name": "", "reconciler": "root-sync"}},
			},
		},
		{
			name: "multiple reconciler errors in source component",
			parseErrors: status.Wrap(
				status.SourceError.Sprintf("source error").Build(),
				status.InternalError("internal error"),
			),
			expectedError: status.Wrap(
				status.SourceError.Sprintf("source error").Build(),
				status.InternalError("internal error"),
			),
			expectedMetrics: []testmetrics.MetricData{
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "source", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "source", "errorclass": "9xxx"}},
				{Name: metrics.PipelineErrorName, Value: 1, Labels: map[string]string{"component": "source", "name": "", "reconciler": "root-sync"}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.parseErrors)
			// Initialize metrics for this test
			exporter, err := testmetrics.NewTestExporter()
			if err != nil {
				t.Fatalf("Failed to create test exporter: %v", err)
			}
			defer exporter.ClearMetrics()
			fakeConfigParser := &fsfake.ConfigParser{
				Outputs: []fsfake.ParserOutputs{
					// One Parse call, with errors
					{Errors: tc.parseErrors},
				},
			}
			fakeApplier := &applierfake.Applier{
				ApplyOutputs: []applierfake.ApplierOutputs{
					{}, // One Apply call
				},
			}
			state := &ReconcilerState{
				status: &ReconcilerStatus{},
				cache: cacheForCommit{
					source: &sourceState{},
				},
				syncErrorCache: NewSyncErrorCache(conflict.NewHandler(), fight.NewHandler()),
			}
			opts := &Options{
				Clock:             clock.RealClock{}, // TODO: Test with fake clock
				ConfigParser:      fakeConfigParser,
				SyncName:          rootSyncName,
				Scope:             declared.RootScope,
				ReconcilerName:    rootReconcilerName,
				Client:            syncertest.NewClient(t, core.Scheme, k8sobjects.RootSyncObjectV1Beta1(rootSyncName)),
				DiscoveryClient:   syncertest.NewDiscoveryClient(kinds.Namespace(), kinds.Role()),
				DeclaredResources: &declared.Resources{},
			}
			rootOpts := &RootOptions{
				Options:      opts,
				SourceFormat: configsync.SourceFormatUnstructured,
			}
			recOpts := &ReconcilerOptions{
				Options: opts,
				Updater: &Updater{
					Scope:          opts.Scope,
					Resources:      opts.DeclaredResources,
					Remediator:     &remediatorfake.Remediator{},
					Applier:        fakeApplier,
					SyncErrorCache: state.syncErrorCache,
				},
				FullSyncPeriod:     configsync.DefaultReconcilerFullSyncPeriod,
				StatusUpdatePeriod: configsync.DefaultReconcilerSyncStatusUpdatePeriod,
				RenderingEnabled:   false,
			}
			reconciler := &reconciler{
				options: recOpts,
				syncStatusClient: &rootSyncStatusClient{
					options: opts,
				},
				parser: &rootSyncParser{
					options: rootOpts,
				},
				reconcilerState: state,
			}

			ctx := context.Background()
			trigger := triggerSync
			errs := reconciler.parse(ctx, trigger)
			testerrors.AssertEqual(t, tc.expectedError, errs, "expected parse errors to match")

			if diff := exporter.ValidateMetrics(tc.expectedMetrics); diff != "" {
				t.Error(diff)
			}
		})
	}
}

func TestRootReconciler_Update_ApplierErrorMetricValidation(t *testing.T) {
	testCases := []struct {
		name            string
		applyErrors     []status.Error
		expectedError   status.MultiError
		expectedMetrics []testmetrics.MetricData
	}{
		{
			name: "single reconciler error in sync component",
			applyErrors: []status.Error{
				applier.Error(errors.New("sync error")),
			},
			expectedError: applier.Error(errors.New("sync error")),
			expectedMetrics: []testmetrics.MetricData{
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "9xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "sync", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "sync", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "sync", "errorclass": "9xxx"}},
			},
		},
		{
			name: "multiple reconciler errors in sync component",
			applyErrors: []status.Error{
				applier.Error(errors.New("sync error")),
				status.InternalError("internal error"),
			},
			expectedError: status.Wrap(
				applier.Error(errors.New("sync error")),
				status.InternalError("internal error"),
			),
			expectedMetrics: []testmetrics.MetricData{
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "source", "errorclass": "9xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 0, Labels: map[string]string{"component": "sync", "errorclass": "1xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "sync", "errorclass": "2xxx"}},
				{Name: metrics.ReconcilerErrorsName, Value: 1, Labels: map[string]string{"component": "sync", "errorclass": "9xxx"}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Initialize metrics for this test
			exporter, err := testmetrics.NewTestExporter()
			if err != nil {
				t.Fatalf("Failed to create test exporter: %v", err)
			}
			defer exporter.ClearMetrics()
			fakeConfigParser := &fsfake.ConfigParser{
				Outputs: []fsfake.ParserOutputs{
					{}, // One Parse call, no errors
				},
			}
			fakeApplier := &applierfake.Applier{
				ApplyOutputs: []applierfake.ApplierOutputs{
					{Errors: tc.applyErrors}, // One Apply call, optional errors
				},
			}
			state := &ReconcilerState{
				status: &ReconcilerStatus{},
				cache: cacheForCommit{
					source: &sourceState{},
				},
				syncErrorCache: NewSyncErrorCache(conflict.NewHandler(), fight.NewHandler()),
			}
			opts := &Options{
				Clock:             clock.RealClock{}, // TODO: Test with fake clock
				ConfigParser:      fakeConfigParser,
				SyncName:          rootSyncName,
				Scope:             declared.RootScope,
				ReconcilerName:    rootReconcilerName,
				Client:            syncertest.NewClient(t, core.Scheme, k8sobjects.RootSyncObjectV1Beta1(rootSyncName)),
				DiscoveryClient:   syncertest.NewDiscoveryClient(kinds.Namespace(), kinds.Role()),
				DeclaredResources: &declared.Resources{},
			}
			rootOpts := &RootOptions{
				Options:      opts,
				SourceFormat: configsync.SourceFormatUnstructured,
			}
			recOpts := &ReconcilerOptions{
				Options: opts,
				Updater: &Updater{
					Scope:          opts.Scope,
					Resources:      opts.DeclaredResources,
					Remediator:     &remediatorfake.Remediator{},
					Applier:        fakeApplier,
					SyncErrorCache: state.syncErrorCache,
				},
				FullSyncPeriod:     configsync.DefaultReconcilerFullSyncPeriod,
				StatusUpdatePeriod: configsync.DefaultReconcilerSyncStatusUpdatePeriod,
				RenderingEnabled:   false,
			}
			reconciler := &reconciler{
				options: recOpts,
				syncStatusClient: &rootSyncStatusClient{
					options: opts,
				},
				parser: &rootSyncParser{
					options: rootOpts,
				},
				reconcilerState: state,
			}

			ctx := context.Background()
			trigger := triggerSync
			errs := reconciler.parse(ctx, trigger)
			require.NoError(t, errs, "expected parse not to error")
			errs = reconciler.update(ctx, trigger)
			testerrors.AssertEqual(t, tc.expectedError, errs, "expected update errors to match")

			if diff := exporter.ValidateMetrics(tc.expectedMetrics); diff != "" {
				t.Error(diff)
			}
		})
	}
}
