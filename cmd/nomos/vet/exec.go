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

package vet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // Aliased to avoid confusion
	nomosparse "kpt.dev/configsync/cmd/nomos/parse"
	"kpt.dev/configsync/cmd/nomos/util"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/declared"
	"kpt.dev/configsync/pkg/hydrate"
	"kpt.dev/configsync/pkg/importer/analyzer/ast"
	"kpt.dev/configsync/pkg/importer/filesystem"
	"kpt.dev/configsync/pkg/importer/filesystem/cmpath"
	"kpt.dev/configsync/pkg/importer/reader"
	"kpt.dev/configsync/pkg/parse"
	"kpt.dev/configsync/pkg/reconcilermanager"
	"kpt.dev/configsync/pkg/status"
)

// executeVet is the main execution function for the vet command.
// It takes vetOptions populated from command line flags.
func executeVet(ctx context.Context, out io.Writer, opts vetOptions) error {
	// Determine effective sourceFormat
	effectiveSourceFormat := opts.SourceFormat
	if effectiveSourceFormat == "" {
		if opts.Namespace == "" {
			effectiveSourceFormat = configsync.SourceFormatHierarchy
		} else {
			effectiveSourceFormat = configsync.SourceFormatUnstructured
		}
	}

	// Validate hydrate flags and potentially run Kustomize
	// The path for hydration comes from opts.Path (originally flags.Path)
	// hydrate.ValidateHydrateFlags now takes sourceFormat and the path string.
	// It returns the effective rootDir (cmpath.Absolute) and whether hydration is needed.
	var hydratedRootDir cmpath.Absolute
	var needsHydrate bool
	var err error

	// Convert opts.Path to absolute OS path for ValidateHydrateFlags
	absPath := opts.Path
	if !filepath.IsAbs(absPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		absPath = filepath.Join(cwd, absPath)
	}

	// If opts.Path is empty, ValidateHydrateFlags will use current dir.
	hydratedRootDir, needsHydrate, err = hydrate.ValidateHydrateFlags(effectiveSourceFormat, absPath)
	if err != nil {
		return err
	}

	if needsHydrate {
		hydratedRootDir, err = hydrate.ValidateAndRunKustomize(hydratedRootDir.OSPath())
		if err != nil {
			return fmt.Errorf("kustomize hydration failed: %w", err)
		}
		if !opts.KeepOutput { // only defer removal if we are not keeping output
			defer func() {
				// It's good practice to check the error from RemoveAll, but in defer it's often ignored.
				_ = os.RemoveAll(hydratedRootDir.OSPath())
			}()
		}
	}


	files, err := nomosparse.FindFiles(hydratedRootDir)
	if err != nil {
		return err
	}

	parser := filesystem.NewParser(&reader.File{})

	validateOpts, err := hydrate.ValidateOptions(ctx, hydratedRootDir, opts.APIServerTimeout)
	if err != nil {
		return err
	}
	validateOpts.FieldManager = util.FieldManager // util.FieldManager is "nomos"
	validateOpts.MaxObjectCount = opts.MaxObjectCount
	validateOpts.SkipAPIServer = opts.SkipAPIServer // Pass the skipAPIServer check

	switch effectiveSourceFormat {
	case configsync.SourceFormatHierarchy:
		if opts.Namespace != "" {
			return fmt.Errorf("if --namespace is provided, --%s must be omitted or set to %s",
				reconcilermanager.SourceFormat, configsync.SourceFormatUnstructured)
		}
		files = filesystem.FilterHierarchyFiles(hydratedRootDir, files)
	case configsync.SourceFormatUnstructured:
		if opts.Namespace == "" {
			validateOpts = parse.OptionsForScope(validateOpts, declared.RootScope)
		} else {
			validateOpts = parse.OptionsForScope(validateOpts, declared.Scope(opts.Namespace))
		}
	default:
		return fmt.Errorf("unknown %s value %q", reconcilermanager.SourceFormat, effectiveSourceFormat)
	}

	filePaths := reader.FilePaths{
		RootDir:   hydratedRootDir,
		PolicyDir: cmpath.RelativeOS(hydratedRootDir.OSPath()), // Assuming PolicyDir is relative to rootDir
		Files:     files,
	}

	parseOpts := hydrate.ParseOptions{
		Parser:       parser,
		SourceFormat: effectiveSourceFormat,
		FilePaths:    filePaths,
	}

	var allObjects []ast.FileObject
	var vetErrs []string
	numClusters := 0

	// Access nomosflags.AllClusters() and nomosflags.Clusters for filtering
	clusterFilterFunc := func(clusterName string, fileObjects []ast.FileObject, multiErr status.MultiError) {
		clusterEnabled := nomosflags.AllClusters() // From global flags
		if !clusterEnabled {
			for _, c := range nomosflags.Clusters { // From global flags
				if clusterName == c {
					clusterEnabled = true
					break
				}
			}
		}
		if !clusterEnabled {
			return
		}
		numClusters++

		if multiErr != nil {
			actualClusterName := clusterName
			if actualClusterName == "" {
				actualClusterName = nomosparse.UnregisteredCluster
			}
			vetErrs = append(vetErrs, clusterErrors{
				name:       actualClusterName,
				MultiError: multiErr,
			}.Error())
		}

		if opts.KeepOutput { // opts.KeepOutput was from local keepOutput flag
			allObjects = append(allObjects, fileObjects...)
		}
	}

	hydrate.ForEachCluster(ctx, parseOpts, validateOpts, clusterFilterFunc)

	if opts.KeepOutput {
		multiCluster := numClusters > 1
		fileObjectsToPrint := hydrate.GenerateFileObjects(multiCluster, allObjects...)
		// opts.OutputPath was from local outPath flag
		// opts.OutputFormat was from global flags.OutputFormat
		if err := hydrate.PrintDirectoryOutput(opts.OutputPath, opts.OutputFormat, fileObjectsToPrint); err != nil {
			// The original code used util.PrintErr which prints to stderr and doesn't exit.
			// We should return this error or handle it similarly.
			fmt.Fprintf(os.Stderr, "Error printing directory output: %v\n", err)
			// Potentially, this could be added to vetErrs as well.
		}
	}

	if len(vetErrs) > 0 {
		return errors.New(strings.Join(vetErrs, "\n\n"))
	}

	_, err = fmt.Fprintln(out, "✅ No validation issues found.")
	return err
}

// clusterErrors is the set of vet errors for a specific Cluster.
type clusterErrors struct {
	name string
	status.MultiError
}

// Error implements the error interface.
func (e clusterErrors) Error() string {
	if e.name == nomosparse.UnregisteredCluster { // Use const for "defaultcluster" if available, or match string
		return e.MultiError.Error()
	}
	return fmt.Sprintf("errors for cluster %q:\n%v", e.name, e.MultiError.Error())
}
