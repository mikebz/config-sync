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

package initialize

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/cli-runtime/pkg/printers"
	"kpt.dev/configsync/cmd/nomos/util"
	v1repo "kpt.dev/configsync/pkg/api/configmanagement/v1/repo"
	"kpt.dev/configsync/pkg/importer/filesystem/cmpath"
	"kpt.dev/configsync/pkg/status"
)

// executeInit is the main execution logic for the `nomos init` command.
// It initializes a directory structure for Anthos Configuration Management.
// rootPath corresponds to the --path flag, and force corresponds to the --force flag.
func executeInit(rootPath string, force bool) error {
	// If no path is specified, default to the current directory.
	if rootPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		rootPath = cwd
	}

	// Ensure the root directory exists, creating it if necessary.
	if _, err := os.Stat(rootPath); os.IsNotExist(err) {
		err = os.MkdirAll(rootPath, os.ModePerm)
		if err != nil {
			return fmt.Errorf("unable to create dir %q: %w", rootPath, err)
		}
	} else if err != nil {
		// Handle other stat errors (e.g., permission denied).
		return fmt.Errorf("failed to stat root directory %q: %w", rootPath, err)
	}

	// Convert to absolute path and cmpath.Absolute type.
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for %q: %w", rootPath, err)
	}
	rootDir, err := cmpath.AbsoluteOS(absPath)
	if err != nil {
		// This error might occur if the path is invalid for cmpath.
		return fmt.Errorf("failed to create cmpath.Absolute for %q: %w", absPath, err)
	}

	// If not forcing, check if the directory is empty (ignoring hidden files).
	if !force {
		err := checkEmpty(rootDir)
		if err != nil {
			return err // Returns specific error message if directory is not empty.
		}
	}

	// Use repoDirectoryBuilder to create the directory structure and files.
	repoBuilder := &repoDirectoryBuilder{
		root: rootDir,
		// errors field is initialized as nil (zero value for status.MultiError)
	}

	// Create root README.md.
	repoBuilder.createFile("", readmeFile, rootReadmeContents) // Constants from template_files.go

	// Create system/ directory and its README.
	repoBuilder.createDir(v1repo.SystemDir)
	repoBuilder.createSystemFile(readmeFile, systemReadmeContents) // Constants from template_files.go

	// Create default system/repo.yaml.
	repoObj, err := defaultRepo() // Function from template_files.go
	if err != nil {
		return fmt.Errorf("failed to create default Repo object: %w", err)
	}

	// Write the Repo object to system/repo.yaml.
	// util.WriteObject expects the root path of the repo, and the FileObject contains the relative path.
	err = util.WriteObject(&printers.YAMLPrinter{}, rootDir.OSPath(), repoObj)
	if err != nil {
		return fmt.Errorf("failed to write Repo object to file: %w", err)
	}

	// Create cluster/ directory.
	repoBuilder.createDir(v1repo.ClusterDir)

	// Create clusterregistry/ directory.
	repoBuilder.createDir(v1repo.ClusterRegistryDir)

	// Create namespaces/ directory.
	repoBuilder.createDir(v1repo.NamespacesDir)

	// Return any accumulated errors from the repoDirectoryBuilder.
	return repoBuilder.errors
}

// checkEmpty verifies if the given directory is empty, ignoring files that start with a dot.
func checkEmpty(dir cmpath.Absolute) error {
	files, err := os.ReadDir(dir.OSPath())
	if err != nil {
		return fmt.Errorf("error reading directory %q: %w", dir.OSPath(), err)
	}

	for _, file := range files {
		// Ignore hidden files/directories (e.g., .git)
		if !strings.HasPrefix(file.Name(), ".") {
			return fmt.Errorf("passed directory %q contains non-hidden file %q; use --force to proceed", dir.OSPath(), file.Name())
		}
	}
	return nil
}

// repoDirectoryBuilder helps in creating directories and files within the repository structure.
type repoDirectoryBuilder struct {
	root   cmpath.Absolute
	errors status.MultiError
}

// createDir creates a new directory within the repository.
// dirPath is relative to the repository root.
func (d *repoDirectoryBuilder) createDir(dirPath string) {
	if d.errors != nil && status.HasBlockingErrors(d.errors) {
		return // Don't attempt further operations if a blocking error occurred.
	}
	newDir := filepath.Join(d.root.OSPath(), dirPath)
	err := os.Mkdir(newDir, os.ModePerm)
	if err != nil {
		// Allow EEXIST errors if directory already exists (e.g. when --force is used on a partially initialized repo)
		if !os.IsExist(err) {
			d.errors = status.Append(d.errors, status.PathWrapError(err, newDir))
		}
	}
}

// createFile creates a new file with the given contents within the repository.
// dirPath is relative to the repository root, and fileName is the name of the file.
func (d *repoDirectoryBuilder) createFile(dirPath string, fileName string, contents string) {
	if d.errors != nil && status.HasBlockingErrors(d.errors) {
		return
	}
	fullDirPath := filepath.Join(d.root.OSPath(), dirPath)
	// Ensure the directory exists before creating the file in it.
	// This is important if dirPath is a nested path not created by createDir yet.
	// However, current usage seems to be for top-level or already created dirs.
	// For robustness, one might add:
	// if _, err := os.Stat(fullDirPath); os.IsNotExist(err) {
	//    if mkErr := os.MkdirAll(fullDirPath, os.ModePerm); mkErr != nil {
	//        d.errors = status.Append(d.errors, status.PathWrapError(mkErr, fullDirPath))
	//        return
	//    }
	// }

	filePath := filepath.Join(fullDirPath, fileName)
	file, err := os.Create(filePath)
	if err != nil {
		d.errors = status.Append(d.errors, status.PathWrapError(err, filePath))
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			// Append close error only if no other error has occurred for this file operation.
			if err == nil {
				d.errors = status.Append(d.errors, status.PathWrapError(closeErr, filePath))
			}
		}
	}()

	_, err = file.WriteString(contents)
	if err != nil {
		d.errors = status.Append(d.errors, status.PathWrapError(err, filePath))
	}
}

// createSystemFile is a helper to create a file specifically within the system/ directory.
func (d *repoDirectoryBuilder) createSystemFile(fileName string, contents string) {
	d.createFile(v1repo.SystemDir, fileName, contents)
}
