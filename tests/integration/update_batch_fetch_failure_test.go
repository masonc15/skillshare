//go:build !online

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillshare/internal/install"
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

// The stored repo URL can carry a token as userinfo. The warning must name the
// repo without it, and must not echo it through git's error text either.
func TestUpdateAll_FetchWarningRedactsURLCredentials(t *testing.T) {
	sb := testutil.NewSandbox(t)
	defer sb.Cleanup()
	setupGlobalConfig(sb)

	// Nothing listens on port 1, so the clone fails right away.
	repoURL := "https://someone:s3cret-token@127.0.0.1:1/owner/skills.git"
	store, err := install.LoadMetadata(sb.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(sb.SourcePath, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		store.Set(name, &install.MetadataEntry{
			Source:  repoURL + "//skills/" + name,
			Type:    "git",
			RepoURL: repoURL,
			Subdir:  "skills/" + name,
		})
	}
	if err := store.Save(sb.SourcePath); err != nil {
		t.Fatal(err)
	}

	result := sb.RunCLI("update", "--all", "--skip-audit")
	result.AssertSuccess(t)
	result.AssertAnyOutputContains(t, "Could not fetch https://127.0.0.1:1/owner/skills.git")
	if out := result.Stdout + result.Stderr; strings.Contains(out, "s3cret-token") || strings.Contains(out, "someone:") {
		t.Fatalf("fetch warning leaked URL credentials:\n%s", out)
	}
}
