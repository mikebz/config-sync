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

package reconcile

import (
	"context"
	"testing"

	"github.com/GoogleContainerTools/config-sync/pkg/api/configsync"
	"github.com/GoogleContainerTools/config-sync/pkg/core"
	"github.com/GoogleContainerTools/config-sync/pkg/core/k8sobjects"
	"github.com/GoogleContainerTools/config-sync/pkg/declared"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/analyzer/validation/nonhierarchical"
	"github.com/GoogleContainerTools/config-sync/pkg/metadata"
	"github.com/GoogleContainerTools/config-sync/pkg/metrics"
	"github.com/GoogleContainerTools/config-sync/pkg/policycontroller"
	"github.com/GoogleContainerTools/config-sync/pkg/remediator/conflict"
	"github.com/GoogleContainerTools/config-sync/pkg/status"
	syncerclient "github.com/GoogleContainerTools/config-sync/pkg/syncer/client"
	"github.com/GoogleContainerTools/config-sync/pkg/syncer/syncertest"
	testingfake "github.com/GoogleContainerTools/config-sync/pkg/syncer/syncertest/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testerrors"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testmetrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRemediator_Reconcile(t *testing.T) {
	testCases := []struct {
		name string
		// version is Version (from GVK) of the object to try to remediate.
		version string
		// conflictHandler is the initial state of the conflict handler.
		// Nil defaults to conflict.NewHandler().
		conflictHandler conflict.Handler
		// declared is the state of the object as returned by the Parser.
		declared client.Object
		// existingConflict is true if the conflict handler has previously seen
		// a management conflict for the declared object.
		existingConflict bool
		// actual is the current state of the object on the cluster.
		actual client.Object
		// want is the desired final state of the object on the cluster after
		// reconciliation.
		want client.Object
		// wantError is the desired error resulting from calling Reconcile, if there
		// is one.
		wantError error
		// wantConflict should be true if the conflict handler reports a
		// management conflict for the declared object after remediation.
		wantConflict bool
	}{
		// Happy Paths.
		{
			name:    "create added object",
			version: "v1",
			declared: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName))),
			actual: nil,
			want: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
			),
			wantError: nil,
		},
		{
			name:    "update declared object",
			version: "v1",
			declared: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("new-label", "one")),
			actual: k8sobjects.ClusterRoleBindingObject(),
			want: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
				core.Label("new-label", "one"),
			),
			wantError: nil,
		},
		{
			name:     "delete removed object",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_clusterrolebinding_default-name")),
			want:      nil,
			wantError: nil,
		},
		{
			name:    "update declared object where the actual state has the ignore mutation annotation",
			version: "v1",
			declared: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled),
			actual: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.Annotation("foo", "bar")),
			want: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				core.UID("1"),
				core.ResourceVersion("2"),
				core.Generation(1),
				core.Annotation("foo", "bar"),
				core.Annotation(metadata.LifecycleMutationAnnotation, "")),
			wantError: nil,
		},
		{
			name:    "update a mutation-ignored object with a drifted spec",
			version: "v1",
			declared: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation),
			actual: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.Annotation("foo", "bar")),
			want: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.UID("1"),
				core.ResourceVersion("1"),
				core.Generation(1),
				core.Annotation("foo", "bar")),
			wantError: nil,
		},
		{
			name:    "update a mutation-ignored object with both spec and CS metadata drift",
			version: "v1",
			declared: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.Annotation("foo", "baz"),
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
			),
			actual: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.Annotation("foo", "bar")),
			want: k8sobjects.NamespaceObject("test-namespace",
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				syncertest.ManagementEnabled,
				syncertest.IgnoreMutationAnnotation,
				core.UID("1"),
				core.ResourceVersion("2"),
				core.Generation(1),
				core.Annotation("foo", "bar")),
			wantError: nil,
		},
		{
			name:    "update an object with only CS metadata drift",
			version: "v1",
			declared: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				core.Annotation("foo", "baz"),
				core.Annotation(metadata.GitContextKey, "foo"),
			),
			actual: k8sobjects.NamespaceObject("test-namespace",
				syncertest.ManagementEnabled,
				core.Annotation("foo", "bar"),
				core.Annotation(metadata.GitContextKey, "bar")),
			want: k8sobjects.NamespaceObject("test-namespace",
				core.Annotation(metadata.GitContextKey, "foo"),
				syncertest.ManagementEnabled,
				core.UID("1"),
				core.ResourceVersion("2"),
				core.Generation(1),
				core.Annotation("foo", "baz")),
			wantError: nil,
		},
		// Unmanaged paths.
		{
			name:    "don't create unmanaged object",
			version: "v1",
			declared: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementDisabled,
				core.Label("declared-label", "foo")),
			actual:    nil,
			want:      nil,
			wantError: nil,
		},
		{
			name:    "don't update unmanaged object",
			version: "v1",
			declared: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementDisabled,
				core.Label("declared-label", "foo")),
			actual: k8sobjects.ClusterRoleBindingObject(core.Label("actual-label", "bar")),
			want: k8sobjects.ClusterRoleBindingObject(core.Label("actual-label", "bar"),
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
			),
			wantError: nil,
		},
		{
			name:     "don't delete unmanaged object",
			version:  "v1",
			declared: nil,
			actual:   k8sobjects.ClusterRoleBindingObject(),
			want: k8sobjects.ClusterRoleBindingObject(
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
			),
			wantError: nil,
		},
		{
			name:     "don't delete unmanaged object (the configsync.gke.io/resource-id annotation is incorrect)",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_clusterrolebinding_wrong-name")),
			want: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
				core.Annotation(metadata.ResourceIDKey, "rbac.authorization.k8s.io_clusterrolebinding_wrong-name")),
			wantError: nil,
		},
		// Bad declared management annotation paths.
		{
			name:     "don't create, and error on bad declared management annotation",
			version:  "v1",
			declared: k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid),
			actual:   nil,
			want:     nil,
			wantError: nonhierarchical.IllegalManagementAnnotationError(
				k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid),
				"invalid"),
		},
		{
			name:     "don't update, and error on bad declared management annotation",
			version:  "v1",
			declared: k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid),
			actual:   k8sobjects.ClusterRoleBindingObject(core.Label("actual-label", "bar")),
			want: k8sobjects.ClusterRoleBindingObject(core.Label("actual-label", "bar"),
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
			),
			wantError: nonhierarchical.IllegalManagementAnnotationError(
				k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid),
				"invalid"),
		},
		// bad in-cluster management annotation paths.
		{
			name:    "remove bad actual management annotation",
			version: "v1",
			declared: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("declared-label", "foo")),
			actual: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementInvalid,
				core.Label("declared-label", "foo")),
			want: k8sobjects.ClusterRoleBindingObject(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
				core.Label("declared-label", "foo")),
			wantError: nil,
		},
		{
			name:     "don't update non-Config-Sync-managed-objects with invalid management annotation",
			version:  "v1",
			declared: nil,
			actual:   k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid),
			want: k8sobjects.ClusterRoleBindingObject(core.Label("declared-label", "foo"), syncertest.ManagementInvalid,
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1),
			),
			wantError: nil,
		},
		// system namespaces
		{
			name:     "don't delete kube-system Namespace",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.NamespaceObject(metav1.NamespaceSystem, syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "_namespace_kube-system")),
			want: k8sobjects.NamespaceObject(metav1.NamespaceSystem,
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
			),
		},
		{
			name:     "don't delete kube-public Namespace",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.NamespaceObject(metav1.NamespacePublic, syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "_namespace_kube-public")),
			want: k8sobjects.NamespaceObject(metav1.NamespacePublic,
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
			),
		},
		{
			name:     "don't delete default Namespace",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.NamespaceObject(metav1.NamespaceDefault, syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "_namespace_default")),
			want: k8sobjects.NamespaceObject(metav1.NamespaceDefault,
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
			),
		},
		{
			name:     "don't delete gatekeeper-system Namespace",
			version:  "v1",
			declared: nil,
			actual: k8sobjects.NamespaceObject(policycontroller.NamespaceSystem, syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceIDKey, "_namespace_gatekeeper-system")),
			want: k8sobjects.NamespaceObject(policycontroller.NamespaceSystem,
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
			),
		},
		// Version difference paths.
		{
			name: "update actual object with different version",
			declared: k8sobjects.ClusterRoleBindingV1Beta1Object(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("new-label", "one")),
			actual: k8sobjects.ClusterRoleBindingObject(),
			// Metadata change increments ResourceVersion, but not Generation
			want: k8sobjects.ClusterRoleBindingV1Beta1Object(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1),
				core.Label("new-label", "one")),
			wantError: nil,
		},
		{
			// Normally, the filtered watcher handles detecting and reporting conflicts.
			// But with watch filtering, when the selected label is removed,
			// the object has to be retrieved from the cluster to check if it was deleted or just updated.
			// If updated, or deleted and recreated, the management may conflict.
			name: "management conflict error from selected label removal",
			declared: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("selected-label", "expected-value")),
			actual: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
				core.Label("selected-label", "unexpected-value"),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
			// No change made when a conflict is detected
			want: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
				core.Label("selected-label", "unexpected-value"),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
			wantError: status.ManagementConflictErrorWrap(
				k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
					core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
					core.Label("selected-label", "unexpected-value"),
					core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
				declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
			wantConflict: true,
		},
		{
			name: "management conflict resolved",
			// Setup pre-existing conflict error
			conflictHandler: func() conflict.Handler {
				h := conflict.NewHandler()
				obj := k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
					core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
					core.Label("selected-label", "unexpected-value"),
					core.Label("unselected-label", "unexpected-value"),
					core.UID("1"), core.ResourceVersion("2"), core.Generation(1))
				h.AddConflictError(core.IDOf(obj), status.ManagementConflictErrorWrap(obj,
					declared.ResourceManager(declared.RootScope, configsync.RootSyncName)))
				return h
			}(),
			declared: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("selected-label", "expected-value"),
				core.Label("unselected-label", "expected-value")),
			// Find unselected label drift, but correct manager
			actual: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("selected-label", "expected-value"),
				core.Label("unselected-label", "unexpected-value"),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
			// Revert unselected label drift
			want: k8sobjects.ClusterRoleBinding(syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("selected-label", "expected-value"),
				core.Label("unselected-label", "expected-value"),
				core.UID("1"), core.ResourceVersion("3"), core.Generation(1)),
			wantError: nil,
			// Resolve conflict
			wantConflict: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			exporter, err := testmetrics.NewTestExporter()
			if err != nil {
				t.Fatalf("Failed to create test exporter: %v", err)
			}
			defer exporter.ClearMetrics()
			// Set up the fake client that represents the initial state of the cluster.
			var existingObjs []client.Object
			if tc.actual != nil {
				existingObjs = append(existingObjs, tc.actual)
			}
			c := testingfake.NewClient(t, core.Scheme, existingObjs...)
			// Simulate the Parser having already parsed the resource and recorded it.
			d := makeDeclared(t, "unused", tc.declared)

			if tc.conflictHandler == nil {
				tc.conflictHandler = conflict.NewHandler()
			}

			r := newReconciler(declared.RootScope, configsync.RootSyncName, c.Applier(configsync.FieldManager), d,
				tc.conflictHandler, testingfake.NewFightHandler())

			// Get the triggering object for the reconcile event.
			var obj client.Object
			switch {
			case tc.declared != nil:
				obj = tc.declared
			case tc.actual != nil:
				obj = tc.actual
			default:
				t.Fatal("at least one of actual or declared must be specified for a test")
			}

			err = r.Remediate(context.Background(), core.IDOf(obj), tc.actual)
			testerrors.AssertEqual(t, tc.wantError, err)

			if tc.declared != nil {
				assert.Equal(t, tc.wantConflict, tc.conflictHandler.HasConflictError(core.IDOf(tc.declared)))
			}

			if tc.want == nil {
				c.Check(t)
			} else {
				c.Check(t, tc.want)
			}
		})
	}
}

func TestRemediator_Reconcile_Metrics(t *testing.T) {
	testCases := []struct {
		name string
		// version is Version (from GVK) of the object to try to remediate.
		version string
		// declared is the state of the object as returned by the Parser.
		declared client.Object
		// actual is the current state of the object on the cluster.
		actual                                client.Object
		createError, updateError, deleteError status.Error
		// want is the expected final state of the object on the cluster after
		// reconciliation.
		want client.Object
		// wantError is the expected error resulting from calling Reconcile
		wantError error
		// wantMetrics is the expected metrics resulting from calling Reconcile
		wantMetrics []testmetrics.MetricData
	}{
		{
			name: "ConflictUpdateDoesNotExist",
			// Object declared with label
			declared: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				syncertest.ManagementEnabled,
				core.Label("new-label", "one")),
			// Object on cluster has no label and is unmanaged
			actual: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example")),
			// Object update fails, because it was deleted by another client
			updateError: syncerclient.ConflictUpdateObjectDoesNotExist(
				apierrors.NewNotFound(schema.GroupResource{Group: "rbac", Resource: "roles"}, "example"),
				k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"))),
			// Object NOT updated on cluster, because update failed with conflict error
			want: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				core.UID("1"), core.ResourceVersion("1"), core.Generation(1)),
			// Expect update error returned from Remediate
			wantError: syncerclient.ConflictUpdateObjectDoesNotExist(
				apierrors.NewNotFound(schema.GroupResource{Group: "rbac", Resource: "roles"}, "example"),
				k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"))),
			// Expect resource conflict error
			wantMetrics: []testmetrics.MetricData{
				{
					Name:   metrics.ResourceConflictsName,
					Value:  1,
					Labels: map[string]string{"commit": "abc123"},
				},
			},
		},
		{
			name: "ConflictCreateAlreadyExists",
			// Object declared with label
			declared: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				syncertest.ManagementEnabled,
				core.Label("new-label", "one")),
			// Object on cluster does not exist yet
			actual: nil,
			// Object create fails, because it was already created by another client
			createError: syncerclient.ConflictCreateAlreadyExists(
				apierrors.NewNotFound(schema.GroupResource{Group: "rbac", Resource: "roles"}, "example"),
				k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"))),
			// Object NOT created on cluster, because update failed with conflict error
			want: nil,
			// Expect create error returned from Remediate
			wantError: syncerclient.ConflictCreateAlreadyExists(
				apierrors.NewNotFound(schema.GroupResource{Group: "rbac", Resource: "roles"}, "example"),
				k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"))),
			// Expect resource conflict error
			wantMetrics: []testmetrics.MetricData{
				{
					Name:   metrics.ResourceConflictsName,
					Value:  1,
					Labels: map[string]string{"commit": "abc123"},
				},
			},
		},
		// ConflictUpdateOldVersion will never be reported by the remediator,
		// because it uses server-side apply.
		{
			name: "ManagementConflict",
			declared: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
				core.Label("expected-label", "expected-value")),
			// Object on cluster has label drift and different manager
			actual: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
				core.Label("expected-label", "unexpected-value"),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
			// No change on server because of conflict
			want: k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
				syncertest.ManagementEnabled,
				core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
				core.Label("expected-label", "unexpected-value"),
				core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
			// Expect resource conflict error
			wantError: status.ManagementConflictErrorWrap(
				k8sobjects.RoleObject(core.Namespace("example"), core.Name("example"),
					syncertest.ManagementEnabled,
					core.Annotation(metadata.ResourceManagerKey, declared.ResourceManager(declared.RootScope, "other-root-sync")),
					core.Label("selected-label", "unexpected-value"),
					core.UID("1"), core.ResourceVersion("2"), core.Generation(1)),
				declared.ResourceManager(declared.RootScope, configsync.RootSyncName)),
			// Expect resource conflict metric
			wantMetrics: []testmetrics.MetricData{
				{
					Name:   metrics.ResourceConflictsName,
					Value:  1,
					Labels: map[string]string{"commit": "abc123"},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset metrics for each test case to avoid cross-test contamination
			exporter, err := testmetrics.NewTestExporter()
			require.NoError(t, err)
			// Set up the fake client that represents the initial state of the cluster.
			var existingObjs []client.Object
			if tc.actual != nil {
				existingObjs = append(existingObjs, tc.actual)
			}
			fakeClient := testingfake.NewClient(t, core.Scheme, existingObjs...)
			// Simulate the Parser having already parsed the resource and recorded it.
			d := makeDeclared(t, "abc123", tc.declared)

			fakeApplier := &testingfake.Applier{Client: fakeClient, FieldManager: configsync.FieldManager}
			fakeApplier.CreateError = tc.createError
			fakeApplier.UpdateError = tc.updateError
			fakeApplier.DeleteError = tc.deleteError

			reconciler := newReconciler(declared.RootScope, configsync.RootSyncName, fakeApplier, d,
				testingfake.NewConflictHandler(), testingfake.NewFightHandler())

			// Get the triggering object for the reconcile event.
			var obj client.Object
			switch {
			case tc.declared != nil:
				obj = tc.declared
			case tc.actual != nil:
				obj = tc.actual
			default:
				t.Fatal("at least one of actual or declared must be specified for a test")
			}

			err = reconciler.Remediate(context.Background(), core.IDOf(obj), tc.actual)
			testerrors.AssertEqual(t, tc.wantError, err)

			if tc.want == nil {
				fakeClient.Check(t)
			} else {
				fakeClient.Check(t, tc.want)
			}

			if diff := exporter.ValidateMetrics(tc.wantMetrics); diff != "" {
				t.Errorf("Unexpected metrics recorded: %v", diff)
			}
		})
	}
}

func makeDeclared(t *testing.T, commit string, objs ...client.Object) *declared.Resources {
	t.Helper()
	d := &declared.Resources{}
	if _, err := d.UpdateDeclared(context.Background(), objs, commit); err != nil {
		// Test precondition; fail early.
		t.Fatal(err)
	}
	return d
}
