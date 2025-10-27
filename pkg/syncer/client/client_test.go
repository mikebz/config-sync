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

package client_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GoogleContainerTools/config-sync/pkg/core"
	"github.com/GoogleContainerTools/config-sync/pkg/core/k8sobjects"
	"github.com/GoogleContainerTools/config-sync/pkg/kinds"
	"github.com/GoogleContainerTools/config-sync/pkg/status"
	syncerclient "github.com/GoogleContainerTools/config-sync/pkg/syncer/client"
	syncertestfake "github.com/GoogleContainerTools/config-sync/pkg/syncer/syncertest/fake"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testerrors"
	"github.com/GoogleContainerTools/config-sync/pkg/testing/testmetrics"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestClient_Create(t *testing.T) {
	testCases := []struct {
		name     string
		declared client.Object
		client   client.Client
		wantErr  status.Error
	}{
		{
			name:     "Creates if object does not exist",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewClient(t, core.Scheme),
			wantErr:  nil,
		},
		{
			name:     "Conflict error if resource IsNoMatchError",
			declared: k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")}),
			wantErr: syncerclient.ConflictCreateResourceDoesNotExist(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")},
				k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Conflict error if resource IsNotFound",
			declared: k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				apierrors.NewNotFound(kinds.Anvil().GroupVersion().WithResource("anvils").GroupResource(), "admin")),
			wantErr: syncerclient.ConflictCreateResourceDoesNotExist(
				apierrors.NewNotFound(kinds.Anvil().GroupVersion().WithResource("anvils").GroupResource(), "admin"),
				k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Retriable if receives AlreadyExists",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewClient(t, core.Scheme,
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			),
			wantErr: syncerclient.ConflictCreateAlreadyExists(
				apierrors.NewAlreadyExists(rbacv1.Resource("roles"), "billing/admin"),
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Generic APIServerError if other error",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewErrorClient(errors.New("some API server error")),
			wantErr: status.APIServerError(errors.New("some API server error"), "failed to create object",
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
	}

	for _, tc := range testCases {
		exporter, err := testmetrics.NewTestExporter()
		if err != nil {
			t.Fatalf("Failed to create test exporter: %v", err)
		}
		defer exporter.ClearMetrics()

		t.Run(tc.name, func(t *testing.T) {
			sc := syncerclient.New(tc.client, nil)

			err := sc.Create(context.Background(), tc.declared, client.FieldOwner(syncertestfake.FieldManager))
			testerrors.AssertEqual(t, tc.wantErr, err)
		})
	}
}

func TestClient_Apply(t *testing.T) {
	testCases := []struct {
		name     string
		declared client.Object
		client   client.Client
		wantErr  status.Error
	}{
		{
			name:     "Conflict error if object not found",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				apierrors.NewNotFound(rbacv1.Resource("Role"), "billing/admin")),
			wantErr: syncerclient.ConflictUpdateObjectDoesNotExist(
				apierrors.NewNotFound(rbacv1.Resource("Role"), "billing/admin"),
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Conflict error if resource not found",
			declared: k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")}),
			wantErr: syncerclient.ConflictUpdateResourceDoesNotExist(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")},
				k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Generic error if other error",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewErrorClient(errors.New("some error")),
			wantErr: status.ResourceWrap(errors.New("some error"),
				"failed to get object to update",
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "No error if client does not return error",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewErrorClient(nil),
			wantErr:  nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc := syncerclient.New(tc.client, nil)

			_, err := sc.Apply(context.Background(), tc.declared, noOpUpdate)
			testerrors.AssertEqual(t, tc.wantErr, err)
		})
	}
}

func noOpUpdate(o client.Object) (object client.Object, err error) { return o, nil }

func TestClient_Update(t *testing.T) {
	testCases := []struct {
		name     string
		declared client.Object
		client   client.Client
		wantErr  status.Error
	}{
		{
			name:     "Conflict error if object not found",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				apierrors.NewNotFound(rbacv1.Resource("Role"), "admin")),
			wantErr: syncerclient.ConflictUpdateObjectDoesNotExist(
				apierrors.NewNotFound(rbacv1.Resource("Role"), "admin"),
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Conflict error if resource not found",
			declared: k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing")),
			client: syncertestfake.NewErrorClient(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")}),
			wantErr: syncerclient.ConflictUpdateResourceDoesNotExist(
				&meta.NoResourceMatchError{PartialResource: kinds.Anvil().GroupVersion().WithResource("anvils")},
				k8sobjects.UnstructuredObject(kinds.Anvil(), core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "Generic error if other error",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewErrorClient(errors.New("some error")),
			wantErr: status.ResourceWrap(errors.New("some error"), "failed to update object",
				k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing"))),
		},
		{
			name:     "No error if client does not return error",
			declared: k8sobjects.RoleObject(core.Name("admin"), core.Namespace("billing")),
			client:   syncertestfake.NewErrorClient(nil),
			wantErr:  nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc := syncerclient.New(tc.client, nil)

			err := sc.Update(context.Background(), tc.declared, client.FieldOwner(syncertestfake.FieldManager))
			testerrors.AssertEqual(t, tc.wantErr, err)
		})
	}
}
