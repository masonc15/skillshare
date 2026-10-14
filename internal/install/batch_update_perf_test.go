package install

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkill commits SKILL.md content at repo/subdir.
func writeSkill(t *testing.T, repo, subdir, body string) {
	t.Helper()
	dir := filepath.Join(repo, filepath.FromSlash(subdir))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+filepath.Base(dir)+"\n---\n"+body+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, repo, ".")
	gitCommit(t, repo, "update "+subdir)
}

// The batch fast path must read the centralized store: sidecars no longer
// exist after migration, so reading them made every update --all reinstall
// every skill.
func TestUpdateSkillsFromRepo_SkipsSkillCurrentInStore(t *testing.T) {
	repo := initTestRepo(t)
	writeSkill(t, repo, "skills/my-skill", "# v1")

	sourceDir := t.TempDir()
	dest := filepath.Join(sourceDir, "my-skill")
	targets := map[string]string{"skills/my-skill": dest}
	opts := InstallOptions{Update: true, Force: true, SkipAudit: true, SourceDir: sourceDir}

	first, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if first.Results["skills/my-skill"] == nil {
		t.Fatalf("expected initial install, errors=%v", first.Errors)
	}

	second, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	if second.Results["skills/my-skill"] != nil || !second.Current["skills/my-skill"] {
		t.Fatalf("expected unchanged skill to be skipped as current, results=%v current=%v errors=%v",
			second.Results, second.Current, second.Errors)
	}
	if _, err := os.Stat(filepath.Join(dest, MetaFileName)); !os.IsNotExist(err) {
		t.Fatalf("fast path must not create a sidecar, stat err=%v", err)
	}

	// Metadata outlives a deleted install; the update must restore it.
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	restored, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatalf("restore update: %v", err)
	}
	if restored.Results["skills/my-skill"] == nil {
		t.Fatalf("deleted install must be reinstalled, current=%v errors=%v", restored.Current, restored.Errors)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("expected SKILL.md restored: %v", err)
	}

	opts.Reinstall = true
	forced, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatalf("forced update: %v", err)
	}
	if forced.Results["skills/my-skill"] == nil {
		t.Fatalf("Reinstall must bypass the fast path, current=%v errors=%v", forced.Current, forced.Errors)
	}
}

// When HEAD moves but the skill's tree does not, the skip must refresh the
// recorded version and commit in the store so the next run's remote check
// can skip the clone.
func TestUpdateSkillsFromRepo_TreeMatchRefreshesStoreCommit(t *testing.T) {
	repo := initTestRepo(t)
	writeSkill(t, repo, "skills/my-skill", "# v1")

	sourceDir := t.TempDir()
	dest := filepath.Join(sourceDir, "my-skill")
	targets := map[string]string{"skills/my-skill": dest}
	opts := InstallOptions{Update: true, Force: true, SkipAudit: true, SourceDir: sourceDir}
	if _, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts); err != nil {
		t.Fatal(err)
	}

	writeSkill(t, repo, "skills/other", "# unrelated")
	head, err := getGitFullHash(repo)
	if err != nil {
		t.Fatal(err)
	}

	result, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Current["skills/my-skill"] {
		t.Fatalf("expected tree-hash match to skip, results=%v errors=%v", result.Results, result.Errors)
	}
	store, err := LoadMetadata(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	entry := store.Get("my-skill")
	if entry == nil {
		t.Fatal("missing store entry")
	}
	if entry.Commit != head || entry.Version != result.CommitHash {
		t.Fatalf("store not refreshed: commit=%q version=%q, want commit=%q version=%q",
			entry.Commit, entry.Version, head, result.CommitHash)
	}
}
