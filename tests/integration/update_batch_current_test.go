//go:build !online

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillshare/internal/install"
	"skillshare/internal/testutil"
)

type updateCounts struct {
	Updated  int `json:"updated"`
	UpToDate int `json:"up_to_date"`
	Skipped  int `json:"skipped"`
}

func runUpdateAllJSON(t *testing.T, sb *testutil.Sandbox, extra ...string) updateCounts {
	t.Helper()
	args := append([]string{"update", "--all", "--skip-audit", "--json"}, extra...)
	result := sb.RunCLI(args...)
	result.AssertSuccess(t)
	var counts updateCounts
	if err := json.Unmarshal([]byte(result.Stdout), &counts); err != nil {
		t.Fatalf("parse update JSON: %v\n%s", err, result.Stdout)
	}
	return counts
}

// setupGroupedSkills pushes skills/<name> for each name to a bare repo and
// installs them, returning the work clone used to push later changes.
func setupGroupedSkills(t *testing.T, sb *testutil.Sandbox, names ...string) (remote, work string) {
	t.Helper()
	remote = filepath.Join(sb.Root, "grouped.git")
	work = filepath.Join(sb.Root, "grouped-work")
	gitInit(t, remote, true)
	gitClone(t, remote, work)
	for _, name := range names {
		writeGroupedSkill(t, work, name, "v1")
	}
	gitAddCommit(t, work, "add skills")
	gitPush(t, work)
	for _, name := range names {
		sb.RunCLI("install", "file://"+remote+"//skills/"+name, "--skip-audit").AssertSuccess(t)
	}
	return remote, work
}

func writeGroupedSkill(t *testing.T, work, name, marker string) {
	t.Helper()
	dir := filepath.Join(work, "skills", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\n---\n# " + name + " " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// update --all used to reinstall every grouped skill on every run because its
// "already current" check read sidecars that no longer exist.
func TestUpdateAll_UnchangedGroupedSkillsAreUpToDate(t *testing.T) {
	sb := testutil.NewSandbox(t)
	defer sb.Cleanup()
	setupGlobalConfig(sb)
	_, work := setupGroupedSkills(t, sb, "alpha", "beta")

	if got := runUpdateAllJSON(t, sb); got.Updated != 0 || got.UpToDate != 2 {
		t.Fatalf("unchanged upstream: got %+v, want 0 updated and 2 up to date", got)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(sb.SourcePath, name, install.MetaFileName)); !os.IsNotExist(err) {
			t.Fatalf("%s: update must not write a sidecar, stat err=%v", name, err)
		}
	}

	writeGroupedSkill(t, work, "alpha", "v2")
	gitAddCommit(t, work, "change alpha")
	gitPush(t, work)

	if got := runUpdateAllJSON(t, sb); got.Updated != 1 || got.UpToDate != 1 {
		t.Fatalf("one skill changed: got %+v, want 1 updated and 1 up to date", got)
	}
	if data := sb.ReadFile(filepath.Join(sb.SourcePath, "alpha", "SKILL.md")); !strings.Contains(data, "alpha v2") {
		t.Fatalf("alpha not updated:\n%s", data)
	}

	if got := runUpdateAllJSON(t, sb, "--force"); got.Updated != 2 || got.UpToDate != 0 {
		t.Fatalf("--force: got %+v, want 2 updated", got)
	}
}

// Project mode shares the batch path; unchanged skills must stay up to date
// there too.
func TestUpdateProject_UnchangedGroupedSkillsAreUpToDate(t *testing.T) {
	sb := testutil.NewSandbox(t)
	defer sb.Cleanup()
	projectRoot := sb.SetupProjectDir("claude")

	remote := filepath.Join(sb.Root, "project-grouped.git")
	work := filepath.Join(sb.Root, "project-grouped-work")
	gitInit(t, remote, true)
	gitClone(t, remote, work)
	for _, name := range []string{"alpha", "beta"} {
		writeGroupedSkill(t, work, name, "v1")
	}
	gitAddCommit(t, work, "add skills")
	gitPush(t, work)
	for _, name := range []string{"alpha", "beta"} {
		sb.RunCLIInDir(projectRoot, "install", "file://"+remote+"//skills/"+name, "-p", "--skip-audit").AssertSuccess(t)
	}

	result := sb.RunCLIInDir(projectRoot, "update", "--all", "-p", "--skip-audit", "--json")
	result.AssertSuccess(t)
	var got updateCounts
	if err := json.Unmarshal([]byte(result.Stdout), &got); err != nil {
		t.Fatalf("parse update JSON: %v\n%s", err, result.Stdout)
	}
	if got.Updated != 0 || got.UpToDate != 2 {
		t.Fatalf("project mode, unchanged upstream: got %+v, want 0 updated and 2 up to date", got)
	}
}
