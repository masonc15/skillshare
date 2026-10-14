package install

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func gitSupportsSparseCheckout() bool {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return false
	}
	return supportsSparseCheckoutVersion(strings.TrimSpace(string(out)))
}

func supportsSparseCheckoutVersion(versionOutput string) bool {
	// Examples:
	//   git version 2.44.0
	//   git version 2.39.3 (Apple Git-146)
	fields := strings.Fields(versionOutput)
	if len(fields) < 3 {
		return false
	}
	ver := fields[2]
	parts := strings.Split(ver, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	if major > 2 {
		return true
	}
	return major == 2 && minor >= 25
}

func sparseCloneSubdir(url, subdir, destPath, branch string, extraEnv []string, onProgress ProgressCallback) error {
	return sparseClonePaths(url, []string{subdir}, destPath, branch, extraEnv, onProgress)
}

// sparseClonePaths is sparseCloneSubdir for several subdirs at once, so a
// batch update fetches only the blobs of the skills it refreshes.
func sparseClonePaths(url string, subdirs []string, destPath, branch string, extraEnv []string, onProgress ProgressCallback) error {
	paths := make([]string, 0, len(subdirs))
	for _, subdir := range subdirs {
		subdir = strings.TrimSpace(subdir)
		if subdir == "" {
			return fmt.Errorf("sparse checkout requires non-empty subdir")
		}
		paths = append(paths, subdir)
	}
	if len(paths) == 0 {
		return fmt.Errorf("sparse checkout requires non-empty subdir")
	}

	cloneArgs := []string{"clone", "--filter=blob:none", "--no-checkout", "--depth", "1"}
	// clone --branch rejects a commit SHA: clone the default branch and fetch
	// the commit below instead.
	sha := IsCommitSHA(branch)
	if branch != "" && !sha {
		cloneArgs = append(cloneArgs, "--branch", branch)
	}
	if onProgress != nil {
		cloneArgs = append(cloneArgs, "--progress")
	} else {
		cloneArgs = append(cloneArgs, "--quiet")
	}
	cloneArgs = append(cloneArgs, url, destPath)
	if err := runGitCommandWithProgress(cloneArgs, "", extraEnv, onProgress); err != nil {
		return err
	}

	if err := runGitCommandWithProgress(append([]string{"sparse-checkout", "set"}, paths...), destPath, extraEnv, nil); err != nil {
		return err
	}

	if sha {
		// Servers only resolve a full SHA here; an abbreviated one fails and the
		// caller falls back to a full clone.
		if err := runGitCommandWithProgress([]string{"fetch", "--quiet", "--depth", "1", "--filter=blob:none", "origin", branch}, destPath, extraEnv, nil); err != nil {
			return err
		}
		return runGitCommandWithProgress([]string{"checkout", "--quiet", "--detach", "FETCH_HEAD"}, destPath, extraEnv, nil)
	}
	return runGitCommandWithProgress([]string{"checkout"}, destPath, extraEnv, nil)
}
