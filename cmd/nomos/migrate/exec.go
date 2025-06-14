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
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	nomosstatus "kpt.dev/configsync/cmd/nomos/status" // aliased to avoid conflict with local status
	"kpt.dev/configsync/cmd/nomos/util"
	"kpt.dev/configsync/pkg/api/configmanagement"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/api/configsync/v1beta1"
	"kpt.dev/configsync/pkg/kinds" // Added for kinds.XYZ()
	"kpt.dev/configsync/pkg/reconcilermanager/controllers"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	monorepoMigrateDir         = "nomos-migrate-monorepo"
	configManagementMigrateDir = "nomos-migrate-configmanagement"
	rootSyncYamlFile           = "root-sync.yaml"
	cmOrigYAMLFile             = "cm-original.yaml"
	cmMultiYAMLFile            = "cm-multi.yaml"
	cmOperatorYAMLFile         = "config-management-operator.yaml"

	updatingConfigManagement    = "Updating the ConfigManagement object ..."
	waitingForConfigSyncCRDs    = "Waiting for ConfigSync CRDs to be established ..."
	creatingRootSync            = "Creating the RootSync object ..."
	waitingForReconcilerManager = "Waiting for the reconciler-manager Pod to be ready ..."
	waitingForRootReconciler    = "Waiting for the root-reconciler Pod to be ready ..."
	waitingForRGManager         = "Waiting for the resource-group-controller-manager Pod to be ready ..."
	deletingConfigManagement    = "Deleting the ConfigManagement operator ..."
	migrationSuccess            = "The migration process is done. Please check the sync status with `nomos status`"
)

// executeMigrate is the top-level function for the migration logic.
// It iterates through contexts and performs migration steps.
func executeMigrate(ctx context.Context, contexts []string, localDryRun bool, localWaitTimeout time.Duration, removeCM bool, connectTimeout time.Duration) error {
	// Note: connectTimeout is used by nomosstatus.ClusterClients, not directly here.
	clientMap, err := nomosstatus.ClusterClients(ctx, contexts)
	if err != nil {
		return fmt.Errorf("failed to get cluster clients: %w", err)
	}

	var migrationContexts []string
	migrationErrorOccurred := false
	overallErrorMessage := ""

	for contextName, c := range clientMap {
		migrationContexts = append(migrationContexts, contextName)
		fmt.Println()
		fmt.Println(util.Separator)
		printInfo("Migrating context: %s", contextName)

		cs := &nomosstatus.ClusterState{Ref: contextName}
		if !c.IsInstalled(ctx, cs) {
			printErrorToStdErr(cs.Error)
			migrationErrorOccurred = true
			overallErrorMessage += fmt.Sprintf("\nContext %s: Failed to confirm Config Sync installation: %v", contextName, cs.Error)
			continue
		}

		isHubManaged, err := c.ConfigManagement.IsManagedByHub(ctx)
		if err != nil {
			printErrorToStdErr(err)
			migrationErrorOccurred = true
			overallErrorMessage += fmt.Sprintf("\nContext %s: Failed to check if managed by Hub: %v", contextName, err)
			continue
		}
		if isHubManaged {
			errMsg := "The cluster is managed by Hub. Migration is not supported."
			printErrorToStdErr(errMsg)
			migrationErrorOccurred = true
			overallErrorMessage += fmt.Sprintf("\nContext %s: %s", contextName, errMsg)
			continue
		}

		err = migrateMonoRepoAndUpdateStatus(ctx, c, contextName, localDryRun, localWaitTimeout)
		if err != nil {
			printErrorToStdErr(err)
			migrationErrorOccurred = true
			overallErrorMessage += fmt.Sprintf("\nContext %s: Mono-repo migration failed: %v", contextName, err)
			continue
		}

		if removeCM {
			err := migrateConfigManagementAndUpdateStatus(ctx, c, contextName, localDryRun, localWaitTimeout)
			if err != nil {
				printErrorToStdErr(err)
				migrationErrorOccurred = true
				overallErrorMessage += fmt.Sprintf("\nContext %s: ConfigManagement removal failed: %v", contextName, err)
				continue
			}
		}
		printSuccess(migrationSuccess)
	}

	if migrationErrorOccurred {
		fmt.Fprintf(os.Stderr, "\nFinished migration with errors. Please see above for errors and check the status with `nomos status`.\nDetailed errors:%s\n", overallErrorMessage)
		return errors.New("migration encountered errors")
	}

	fmt.Printf("\nFinished migration on the contexts: %s. Please check the sync status with `nomos status`.\n", strings.Join(migrationContexts, ", "))
	return nil
}

func printErrorToStdErr(err interface{}) {
	fmt.Fprintf(os.Stderr, "%s%sError: %s.%s\n", util.Bullet, util.ColorRed, err, util.ColorDefault)
}

func printNotice(format string, a ...interface{}) {
	fmt.Printf(fmt.Sprintf("%s%sNotice: %s.%s\n", util.Bullet, util.ColorYellow, format, util.ColorDefault), a...)
}

func printInfo(format string, a ...interface{}) {
	fmt.Printf(fmt.Sprintf("%s%s.\n", util.Bullet, format), a...)
}

func printHint(format string, a ...interface{}) {
	fmt.Printf(fmt.Sprintf("%s%s%s.%s\n", util.Bullet, util.ColorCyan, format, util.ColorDefault), a...)
}

func printSuccess(format string, a ...interface{}) {
	fmt.Printf(fmt.Sprintf("%s%s%s.%s\n", util.Bullet, util.ColorGreen, format, util.ColorDefault), a...)
}

func dryRunMonoRepoExecution() {
	printInfo(updatingConfigManagement)
	printInfo(waitingForConfigSyncCRDs)
	printInfo(creatingRootSync)
	printInfo(waitingForReconcilerManager)
	printInfo(waitingForRootReconciler)
}

func executeMonoRepoMigrationSteps(ctx context.Context, sc *nomosstatus.ClusterClient, cmObj *unstructured.Unstructured, rs *v1beta1.RootSync, waitTimeout time.Duration) error {
	printInfo(updatingConfigManagement)
	if err := sc.ConfigManagement.UpdateConfigManagement(ctx, cmObj); err != nil {
		return fmt.Errorf("failed to update ConfigManagement: %w", err)
	}
	printInfo(waitingForConfigSyncCRDs)
	if err := waitForMultiRepoCRDsToBeEstablished(ctx, sc.Client, waitTimeout); err != nil {
		return fmt.Errorf("failed waiting for multi-repo CRDs: %w", err)
	}
	printInfo("The RootSync CRD has been established")

	printInfo(creatingRootSync)
	if err := sc.Client.Create(ctx, rs); err != nil {
		return fmt.Errorf("failed to create RootSync: %w", err)
	}

	printInfo(waitingForReconcilerManager)
	if err := waitForPodToBeRunning(ctx, sc.K8sClient, configmanagement.ControllerNamespace, "app=reconciler-manager", waitTimeout); err != nil {
		return fmt.Errorf("reconciler-manager pod not ready: %w", err)
	}
	printInfo("The reconciler-manager Pod is running")

	printInfo(waitingForRootReconciler)
	if err := waitForPodToBeRunning(ctx, sc.K8sClient, configmanagement.ControllerNamespace, "configsync.gke.io/reconciler=root-reconciler", waitTimeout); err != nil {
		return fmt.Errorf("root-reconciler pod not ready: %w", err)
	}
	printInfo("The root-reconciler Pod is running")

	printInfo(waitingForRGManager)
	if err := waitForPodToBeRunning(ctx, sc.K8sClient, configmanagement.RGControllerNamespace, "configsync.gke.io/deployment-name=resource-group-controller-manager", waitTimeout); err != nil {
		return fmt.Errorf("resource-group-controller-manager pod not ready: %w", err)
	}
	printInfo("The resource-group-controller-manager Pod is running")

	return nil
}

func recheck(fn func() error, currentWaitTimeout time.Duration) error {
	return retry.OnError(backoff(currentWaitTimeout), func(error) bool { return true }, func() error {
		return fn()
	})
}

func backoff(currentWaitTimeout time.Duration) wait.Backoff {
	steps := int(currentWaitTimeout / time.Second)
	if steps == 0 {
		steps = 1
	}
	return wait.Backoff{
		Duration: time.Second,
		Steps:    steps,
	}
}

func waitForPodToBeRunning(ctx context.Context, k8sclient *kubernetes.Clientset, ns string, labelSelector string, currentWaitTimeout time.Duration) error {
	return recheck(func() error {
		pods, err := k8sclient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			printErrorToStdErr(err)
			return err
		}
		if nomosstatus.HasRunningPod(pods.Items) {
			return nil
		}
		errMsg := fmt.Sprintf("%sHaven't detected running Pods with the label selector %q in namespace %q", util.Indent, labelSelector, ns)
		printInfo(errMsg)
		return errors.New(errMsg)
	}, currentWaitTimeout)
}

var configSyncCRDs = []string{
	configsync.RootSyncCRDName,
	configsync.RepoSyncCRDName,
	configsync.ResourceGroupCRDName,
}

func waitForMultiRepoCRDsToBeEstablished(ctx context.Context, c client.Client, currentWaitTimeout time.Duration) error {
	for _, crdName := range configSyncCRDs {
		if err := waitForCRDToBeEstablished(ctx, c, crdName, currentWaitTimeout); err != nil {
			return err
		}
	}
	return nil
}

func waitForCRDToBeEstablished(ctx context.Context, c client.Client, crdName string, currentWaitTimeout time.Duration) error {
	return recheck(func() error {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := c.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
			if apierrors.IsNotFound(err) {
				printInfo("%sCRD %s not found yet...", util.Indent, crdName)
			} else {
				printErrorToStdErr(err)
			}
			return err
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
				return nil
			}
		}
		errMsg := fmt.Sprintf("The %s CRD has not been established yet.", crdName)
		printInfo("%s%s", util.Indent, errMsg)
		return errors.New(errMsg)
	}, currentWaitTimeout)
}

func createRootSyncFromConfigManagement(ctx context.Context, cm *util.ConfigManagementClient) (*v1beta1.RootSync, error) {
	proxyConfig, err := cm.NestedString(ctx, "spec", "git", "httpProxy")
	if err != nil {
		return nil, err
	}
	httpsProxy, err := cm.NestedString(ctx, "spec", "git", "httpsProxy")
	if err != nil {
		return nil, err
	}
	desiredScheme := "http"
	if httpsProxy != "" {
		proxyConfig = httpsProxy
		desiredScheme = "https"
	}
	if proxyConfig != "" {
		parsedURL, err := url.Parse(proxyConfig)
		if err != nil {
			return nil, fmt.Errorf("malformed proxy config %s: %w", proxyConfig, err)
		}
		if parsedURL.Hostname() == "" {
			return nil, fmt.Errorf("malformed proxy config %s missing hostname", proxyConfig)
		}
		if parsedURL.Scheme != desiredScheme {
			return nil, fmt.Errorf("scheme for %s proxy %s needs to be %s", desiredScheme, proxyConfig, desiredScheme)
		}
	}

	sourceFormat, err := cm.NestedString(ctx, "spec", "sourceFormat")
	if err != nil {
		return nil, err
	}
	if sourceFormat == "" {
		sourceFormat = string(configsync.SourceFormatUnstructured)
	}

	syncRepo, err := cm.NestedString(ctx, "spec", "git", "syncRepo")
	if err != nil {
		return nil, err
	}
	if syncRepo == "" {
		return nil, errors.New("Git syncRepo is empty in ConfigManagement spec.git")
	}

	var secretRefName string
	var gcpServiceAccountEmail string
	secretType, err := cm.NestedString(ctx, "spec", "git", "secretType")
	if err != nil {
		secretType = "none"
	}

	switch secretType {
	case "ssh", "cookiefile", "token":
		secretRefName = "git-creds"
	case "gcpserviceaccount":
		gcpServiceAccountEmail, err = cm.NestedString(ctx, "spec", "git", "gcpServiceAccountEmail")
		if err != nil {
			return nil, fmt.Errorf("failed to get gcpServiceAccountEmail for secretType %s: %w", secretType, err)
		}
		if gcpServiceAccountEmail == "" {
			return nil, fmt.Errorf("gcpServiceAccountEmail not present, but is required when secretType is %s", secretType)
		}
	case "none", "":
		secretType = "none"
	default:
		return nil, fmt.Errorf("%v is an unknown secret type", secretType)
	}

	syncRev, err := cm.NestedString(ctx, "spec", "git", "syncRev")
	if err != nil {
		syncRev = ""
	}
	if syncRev == "" {
		syncRev = controllers.DefaultSyncRev
	}

	syncBranch, err := cm.NestedString(ctx, "spec", "git", "syncBranch")
	if err != nil {
		syncBranch = ""
	}
	if syncBranch == "" {
		syncBranch = controllers.DefaultSyncBranch
	}

	syncDir, err := cm.NestedString(ctx, "spec", "git", "policyDir")
	if err != nil {
		syncDir = ""
	}
	if syncDir == "" {
		syncDir = controllers.DefaultSyncDir
	}

	syncWaitSeconds, err := cm.NestedInt(ctx, "spec", "git", "syncWait")
	if err != nil {
		syncWaitSeconds = controllers.DefaultSyncWaitSecs
	}
	if syncWaitSeconds == 0 {
		syncWaitSeconds = controllers.DefaultSyncWaitSecs
	}

	return &v1beta1.RootSync{
		TypeMeta: metav1.TypeMeta{
			Kind:       configsync.RootSyncKind,
			APIVersion: v1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configsync.RootSyncName,
			Namespace: configmanagement.ControllerNamespace,
		},
		Spec: v1beta1.RootSyncSpec{
			SourceFormat: configsync.SourceFormat(sourceFormat),
			Git: &v1beta1.Git{
				Repo:                   syncRepo,
				Revision:               syncRev,
				Branch:                 syncBranch,
				Dir:                    syncDir,
				Period:                 metav1.Duration{Duration: time.Duration(syncWaitSeconds) * time.Second},
				Auth:                   configsync.AuthType(secretType),
				Proxy:                  proxyConfig,
				GCPServiceAccountEmail: gcpServiceAccountEmail,
				SecretRef: &v1beta1.SecretReference{
					Name: secretRefName,
				},
			},
		},
	}, nil
}

func saveRootSyncYAML(ctx context.Context, cm *util.ConfigManagementClient, contextName string) (*v1beta1.RootSync, string, error) {
	rs, err := createRootSyncFromConfigManagement(ctx, cm)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create RootSync object: %w", err)
	}
	content, err := yaml.Marshal(rs)
	if err != nil {
		return rs, "", fmt.Errorf("failed to marshal RootSync: %w", err)
	}

	dir := filepath.Join(os.TempDir(), monorepoMigrateDir, contextName)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return rs, "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	yamlFile := filepath.Join(dir, rootSyncYamlFile)
	if err := os.WriteFile(yamlFile, content, 0644); err != nil {
		printErrorToStdErr(fmt.Sprintf("Failed to write RootSync YAML to %s: %v (continuing)", yamlFile, err))
		return rs, yamlFile, nil
	}
	printInfo("A RootSync object is generated and saved in %q", yamlFile)
	return rs, yamlFile, nil
}

func saveConfigManagementYAML(ctx context.Context, cmClient *util.ConfigManagementClient, contextName string) (*unstructured.Unstructured, string, error) {
	dir := filepath.Join(os.TempDir(), monorepoMigrateDir, contextName)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return nil, "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	cmOrig, cmMulti, err := cmClient.EnableMultiRepo(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("failed to prepare ConfigManagement for multi-repo: %w", err)
	}
	content, err := yaml.Marshal(cmOrig)
	if err != nil {
		return cmMulti, "", fmt.Errorf("failed to marshal original ConfigManagement: %w", err)
	}
	origYAMLFile := filepath.Join(dir, cmOrigYAMLFile)
	if err := os.WriteFile(origYAMLFile, content, 0644); err != nil {
		printErrorToStdErr(fmt.Sprintf("Failed to write original ConfigManagement YAML to %s: %v (continuing)", origYAMLFile, err))
	} else {
		printInfo("The original ConfigManagement object is saved in %q", origYAMLFile)
	}

	content, err = yaml.Marshal(cmMulti)
	if err != nil {
		return cmMulti, "", fmt.Errorf("failed to marshal multi-repo ConfigManagement: %w", err)
	}
	multiYAMLFile := filepath.Join(dir, cmMultiYAMLFile)
	if err := os.WriteFile(multiYAMLFile, content, 0644); err != nil {
		printErrorToStdErr(fmt.Sprintf("Failed to write updated ConfigManagement YAML to %s: %v (continuing)", multiYAMLFile, err))
	} else {
		printInfo("The ConfigManagement object is updated and saved in %q", multiYAMLFile)
	}
	return cmMulti, multiYAMLFile, nil
}

func migrateMonoRepoAndUpdateStatus(ctx context.Context, cc *nomosstatus.ClusterClient, kubeCtx string, localDryRun bool, localWaitTimeout time.Duration) error {
	isMulti, err := cc.ConfigManagement.IsMultiRepo(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if multi-repo is enabled: %w", err)
	}
	if isMulti != nil && *isMulti {
		printNotice("The cluster is already running in the multi-repo mode. No RootSync will be created.")
		return nil
	}

	fmt.Printf("Enabling the multi-repo mode on cluster %q ...\n", kubeCtx)
	rootSync, rsYamlFile, err := saveRootSyncYAML(ctx, cc.ConfigManagement, kubeCtx)
	if err != nil {
		return fmt.Errorf("failed to save RootSync YAML: %w", err)
	}
	cmMulti, cmMultiYamlFile, err := saveConfigManagementYAML(ctx, cc.ConfigManagement, kubeCtx)
	if err != nil {
		return fmt.Errorf("failed to save ConfigManagement YAML: %w", err)
	}
	printHint(`Resources for the multi-repo mode have been saved in a temp folder. If the migration process is terminated, it can be recovered manually by running the following commands:
  kubectl apply -f %s && \
  kubectl wait --for condition=established crd rootsyncs.configsync.gke.io && \
  kubectl apply -f %s`, cmMultiYamlFile, rsYamlFile)

	if localDryRun {
		dryRunMonoRepoExecution()
		return nil
	}
	if err := executeMonoRepoMigrationSteps(ctx, cc, cmMulti, rootSync, localWaitTimeout); err != nil {
		return fmt.Errorf("mono-repo migration execution failed: %w", err)
	}
	return nil
}

// dryRunConfigManagementExecution prints messages for a dry run of CM removal.
func dryRunConfigManagementExecution() {
	printInfo(deletingConfigManagement)
}

// getOperatorObjects returns a list of objects associated with the ConfigManagement
// operator installation. The list is structured for deletion order:
// Deployment, ClusterRoleBinding, ClusterRole, ServiceAccount, ConfigManagement CR, CRD.
func getOperatorObjects() []*unstructured.Unstructured {
	var objs []*unstructured.Unstructured

	dep := &unstructured.Unstructured{}
	dep.SetGroupVersionKind(kinds.Deployment())
	dep.SetName(util.ACMOperatorDeployment)
	dep.SetNamespace(configmanagement.ControllerNamespace)
	objs = append(objs, dep)

	crb := &unstructured.Unstructured{}
	crb.SetGroupVersionKind(kinds.ClusterRoleBinding())
	crb.SetName(util.ACMOperatorDeployment)
	objs = append(objs, crb)

	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(kinds.ClusterRole())
	cr.SetName(util.ACMOperatorDeployment)
	objs = append(objs, cr)

	sa := &unstructured.Unstructured{}
	sa.SetGroupVersionKind(kinds.ServiceAccount())
	sa.SetName(util.ACMOperatorDeployment)
	sa.SetNamespace(configmanagement.ControllerNamespace)
	objs = append(objs, sa)

	cm := &unstructured.Unstructured{}
	cm.SetGroupVersionKind(kinds.ConfigManagement())
	cm.SetName(util.ConfigManagementName)
	objs = append(objs, cm)

	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(kinds.CustomResourceDefinitionV1())
	crd.SetName(util.ConfigManagementCRDName)
	objs = append(objs, crd)

	return objs
}

// executeConfigManagementMigrationSteps performs the migration from ConfigManagement
// installation to a standalone OSS Config Sync installation.
func executeConfigManagementMigrationSteps(ctx context.Context, sc *nomosstatus.ClusterClient, localWaitTimeout time.Duration) error {
	printInfo(waitingForConfigSyncCRDs)
	if err := waitForMultiRepoCRDsToBeEstablished(ctx, sc.Client, localWaitTimeout); err != nil {
		return fmt.Errorf("failed waiting for multi-repo CRDs post mono-repo migration: %w", err)
	}
	printInfo("The following CRDs have been established: %s", strings.Join(configSyncCRDs, ", "))

	printInfo(waitingForReconcilerManager)
	if err := waitForPodToBeRunning(ctx, sc.K8sClient, configmanagement.ControllerNamespace, "app=reconciler-manager", localWaitTimeout); err != nil {
		return fmt.Errorf("reconciler-manager pod not ready post mono-repo migration: %w", err)
	}
	printInfo("The reconciler-manager Pod is running")

	printInfo(waitingForRGManager)
	if err := waitForPodToBeRunning(ctx, sc.K8sClient, configmanagement.RGControllerNamespace, "configsync.gke.io/deployment-name=resource-group-controller-manager", localWaitTimeout); err != nil {
		return fmt.Errorf("resource-group-controller-manager pod not ready post mono-repo migration: %w", err)
	}
	printInfo("The resource-group-controller-manager Pod is running")

	printInfo("Deleting the ConfigManagement operator...")
	operatorObjects := getOperatorObjects()
	for _, obj := range operatorObjects {
		objDescription := fmt.Sprintf("%s %s/%s", obj.GetObjectKind().GroupVersionKind().String(), obj.GetNamespace(), obj.GetName())
		printInfo("Attempting to delete object: %s", objDescription)

		propagationPolicy := client.PropagationPolicy(metav1.DeletePropagationForeground)
		if obj.GetObjectKind().GroupVersionKind() == kinds.ConfigManagement() {
			propagationPolicy = client.PropagationPolicy(metav1.DeletePropagationOrphan)
			if err := sc.ConfigManagement.RemoveFinalizers(ctx); err != nil {
				printErrorToStdErr(fmt.Sprintf("Failed to remove finalizers from ConfigManagement object: %v. Manual check may be required. Continuing with deletion.", err))
			}
		}

		if err := sc.Client.Delete(ctx, obj, propagationPolicy); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete object %s: %w", objDescription, err)
			}
			printInfo("Object %s already not found.", objDescription)
		} else {
			printInfo("Deletion request sent for object: %s", objDescription)
		}

		key := client.ObjectKeyFromObject(obj)
		err := recheck(func() error {
			checkObj := &unstructured.Unstructured{}
			checkObj.SetGroupVersionKind(obj.GroupVersionKind())
			getErr := sc.Client.Get(ctx, key, checkObj)
			if apierrors.IsNotFound(getErr) {
				printInfo("Object %s successfully deleted.", objDescription)
				return nil
			}
			if getErr == nil {
				return fmt.Errorf("expected object %s to be NotFound, but it still exists", objDescription)
			}
			return fmt.Errorf("failed to get object %s during deletion check: %w", objDescription, getErr)
		}, localWaitTimeout)

		if err != nil {
			printErrorToStdErr(fmt.Sprintf("Failed to confirm deletion of object %s: %v. Manual check recommended.", objDescription, err))
			if obj.GetObjectKind().GroupVersionKind() == kinds.CustomResourceDefinitionV1() {
				printNotice("CRD deletion can take time. If it persists, ensure all custom resources of this type are deleted, or that the CRD is not in use by other components.")
			}
		}
	}
	printInfo("The ConfigManagement Operator is deleted.")
	return nil
}

// saveConfigManagementOperatorYaml saves the original config-management-operator YAML to a file.
func saveConfigManagementOperatorYaml(ctx context.Context, sc *nomosstatus.ClusterClient, contextName string) (string, error) {
	dir := filepath.Join(os.TempDir(), configManagementMigrateDir, contextName)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	operatorObjectsTemplate := getOperatorObjects()
	fetchedObjects := make([]*unstructured.Unstructured, 0, len(operatorObjectsTemplate))

	for _, objTemplate := range operatorObjectsTemplate {
		obj := objTemplate.DeepCopy()
		key := client.ObjectKeyFromObject(obj)
		if err := sc.Client.Get(ctx, key, obj); err != nil {
			if apierrors.IsNotFound(err) {
				printNotice("Object %s %s/%s not found on cluster, will not be saved in backup.", obj.GetKind(), obj.GetNamespace(), obj.GetName())
				continue
			}
			return "", fmt.Errorf("failed to read object %s %s/%s from cluster: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
		fetchedObjects = append(fetchedObjects, obj)
	}

	if len(fetchedObjects) == 0 {
		printNotice("No ConfigManagement operator objects found to save for context %s.", contextName)
		return "", nil
	}

	var content []byte
	for idx, uObj := range fetchedObjects {
		if idx != 0 {
			content = append(content, []byte("---\n")...)
		}
		yamlObj, err := yaml.Marshal(uObj)
		if err != nil {
			return "", fmt.Errorf("failed to marshal object %s %s/%s: %w", uObj.GetKind(), uObj.GetNamespace(), uObj.GetName(), err)
		}
		content = append(content, yamlObj...)
	}

	yamlFile := filepath.Join(dir, cmOperatorYAMLFile)
	if err := os.WriteFile(yamlFile, content, 0644); err != nil {
		printErrorToStdErr(fmt.Sprintf("Failed to write ConfigManagement operator YAML to %s: %v (continuing)", yamlFile, err))
		return yamlFile, nil
	}
	printInfo("The original ConfigManagement Operator objects are saved in %q", yamlFile)
	return yamlFile, nil
}

// migrateConfigManagementAndUpdateStatus contains the logic from the original migrateConfigManagement function.
func migrateConfigManagementAndUpdateStatus(ctx context.Context, cc *nomosstatus.ClusterClient, kubeCtx string, localDryRun bool, localWaitTimeout time.Duration) error {
	isOSS, err := util.IsOssInstallation(ctx, cc.ConfigManagement, cc.Client, cc.K8sClient)
	if err != nil {
		return fmt.Errorf("failed to check if OSS installation: %w", err)
	}
	if isOSS {
		printNotice("The cluster is already running as an OSS installation. No ConfigManagement operator removal needed.")
		return nil
	}
	isHNCEnabled, err := cc.ConfigManagement.IsHNCEnabled(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if HNC is enabled: %w", err)
	}
	if isHNCEnabled {
		return errors.New("Hierarchy Controller is enabled on the ConfigManagement object. It must be disabled before migrating to standalone Config Sync")
	}

	fmt.Printf("Removing ConfigManagement Operator on cluster %q to complete standalone Config Sync migration...\n", kubeCtx)
	cmOpYamlFile, err := saveConfigManagementOperatorYaml(ctx, cc, kubeCtx)
	if err != nil {
		printErrorToStdErr(fmt.Sprintf("Could not save ConfigManagement operator YAML (non-critical): %v", err))
	}
	if cmOpYamlFile != "" {
		printHint(`ConfigManagement Operator objects have been saved to %s. If you need to revert this step (before further changes), you might be able to reapply this YAML.
However, note that Config Sync (RootSync/RepoSync) is now managing your repository. Reverting is complex and not generally recommended.
The automated deletion will proceed with the following steps:
  1. Wait for ConfigSync CRDs, reconciler-manager, and resource-group-controller-manager to be ready (confirming Config Sync is operational).
  2. Delete ConfigManagement Deployment, ClusterRoleBinding, ClusterRole, ServiceAccount.
  3. Remove finalizers from ConfigManagement CR and delete it with 'orphan' propagation to keep Config Sync resources.
  4. Delete ConfigManagement CRD.
If this process is terminated, manual cleanup might be needed. Refer to Config Sync documentation for details on standalone installation.`, cmOpYamlFile)
	} else if err == nil { // No error from save, but no file path means nothing was found to save.
		printNotice("No ConfigManagement operator objects were found to save (this is normal if operator was already partially removed).")
	}

	if localDryRun {
		dryRunConfigManagementExecution()
		return nil
	}
	if err := executeConfigManagementMigrationSteps(ctx, cc, localWaitTimeout); err != nil {
		return fmt.Errorf("ConfigManagement operator removal failed: %w", err)
	}
	return nil
}
