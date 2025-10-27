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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/GoogleContainerTools/config-sync/pkg/api/configsync"
	"github.com/GoogleContainerTools/config-sync/pkg/declared"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/filesystem"
	"github.com/GoogleContainerTools/config-sync/pkg/importer/filesystem/cmpath"
	"github.com/GoogleContainerTools/config-sync/pkg/metadata"
	ocmetrics "github.com/GoogleContainerTools/config-sync/pkg/metrics"
	"github.com/GoogleContainerTools/config-sync/pkg/profiler"
	"github.com/GoogleContainerTools/config-sync/pkg/reconciler"
	"github.com/GoogleContainerTools/config-sync/pkg/reconcilermanager"
	"github.com/GoogleContainerTools/config-sync/pkg/reconcilermanager/controllers"
	"github.com/GoogleContainerTools/config-sync/pkg/status"
	"github.com/GoogleContainerTools/config-sync/pkg/util"
	"github.com/GoogleContainerTools/config-sync/pkg/util/log"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	clusterName = flag.String(flags.clusterName, os.Getenv(reconcilermanager.ClusterNameKey),
		"Cluster name to use for Cluster selection")
	scopeStr = flag.String("scope", os.Getenv(reconcilermanager.ScopeKey),
		"Scope of the reconciler, either a namespace or ':root'.")
	syncName = flag.String("sync-name", os.Getenv(reconcilermanager.SyncNameKey),
		"Name of the RootSync or RepoSync object.")
	reconcilerName = flag.String("reconciler-name", os.Getenv(reconcilermanager.ReconcilerNameKey),
		"Name of the reconciler Deployment.")

	// source configuration flags. These values originate in the ConfigManagement and
	// configure git-sync/oci-sync to clone the desired repository/reference we want.
	sourceType = flag.String("source-type", os.Getenv(reconcilermanager.SourceTypeKey),
		"The type of repo being synced, must be git or oci or helm.")
	sourceRepo = flag.String("source-repo", os.Getenv(reconcilermanager.SourceRepoKey),
		"The URL of the git or OCI repo being synced.")
	sourceBranch = flag.String("source-branch", os.Getenv(reconcilermanager.SourceBranchKey),
		"The branch of the git repo being synced.")
	sourceRev = flag.String("source-rev", os.Getenv(reconcilermanager.SourceRevKey),
		"The reference we're syncing to in the repo. Could be a specific commit or a chart version.")
	syncDir = flag.String("sync-dir", os.Getenv(reconcilermanager.SyncDirKey),
		"The relative path of the root configuration directory within the repo.")

	// Performance tuning flags.
	sourceDir = flag.String(flags.sourceDir, "/repo/source/rev",
		"The absolute path in the container running the reconciler to the clone of the source repo.")
	repoRootDir = flag.String(flags.repoRootDir, "/repo",
		"The absolute path in the container running the reconciler to the repo root directory.")
	hydratedRootDir = flag.String(flags.hydratedRootDir, "/repo/hydrated",
		"The absolute path in the container running the reconciler to the hydrated root directory.")
	hydratedLinkDir = flag.String("hydrated-link", "rev",
		"The name of (a symlink to) the source directory under --hydrated-root, which contains the hydrated configs")
	fightDetectionThreshold = flag.Float64(
		"fight-detection-threshold", configsync.DefaultFightThreshold,
		"The rate of updates per minute to an API Resource at which the Syncer logs warnings about too many updates to the resource.")
	fullSyncPeriod = flag.Duration("full-sync-period", configsync.DefaultReconcilerFullSyncPeriod,
		"Period of time between forced re-syncs from source (even without a new commit).")
	workers = flag.Int("workers", 1,
		"Number of concurrent remediator workers to run at once.")
	pollingPeriod = flag.Duration("filesystem-polling-period",
		controllers.PollingPeriod(reconcilermanager.ReconcilerPollingPeriod, configsync.DefaultReconcilerPollingPeriod),
		"Period of time between checking the filesystem for source updates to sync.")

	// Root-Repo-only flags. If set for a Namespace-scoped Reconciler, causes the Reconciler to fail immediately.
	sourceFormat = flag.String(flags.sourceFormat, os.Getenv(filesystem.SourceFormatKey),
		"The format of the repository.")
	// Applier flag, Make the reconcile/prune timeout configurable
	reconcileTimeout = flag.String(flags.reconcileTimeout, os.Getenv(reconcilermanager.ReconcileTimeout), "The timeout of applier reconcile and prune tasks")
	// Enable the applier to inject actuation status data into the ResourceGroup object
	statusMode = flag.String(flags.statusMode, os.Getenv(reconcilermanager.StatusMode),
		"When the value is enabled or empty, the applier injects actuation status data into the ResourceGroup object")

	apiServerTimeout = flag.String("api-server-timeout", os.Getenv(reconcilermanager.APIServerTimeout), "The client-side timeout for requests to the API server")

	debug = flag.Bool("debug", false,
		"Enable debug mode, panicking in many scenarios where normally an InternalError would be logged. "+
			"Do not use in production.")

	renderingEnabled  = flag.Bool("rendering-enabled", util.EnvBool(reconcilermanager.RenderingEnabled, false), "")
	namespaceStrategy = flag.String(flags.namespaceStrategy, util.EnvString(reconcilermanager.NamespaceStrategy, ""),
		fmt.Sprintf("Set the namespace strategy for the reconciler. Must be %s or %s. Default: %s.",
			configsync.NamespaceStrategyImplicit, configsync.NamespaceStrategyExplicit, configsync.NamespaceStrategyImplicit))

	dynamicNSSelectorEnabled = flag.Bool("dynamic-ns-selector-enabled", util.EnvBool(reconcilermanager.DynamicNSSelectorEnabled, false), "")

	webhookEnabled       = flag.Bool("webhook-enabled", util.EnvBool(reconcilermanager.WebhookEnabled, false), "")
	reconcilerSignalsDir = flag.String(flags.reconcilerSignalDir, "/reconciler-signals",
		"The absolute path in the container that contains reconciler signals that unblock the rendering phase, for example, the latest image digest that is ready to render.")
)

var flags = struct {
	sourceDir           string
	repoRootDir         string
	hydratedRootDir     string
	reconcilerSignalDir string
	clusterName         string
	sourceFormat        string
	statusMode          string
	reconcileTimeout    string
	namespaceStrategy   string
}{
	repoRootDir:         "repo-root",
	sourceDir:           "source-dir",
	hydratedRootDir:     "hydrated-root",
	reconcilerSignalDir: "reconciler-signals",
	clusterName:         "cluster-name",
	sourceFormat:        reconcilermanager.SourceFormat,
	statusMode:          "status-mode",
	reconcileTimeout:    "reconcile-timeout",
	namespaceStrategy:   "namespace-strategy",
}

func main() {
	log.Setup()
	profiler.Service()
	logger := textlogger.NewLogger(textlogger.NewConfig())
	ctrl.SetLogger(logger)

	if *debug {
		status.EnablePanicOnMisuse()
	}

	// Register the OTLP metrics exporter and metrics instruments
	ctx := context.Background()
	oce, err := ocmetrics.RegisterOTelExporter(ctx, reconcilermanager.Reconciler)
	if err != nil {
		klog.Fatalf("Failed to register the OTLP metrics exporter: %v", err)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), ocmetrics.ShutdownTimeout)
		defer cancel()
		if err := oce.Shutdown(shutdownCtx); err != nil {
			klog.Fatalf("Unable to stop the OTLP metrics exporter: %v", err)
		}
	}()

	absRepoRoot, err := cmpath.AbsoluteOS(*repoRootDir)
	if err != nil {
		klog.Fatalf("%s must be an absolute path: %v", flags.repoRootDir, err)
	}

	absReconcilerSignalDir, err := cmpath.AbsoluteOS(*reconcilerSignalsDir)
	if err != nil {
		klog.Fatalf("%s must be an absolute path: %v", flags.reconcilerSignalDir, err)
	}

	// Normalize syncDirRelative.
	// Some users specify the directory as if the root of the repository is "/".
	// Strip this from the front of the passed directory so behavior is as
	// expected.
	dir := strings.TrimPrefix(*syncDir, "/")
	relSyncDir := cmpath.RelativeOS(dir)
	absSourceDir, err := cmpath.AbsoluteOS(*sourceDir)
	if err != nil {
		klog.Fatalf("%s must be an absolute path: %v", flags.sourceDir, err)
	}

	scope := declared.Scope(*scopeStr)
	err = scope.Validate()
	if err != nil {
		klog.Fatal(err)
	}

	if err := validateStatusMode(*statusMode); err != nil {
		klog.Fatal(err)
	}

	opts := reconciler.Options{
		Logger:                   logger,
		ClusterName:              *clusterName,
		FightDetectionThreshold:  *fightDetectionThreshold,
		NumWorkers:               *workers,
		ReconcilerScope:          scope,
		FullSyncPeriod:           *fullSyncPeriod,
		PollingPeriod:            *pollingPeriod,
		RetryPeriod:              configsync.DefaultReconcilerRetryPeriod,
		StatusUpdatePeriod:       configsync.DefaultReconcilerSyncStatusUpdatePeriod,
		SourceRoot:               absSourceDir,
		RepoRoot:                 absRepoRoot,
		HydratedRoot:             *hydratedRootDir,
		HydratedLink:             *hydratedLinkDir,
		SourceRev:                *sourceRev,
		SourceBranch:             *sourceBranch,
		SourceType:               configsync.SourceType(*sourceType),
		SourceRepo:               *sourceRepo,
		SyncDir:                  relSyncDir,
		SyncName:                 *syncName,
		ReconcilerName:           *reconcilerName,
		StatusMode:               metadata.StatusMode(*statusMode),
		ReconcileTimeout:         *reconcileTimeout,
		APIServerTimeout:         *apiServerTimeout,
		RenderingEnabled:         *renderingEnabled,
		DynamicNSSelectorEnabled: *dynamicNSSelectorEnabled,
		WebhookEnabled:           *webhookEnabled,
		ReconcilerSignalsDir:     absReconcilerSignalDir,
	}

	if scope == declared.RootScope {
		// Default to "hierarchy" if unset.
		format := configsync.SourceFormat(*sourceFormat)
		if format == "" {
			format = configsync.SourceFormatHierarchy
		}
		// Default to "implicit" if unset.
		nsStrat := configsync.NamespaceStrategy(*namespaceStrategy)
		if nsStrat == "" {
			nsStrat = configsync.NamespaceStrategyImplicit
		}

		klog.Info("Starting reconciler for: root")
		opts.RootOptions = &reconciler.RootOptions{
			SourceFormat:      format,
			NamespaceStrategy: nsStrat,
		}
	} else {
		klog.Infof("Starting reconciler for: %s", scope)

		if *sourceFormat != "" {
			klog.Fatalf("Flag %s and environment variable %s must not be passed to a Namespace reconciler",
				flags.sourceFormat, filesystem.SourceFormatKey)
		}
		if *namespaceStrategy != "" {
			klog.Fatalf("Flag %s and environment variable %s must not be passed to a Namespace reconciler",
				flags.namespaceStrategy, reconcilermanager.NamespaceStrategy)
		}
	}
	reconciler.Run(opts)
}

// validateStatusMode validates the --status-mode flag option value.
func validateStatusMode(statusMode string) error {
	switch statusMode {
	case metadata.StatusEnabled.String(),
		metadata.StatusDisabled.String(),
		"": // unspecified or empty
		return nil
	default:
		return fmt.Errorf("invalid %s %q: must be %s or %s",
			flags.statusMode, statusMode, metadata.StatusEnabled, metadata.StatusDisabled)
	}
}
