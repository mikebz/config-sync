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

package runner

import (
	"context"
	"flag"
	"fmt"

	"github.com/GoogleContainerTools/config-sync/pkg/api/kpt.dev/v1alpha1"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/log"
	ocmetrics "github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/metrics"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/profiler"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/resourcegroup"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/resourcemap"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/root"
	"github.com/GoogleContainerTools/config-sync/pkg/resourcegroup/controllers/typeresolver"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme = runtime.NewScheme()
)

// Run starts all controllers and returns an integer exit code.
func Run() int {
	if err := run(); err != nil {
		klog.Error(err, "exiting")
		return 1
	}
	return 0
}

func run() error {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = v1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme

	_ = apiextensionsv1.AddToScheme(scheme)

	log.InitFlags()

	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	profiler.Service()
	logger := textlogger.NewLogger(textlogger.NewConfig())
	ctx := context.Background()

	// Register the OTLP metrics exporter and metrics instruments
	oce, err := ocmetrics.RegisterOTelExporter(ctx, resourcegroup.ManagerContainerName)
	if err != nil {
		return fmt.Errorf("failed to register the OTLP metrics exporter: %w", err)
	}

	defer func() {
		if err := oce.Shutdown(ctx); err != nil {
			klog.Error(err, "Unable to stop the OTLP metrics exporter")
		}
	}()
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Logger: logger.WithName("controller-manager"),
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: 9443,
		}),
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "413d8c8e.gke.io",
	})
	if err != nil {
		return fmt.Errorf("failed to build manager: %w", err)
	}

	controllerLogger := logger.WithName("controllers")

	for _, group := range []string{root.KptGroup} {
		if err := registerControllersForGroup(ctx, mgr, controllerLogger, group); err != nil {
			return fmt.Errorf("failed to register controllers for group %s: %w", group, err)
		}
	}

	// +kubebuilder:scaffold:builder

	klog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("failed to start controller-manager: %w", err)
	}
	return nil
}

func registerControllersForGroup(ctx context.Context, mgr ctrl.Manager, logger logr.Logger, group string) error {
	// channel is watched by ResourceGroup controller.
	// The Root controller pushes events to it and
	// the ResourceGroup controller consumes events.
	channel := make(chan event.GenericEvent)

	klog.Info("adding the type resolver")
	resolver, err := typeresolver.ForManager(mgr, logger.WithName("TypeResolver"))
	if err != nil {
		return fmt.Errorf("unable to set up the type resolver: %w", err)
	}
	if err := resolver.Refresh(ctx); err != nil {
		return fmt.Errorf("unable to initialize the type resolver resource cache: %w", err)
	}

	klog.Info("adding the Root controller for group " + group)
	resMap := resourcemap.NewResourceMap()
	if err := root.NewController(mgr, channel, logger.WithName("Root"), resolver, group, resMap); err != nil {
		return fmt.Errorf("unable to create the root controller for group %s: %w", group, err)
	}

	klog.Info("adding the ResourceGroup controller for group " + group)
	if err := resourcegroup.NewRGController(mgr, channel, logger.WithName(v1alpha1.ResourceGroupKind), resolver, resMap, resourcegroup.DefaultDuration); err != nil {
		return fmt.Errorf("unable to create the ResourceGroup controller %s: %w", group, err)
	}
	return nil
}
