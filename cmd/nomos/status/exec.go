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

package status

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"text/tabwriter"
	"time"

	"k8s.io/klog/v2"
	nomosflags "kpt.dev/configsync/cmd/nomos/flags" // To access global flags if needed by helpers
	"kpt.dev/configsync/cmd/nomos/util"
	"kpt.dev/configsync/pkg/client/restconfig"
)

// executeStatus is the main entry point for the status command logic.
// It connects to clusters, fetches status, and prints it.
// outWriter is the io.Writer to which status output should be directed.
func executeStatus(
	ctx context.Context,
	contexts []string, // From nomosflags.Contexts
	clientTimeout time.Duration, // From nomosflags.ClientTimeout
	pollInterval time.Duration, // From local pollingInterval flag
	nsFilter string, // From local namespace flag
	includeResourceStatus bool, // From local resourceStatus flag
	nameFilter string, // From local name flag
	outWriter io.Writer,
) error {
	// clientTimeout is used by ClusterClients
	clientMap, err := ClusterClients(ctx, contexts)
	if err != nil {
		// The error from ClusterClients might be wrapped, check for specific underlying causes if needed.
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file or directory") {
			// This specific error check might be too fragile. Better to rely on error types if possible.
			return fmt.Errorf("failed to create client configs (is your kubeconfig correctly setup?): %w", err)
		}
		// klog.Fatalf is not suitable for a library function, return error instead.
		return fmt.Errorf("failed to get clients: %w", err)
	}

	if len(clientMap) == 0 {
		// This message can be printed by the caller (command.go RunE) or here.
		// fmt.Fprintln(outWriter, "No clusters found.")
		return errors.New("no clusters found to check status")
	}

	// Use a sorted order of names to avoid shuffling in the output.
	sortedClusterNames := getSortedClusterNames(clientMap)

	// Create a tabwriter for formatted output.
	// The util.NewWriter(os.Stdout) was used before. We should use the passed outWriter.
	tw := util.NewWriter(outWriter) // Assuming util.NewWriter handles tabwriter initialization

	if pollInterval > 0 {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err() // Exit if context is cancelled
			default:
				// Pass nsFilter and nameFilter to printStatus
				printStatusToWriter(ctx, tw, clientMap, sortedClusterNames, nsFilter, nameFilter, includeResourceStatus, pollInterval > 0)
				time.Sleep(pollInterval)
			}
		}
	} else {
		printStatusToWriter(ctx, tw, clientMap, sortedClusterNames, nsFilter, nameFilter, includeResourceStatus, false)
	}
	return nil
}

// SaveToTempFile writes the `nomos status` output into a temporary file, and
// opens the file for reading. It's used by `nomos bugreport`.
func SaveToTempFile(ctx context.Context, contexts []string, clientTimeout time.Duration, nsFilter string, includeResourceStatus bool, nameFilter string) (*os.File, error) {
	tmpFile, err := os.CreateTemp(os.TempDir(), "nomos-status-")
	if err != nil {
		return nil, fmt.Errorf("failed to create a temporary file: %w", err)
	}
	// Note: The original function used util.NewWriter(tmpFile) for tabwriter.
	// For consistency, if printStatusToWriter expects a tabwriter, we should create one.
	// If it handles raw io.Writer, then tmpFile is fine. Let's assume it needs a tabwriter.
	tabWriter := util.NewWriter(tmpFile)

	// clientTimeout is used by ClusterClients
	clientMap, err := ClusterClients(ctx, contexts)
	if err != nil {
		// Ensure file is closed and potentially removed on error.
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to get cluster clients for status export: %w", err)
	}

	if len(clientMap) == 0 {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, errors.New("no clusters found for status export")
	}

	sortedClusterNames := getSortedClusterNames(clientMap)

	// Pass nsFilter, nameFilter, includeResourceStatus to printStatusToWriter
	printStatusToWriter(ctx, tabWriter, clientMap, sortedClusterNames, nsFilter, nameFilter, includeResourceStatus, false) // false for isPolling

	// Ensure tabwriter flushes its content to the underlying tmpFile.
	if f, ok := tabWriter.Writer.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, fmt.Errorf("failed to flush status writer: %w", err)
		}
	}


	if err := tmpFile.Close(); err != nil {
		// Attempt to remove the file if closing fails, as it might be corrupted.
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to close status temp file: %w", err)
	}

	// Re-open the file for reading.
	f, err := os.Open(tmpFile.Name())
	if err != nil {
		// Attempt to remove if open fails.
		os.Remove(tmpFile.Name())
		return nil, fmt.Errorf("failed to open status temp file for reading: %w", err)
	}

	return f, nil
}


// getSortedClusterNames returns a sorted list of names from the given clientMap.
// Renamed from clusterNames to be more descriptive.
func getSortedClusterNames(clientMap map[string]*ClusterClient) []string {
	var names []string
	for name := range clientMap {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// getAllClusterStates calculates the status for all clusters.
// Renamed from clusterStates. It now takes nsFilter and nameFilter.
func getAllClusterStates(ctx context.Context, clientMap map[string]*ClusterClient, nsFilter string, nameFilter string, includeResourceDetail bool) (map[string]*ClusterState, []string) {
	stateMap := make(map[string]*ClusterState)
	var monoRepoClusters []string
	for name, client := range clientMap {
		if client == nil {
			stateMap[name] = unavailableCluster(name) // unavailableCluster is defined in cluster_state.go
		} else {
			// Pass nsFilter, nameFilter, includeResourceDetail to clusterStatus
			// Assuming clusterStatus (method on ClusterClient) is updated to accept these.
			// This requires changes in client.go or cluster_state.go.
			// For now, let's assume the filters are applied *after* getting the full ClusterState,
			// or that clusterStatus is adapted.
			// The original code passed global `namespace` to `client.clusterStatus`.
			// Now we pass the filtered `nsFilter`.
			cs := client.clusterStatus(ctx, name, nsFilter, nameFilter, includeResourceDetail)
			stateMap[name] = cs
			if cs.isMulti != nil && !*cs.isMulti { // isMulti is part of ClusterState
				monoRepoClusters = append(monoRepoClusters, name)
			}
		}
	}
	return stateMap, monoRepoClusters
}

// printStatusToWriter fetches and prints status to the given tabwriter.
// Renamed from printStatus. Added nsFilter, nameFilter, includeResourceDetail, isPolling.
func printStatusToWriter(
	ctx context.Context,
	writer *tabwriter.Writer,
	clientMap map[string]*ClusterClient,
	sortedClusterNames []string,
	nsFilter string,
	nameFilter string,
	includeResourceDetail bool,
	isPolling bool,
) {
	// First build up a map of all the states to display.
	// Pass nsFilter, nameFilter, includeResourceDetail to getAllClusterStates.
	stateMap, monoRepoClusters := getAllClusterStates(ctx, clientMap, nsFilter, nameFilter, includeResourceDetail)

	// Log a notice for the detected clusters that are running in the mono-repo mode.
	util.MonoRepoNotice(writer, monoRepoClusters...) // This function is in nomos/util.

	currentContext, err := restconfig.CurrentContextName()
	if err != nil {
		// Log or handle error, but don't let it stop status printing for other clusters.
		fmt.Fprintf(writer, "Failed to get current context name: %v\n", err)
	}

	if isPolling {
		clearTerminal(writer) // clearTerminal expects io.Writer, tabwriter wraps one.
		// The original code flushed the writer *after* clearTerminal.
		// It should be flushed *before* printing new content to ensure clear is visible.
		// However, clearTerminal writes to the writer, so flushing after is also fine.
	}

	// Print status for each cluster.
	for _, name := range sortedClusterNames {
		state := stateMap[name]
		if state == nil { // Should not happen if unavailableCluster is used.
			fmt.Fprintf(writer, "%s:\tCluster status not available\n", name)
			continue
		}
		originalRef := state.Ref // Preserve original ref if it was set (e.g. for unavailableCluster)
		if name == currentContext {
			state.Ref = "*" + name
		} else if originalRef == "" { // Avoid overwriting pre-set Ref like in unavailableCluster
			state.Ref = name
		}
		// Assuming state.printRows takes nsFilter, nameFilter, includeResourceDetail
		// to filter what it prints. This requires changes in cluster_state.go.
		// The original `state.printRows(writer)` did not take these.
		// Filters (namespace, name) are now handled when fetching state in `getAllClusterStates` via `client.clusterStatus`.
		// The `includeResourceDetail` is also passed to `client.clusterStatus`.
		// So, `printRows` itself may not need these filters if `ClusterState` is already filtered.
		state.printRows(writer) // printRows is a method on ClusterState, defined in cluster_state.go
	}

	if err := writer.Flush(); err != nil {
		klog.Warningf("Failed to flush status writer: %v", err)
		// To be safe, attempt to print to os.Stderr if writer is os.Stdout based.
		if w, ok := writer.Writer.(io.Writer); ok && w == os.Stdout {
			fmt.Fprintf(os.Stderr, "Error flushing status to stdout: %v\n", err)
		}
	}
}

// clearTerminal executes an OS-specific command to clear all output on the terminal.
func clearTerminal(out io.Writer) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "cls")
	default:
		cmd = exec.Command("clear")
	}
	cmd.Stdout = out // Direct output of 'clear' command to the given writer.
	if err := cmd.Run(); err != nil {
		// Don't log to klog here, as it might interfere with library usage.
		// Print a fallback clear sequence or just a few newlines.
		fmt.Fprint(out, "\n\n\n") // Fallback: print some newlines
		// Or, more robustly, could use a library for terminal manipulation if this needs to be perfect.
		// For now, just log that the command failed.
		// Consider making this error returnable if clearing is critical.
		// klog.Warningf("Failed to execute clear command: %v", err)
		fmt.Fprintf(os.Stderr, "Debug: clearTerminal failed: %v\n", err) // Temporary debug
	}
}
