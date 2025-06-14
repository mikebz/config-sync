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

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"kpt.dev/configsync/cmd/nomos/util"
	"kpt.dev/configsync/pkg/api/configmanagement"
	"kpt.dev/configsync/pkg/client/restconfig"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
)

// executeVersion is the main entry point for the version command logic.
// It fetches versions and prints them to the outWriter.
func executeVersion(ctx context.Context, contexts []string, clientTimeout time.Duration, outWriter io.Writer) error {
	allCfgs, err := getAllKubectlConfigs(contexts, clientTimeout)
	// versionInternal handles the printing, including "No clusters match"
	versionInternal(ctx, allCfgs, outWriter, clientVersionFunc) // Pass clientVersionFunc

	if err != nil {
		// This error is about failing to load kubeconfig, not specific cluster errors.
		// The original code printed this and then proceeded. We'll return it.
		return fmt.Errorf("unable to parse kubectl config: %w", err)
	}
	return nil
}

// GetVersionReadCloser returns a ReadCloser with the output produced by running the "nomos version" command as a string.
// This is used by `nomos bugreport`.
func GetVersionReadCloser(ctx context.Context, contexts []string, clientTimeout time.Duration) (io.ReadCloser, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create os.Pipe: %w", err)
	}

	writer := util.NewWriter(w) // For tabular output
	allCfgs, cfgErr := getAllKubectlConfigs(contexts, clientTimeout)
	// Even if cfgErr is not nil, versionInternal can handle empty/partial configs and print errors.

	versionInternal(ctx, allCfgs, writer, clientVersionFunc) // Pass clientVersionFunc

	// Close the write-end of the pipe so the read-end gets EOF.
	if closeErr := w.Close(); closeErr != nil {
		// If closing the writer fails, the reader might hang or get partial data.
		// It's better to also close the reader and return the error.
		r.Close()
		return nil, fmt.Errorf("failed to close version pipe writer: %w", closeErr)
	}

	if cfgErr != nil {
		// If there was an error getting configs, we still return the output generated so far,
		// but also signal the error. The original GetVersionReadCloser didn't explicitly return this config error.
		// For bug reports, partial info might be better than none.
		// The caller can decide how to handle this.
		// For now, let's return the data and a wrapped error.
		// However, the original did `return nil, err` if allCfgs itself failed.
		// Let's stick to that: if allKubectlConfigs returns an error, it's a setup problem.
		r.Close() // Close the read end as well, as we are erroring out.
		return nil, fmt.Errorf("failed to get kubectl configs: %w", cfgErr)
	}


	return io.NopCloser(r), nil
}

// getAllKubectlConfigs gets all kubectl configs, with error handling.
// Moved from original version.go. Takes contexts and timeout as params now.
func getAllKubectlConfigs(contexts []string, clientTimeout time.Duration) (map[string]*rest.Config, error) {
	allCfgs, err := restconfig.AllKubectlConfigs(clientTimeout, contexts)
	if err != nil {
		// The original code printed the error here and then versionInternal also printed.
		// To avoid double printing, we return the error. The caller (executeVersion or GetVersionReadCloser)
		// can decide how to present it.
		// Example of error from original code:
		// var pathErr *os.PathError
		// if errors.As(err, &pathErr) {
		// 	err = pathErr // Unwrap for cleaner message
		// }
		// fmt.Printf("failed to create client configs: %v\n", err)
		return nil, err // Return the error to be handled by the caller
	}
	// allCfgs can be an empty map if no contexts match, this is not an error itself.
	return allCfgs, nil
}

// versionInternal allows stubbing out the config for tests.
// It now takes a clientVersionGetter function.
func versionInternal(ctx context.Context, configs map[string]*rest.Config, w io.Writer, clientVersionGetter func() string) {
	if len(configs) == 0 {
		// This check is also implicitly handled by `versions` returning nil or empty.
		// But explicit check is fine.
		fmt.Fprint(w, "No clusters match the specified context.\n")
		// Still print CLI version even if no clusters.
	}

	vs, monoRepoClusters := versions(ctx, configs)
	util.MonoRepoNotice(w, monoRepoClusters...) // This function is in nomos/util

	// Use the passed clientVersionGetter for CLI version
	es := entries(vs, currentContextName(), clientVersionGetter)
	tabulate(es, w)
}

func getImageVersion(deployment *v1.Deployment) (string, error) {
	var container corev1.Container
	found := false
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == util.ReconcilerManagerName { // util.ReconcilerManagerName is "reconciler-manager"
			container = c
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("container %s not found in deployment %s/%s", util.ReconcilerManagerName, deployment.Namespace, deployment.Name)
	}

	reconcilerManagerImage := strings.Split(container.Image, ":")
	if len(reconcilerManagerImage) <= 1 {
		return "", fmt.Errorf("failed to get valid image version from image string: %q for container %s", container.Image, container.Name)
	}
	return reconcilerManagerImage[1], nil
}

func lookupVersionAndMode(ctx context.Context, cfg *rest.Config) (string, *bool, string, error) {
	if cfg == nil { // Should not happen if called from versions() correctly
		return util.ErrorMsg, nil, "", errors.New("lookupVersionAndMode called with nil rest.Config")
	}

	cmClient, err := util.NewConfigManagementClient(cfg)
	if err != nil {
		return util.ErrorMsg, nil, "", fmt.Errorf("failed to create ConfigManagement client: %w", err)
	}

	// Check for OSS installation first
	cl, err := ctrl.New(cfg, ctrl.Options{})
	if err != nil {
		return util.ErrorMsg, nil, "", fmt.Errorf("failed to create controller-runtime client: %w", err)
	}
	ck, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return util.ErrorMsg, nil, "", fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	isOss, err := util.IsOssInstallation(ctx, cmClient, cl, ck)
	if err != nil {
		// If IsOssInstallation itself errors (e.g., can't list CRDs), it's an error state.
		return util.ErrorMsg, nil, "", fmt.Errorf("failed to determine OSS installation status: %w", err)
	}

	if isOss {
		// For OSS, get version from reconciler-manager deployment image
		reconcilerDeployment, depErr := ck.AppsV1().Deployments(configmanagement.ControllerNamespace).Get(ctx, util.ReconcilerManagerName, metav1.GetOptions{})
		if depErr != nil {
			if apierrors.IsNotFound(depErr) {
				return util.NotInstalledMsg, &isOss, util.ConfigSyncName, fmt.Errorf("%s deployment not found: %w", util.ReconcilerManagerName, depErr)
			}
			return util.ErrorMsg, &isOss, util.ConfigSyncName, fmt.Errorf("failed to get %s deployment: %w", util.ReconcilerManagerName, depErr)
		}
		imageVer, imgErr := getImageVersion(reconcilerDeployment)
		if imgErr != nil {
			return util.ErrorMsg, &isOss, util.ConfigSyncName, fmt.Errorf("failed to get image version from %s deployment: %w", util.ReconcilerManagerName, imgErr)
		}
		// For OSS, multi-repo is the only mode.
		isMulti := true
		return imageVer, &isMulti, util.ConfigSyncName, nil
	}

	// If not OSS, it's an operator-managed installation (ACM/Anthos Config Management)
	v, err := cmClient.Version(ctx)
	if err != nil {
		// If ConfigManagement CR not found, it might mean not installed or an error.
		if apierrors.IsNotFound(err) {
			return util.NotInstalledMsg, nil, util.ConfigManagementName, errors.New("ConfigManagement resource not found")
		}
		return util.ErrorMsg, nil, util.ConfigManagementName, fmt.Errorf("failed to get version from ConfigManagement CR: %w", err)
	}

	isMulti, err := cmClient.IsMultiRepo(ctx)
	if err != nil {
		// Error determining multi-repo status, but version was found.
		// Return version with error for multi-repo state.
		return v, nil, util.ConfigManagementName, fmt.Errorf("failed to determine multi-repo status: %w", err)
	}
	return v, isMulti, util.ConfigManagementName, nil
}

type vErr struct {
	version   string
	component string
	err       error
}

func versions(ctx context.Context, cfgs map[string]*rest.Config) (map[string]vErr, []string) {
	var monoRepoClusters []string
	if len(cfgs) == 0 {
		return nil, nil
	}
	vs := make(map[string]vErr, len(cfgs))
	var (
		m sync.Mutex
		g sync.WaitGroup
	)
	for n, c := range cfgs {
		g.Add(1)
		go func(n string, c *rest.Config) {
			defer g.Done()
			var ve vErr
			var isMulti *bool
			if c == nil { // Handle case where a specific config might be nil (e.g. error during its creation)
				ve.err = errors.New("invalid kubeconfig for this context")
				ve.version = util.ErrorMsg
			} else {
				ve.version, isMulti, ve.component, ve.err = lookupVersionAndMode(ctx, c)
			}

			if ve.err == nil && isMulti != nil && !*isMulti {
				monoRepoClusters = append(monoRepoClusters, n)
			}
			m.Lock()
			vs[n] = ve
			m.Unlock()
		}(n, c)
	}
	g.Wait()
	sort.Strings(monoRepoClusters) // Ensure consistent order for MonoRepoNotice
	return vs, monoRepoClusters
}

type entry struct {
	current   string
	name      string
	component string
	vErr
}

// currentContextName abstracts fetching current context name, useful for testing.
var currentContextName = restconfig.CurrentContextName

// entries produces a stable list of version reports.
// It now takes currentCtxNameFunc and clientVersionGetter.
func entries(vs map[string]vErr, currentCtxName string, clientVersionGetter func() string) []entry {
	var es []entry
	for n, v := range vs {
		curr := ""
		if n == currentCtxName {
			curr = "*"
		}
		es = append(es, entry{current: curr, name: n, component: v.component, vErr: v})
	}
	// Add the client version using the getter.
	es = append(es, entry{
		name:      "<nomos CLI>", // Keep consistent with original output for sorting/display
		component: "<nomos CLI>", // component can be same as name for CLI
		vErr:      vErr{version: clientVersionGetter(), err: nil}})

	sort.SliceStable(es, func(i, j int) bool {
		// Sort by name, ensuring "<nomos CLI>" comes appropriately, usually last or first.
		// The original code sorted by `name` which for CLI was empty string, typically first.
		// Here, name is "<nomos CLI>".
		if es[i].name == "<nomos CLI>" { return false } // Always sort CLI last
		if es[j].name == "<nomos CLI>" { return true }
		return es[i].name < es[j].name
	})
	return es
}

func tabulate(es []entry, out io.Writer) {
	format := "%s\t%s\t%s\t%s\n"
	w := util.NewWriter(out) // util.NewWriter creates a tabwriter.Writer
	defer func() {
		if err := w.Flush(); err != nil {
			// Original code used util.MustFprintf(os.Stderr, ...), which panics.
			// Better to print to os.Stderr directly or handle error.
			fmt.Fprintf(os.Stderr, "Error on flushing tabwriter: %v\n", err)
		}
	}()
	fmt.Fprintf(w, format, "CURRENT", "CLUSTER_CONTEXT_NAME", "COMPONENT", "VERSION")
	for _, e := range es {
		errMsg := ""
		if e.err != nil {
			errMsg = fmt.Sprintf("<error: %v>", e.err)
		}
		// Ensure consistent output even if version or component is empty due to error
		versionStr := e.version
		if errMsg != "" && versionStr == util.ErrorMsg { // If version is already "<error>", don't duplicate
			versionStr = errMsg
		} else if errMsg != "" {
			versionStr = fmt.Sprintf("%s %s", e.version, errMsg) // Append error to version if version is somewhat valid
		}

		componentName := e.component
		if componentName == "" && errMsg != "" { // If component is unknown due to error
			componentName = "N/A"
		}

		fmt.Fprintf(w, format, e.current, e.name, componentName, versionStr)
	}
}
