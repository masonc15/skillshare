//go:build !online

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"skillshare/internal/testutil"
)

// In a batch update, a repo that could not be fetched used to count its
// skills as skipped without any message. (A single target already reports
// the error through the verbose path.)
func TestUpdateAll_WarnsWhenRepoFetchFails(t *testing.T) {
	sb := testutil.NewSandbox(t)
	defer sb.Cleanup()
	setupGlobalConfig(sb)

	remote := filepath.Join(sb.Root, "unreachable.git")
	work := filepath.Join(sb.Root, "unreachable-work")
	gitInit(t, remote, true)
	gitClone(t, remote, work)
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(work, "skills", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitAddCommit(t, work, "add skills")
	gitPush(t, work)
	for _, name := range []string{"alpha", "beta"} {
		sb.RunCLI("install", "file://"+remote+"//skills/"+name, "--skip-audit").AssertSuccess(t)
	}

	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}

	result := sb.RunCLI("update", "--all", "--skip-audit")
	result.AssertSuccess(t)
	result.AssertAnyOutputContains(t, "Could not fetch")
	result.AssertAnyOutputContains(t, "unreachable.git")
	result.AssertAnyOutputContains(t, "2 skipped")
}
