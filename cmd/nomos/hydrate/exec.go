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

package hydrate

import (
	"context"
	"fmt"
	"os"
	"time"

	nomosflags "kpt.dev/configsync/cmd/nomos/flags"
	nomosparse "kpt.dev/configsync/cmd/nomos/parse"
	"kpt.dev/configsync/cmd/nomos/util"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/declared"
	"kpt.dev/configsync/pkg/hydrate"
	"kpt.dev/configsync/pkg/importer/analyzer/ast"
	"kpt.dev/configsync/pkg/importer/filesystem"
	"kpt.dev/configsync/pkg/importer/filesystem/cmpath"
	"kpt.dev/configsync/pkg/importer/reader"
	"kpt.dev/configsync/pkg/status"
)

// executeHydrate is the main logic for the hydrate command.
// It mirrors the RunE function from the original cmd/nomos/hydrate/hydrate.go.
func executeHydrate(
	ctx context.Context,
	path string, // Corresponds to nomosflags.Path
	clusters []string, // Corresponds to nomosflags.Clusters
	allClusters bool, // Corresponds to nomosflags.AllClusters()
	skipAPIServerCheck bool, // Corresponds to nomosflags.SkipAPIServerCheck
	sourceFormat configsync.SourceFormat, // Corresponds to configsync.SourceFormat(nomosflags.SourceFormat)
	outputFormat string, // Corresponds to nomosflags.OutputFormat
	apiServerTimeout time.Duration, // Corresponds to nomosflags.APIServerTimeout
	flat bool, // Corresponds to local flatOutput flag
	outPath string, // Corresponds to local outputPath flag
) error {
	// Validate sourceFormat, default if empty
	if sourceFormat == "" {
		sourceFormat = configsync.SourceFormatHierarchy
	}

	// Validate hydrate flags (e.g., Kustomize usage)
	// The path used here should be the one provided by the --path flag.
	rootDirPath := path
	if rootDirPath == "" {
		// Default to current directory if --path is not specified, mirroring original behavior implicit in FindFiles.
		// However, ValidateHydrateFlags expects a non-empty path.
		// The original code's `hydrate.ValidateHydrateFlags` seems to be called with `flags.Path`.
		// If `flags.Path` can be empty, this needs careful handling.
		// Assuming `flags.Path` (passed as `path` here) will have a default if not set by user (e.g. ".")
		// For now, let's proceed assuming `path` is correctly defaulted by Cobra or flag setup if applicable.
		// If `path` is truly empty, `cmpath.AbsoluteOS` will likely use current dir.
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		rootDirPath = cwd
	}

	absRootDir, err := cmpath.AbsoluteOS(rootDirPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for %q: %w", rootDirPath, err)
	}

	// The hydrate.ValidateHydrateFlags now takes sourceFormat and the path string.
	// It returns the effective rootDir (cmpath.Absolute) and whether hydration is needed.
	effectiveRootDir, needsHydrate, err := hydrate.ValidateHydrateFlags(sourceFormat, absRootDir.OSPath())
	if err != nil {
		return fmt.Errorf("failed to validate hydrate flags: %w", err)
	}

	if needsHydrate {
		// update rootDir to point to the hydrated output for further processing.
		// ValidateAndRunKustomize returns the path to the hydrated output directory.
		hydratedPath, err := hydrate.ValidateAndRunKustomize(effectiveRootDir.OSPath())
		if err != nil {
			return fmt.Errorf("kustomize hydration failed: %w", err)
		}
		effectiveRootDir = hydratedPath // This is a cmpath.Absolute object
		// delete the hydrated output directory in the end.
		defer func() {
			// It's good practice to check the error from RemoveAll, but in defer it's often ignored.
			// For critical cleanup, error handling might be needed.
			_ = os.RemoveAll(effectiveRootDir.OSPath())
		}()
	}

	// Find files in the (potentially hydrated) root directory
	files, err := nomosparse.FindFiles(effectiveRootDir)
	if err != nil {
		return fmt.Errorf("failed to find files in %q: %w", effectiveRootDir.OSPath(), err)
	}

	parser := filesystem.NewParser(&reader.File{})

	// Setup validation options
	validateOpts, err := hydrate.ValidateOptions(ctx, effectiveRootDir, apiServerTimeout)
	if err != nil {
		return fmt.Errorf("failed to create validation options: %w", err)
	}
	validateOpts.FieldManager = util.FieldManager // As used in original code.
	validateOpts.SkipAPIServer = skipAPIServerCheck // Pass the flag value.

	if sourceFormat == configsync.SourceFormatHierarchy {
		files = filesystem.FilterHierarchyFiles(effectiveRootDir, files)
	} else {
		// hydrate as a root repository to preview all the hydrated configs
		validateOpts.Scope = declared.RootScope
	}

	filePaths := reader.FilePaths{
		RootDir:   effectiveRootDir,
		PolicyDir: cmpath.RelativeOS(effectiveRootDir.OSPath()), // Assuming PolicyDir is relative to rootDir
		Files:     files,
	}

	parseOpts := hydrate.ParseOptions{
		Parser:       parser,
		SourceFormat: sourceFormat,
		FilePaths:    filePaths,
	}

	var allObjects []ast.FileObject
	encounteredError := false
	numClusters := 0

	clusterFilterFunc := func(clusterName string, fileObjects []ast.FileObject, multiErr status.MultiError) {
		clusterEnabled := allClusters // from --all-clusters flag
		if !clusterEnabled {
			for _, c := range clusters { // from --clusters flag
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
			// util.PrintErrOrDie will os.Exit(1), which we want to avoid here to allow cleanup.
			// Instead, log the error and set encounteredError.
			fmt.Fprintf(os.Stderr, "errors for Cluster %q: %v\n", actualClusterName, multiErr)
			encounteredError = true

			if status.HasBlockingErrors(multiErr) {
				return
			}
		}
		allObjects = append(allObjects, fileObjects...)
	}

	// Process for each cluster
	hydrate.ForEachCluster(ctx, parseOpts, validateOpts, clusterFilterFunc)

	multiCluster := numClusters > 1
	finalFileObjects := hydrate.GenerateFileObjects(multiCluster, allObjects...)

	// Output results
	if flat { // from --flat flag
		err = hydrate.PrintFlatOutput(outPath, outputFormat, finalFileObjects)
	} else {
		err = hydrate.PrintDirectoryOutput(outPath, outputFormat, finalFileObjects)
	}
	if err != nil {
		return fmt.Errorf("failed to print output: %w", err)
	}

	if encounteredError {
		// Mimic os.Exit(1) by returning an error that the calling command handler can use.
		return fmt.Errorf("one or more errors encountered during hydration")
	}

	return nil
}
