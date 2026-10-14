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

// When HEAD moves but the skills' trees do not, the skip refreshes each
// recorded version in the store and keeps the full commit, which a project
// lock pin follows.
func TestUpdateSkillsFromRepo_TreeMatchRefreshesStoreVersion(t *testing.T) {
	repo := initTestRepo(t)
	writeSkill(t, repo, "skills/alpha", "# alpha v1")
	writeSkill(t, repo, "skills/beta", "# beta v1")

	sourceDir := t.TempDir()
	targets := map[string]string{
		"skills/alpha": filepath.Join(sourceDir, "alpha"),
		"skills/beta":  filepath.Join(sourceDir, "beta"),
	}
	opts := InstallOptions{Update: true, Force: true, SkipAudit: true, SourceDir: sourceDir}
	if _, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts); err != nil {
		t.Fatal(err)
	}
	before, err := LoadMetadata(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	installedCommit := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		if installedCommit[name] = before.Get(name).Commit; installedCommit[name] == "" {
			t.Fatalf("%s: install recorded no commit", name)
		}
	}

	writeSkill(t, repo, "skills/other", "# unrelated")

	result, err := UpdateSkillsFromRepo("file://"+repo, "", targets, opts)
	if err != nil {
		t.Fatal(err)
	}
	after, err := LoadMetadata(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if !result.Current["skills/"+name] {
			t.Fatalf("%s: expected tree-hash match to skip, results=%v errors=%v", name, result.Results, result.Errors)
		}
		entry := after.Get(name)
		if entry == nil {
			t.Fatalf("%s: missing store entry", name)
		}
		if entry.Commit != installedCommit[name] || entry.Version != result.CommitHash {
			t.Fatalf("%s store refresh: commit=%q version=%q, want commit=%q version=%q",
				name, entry.Commit, entry.Version, installedCommit[name], result.CommitHash)
		}
	}
}

// Grouped skills may still be stored under a legacy basename key with a
// Group field; the lookup and the refresh must use that key.
func TestInstalledSkillMeta_LegacyGroupedKey(t *testing.T) {
	sourceDir := t.TempDir()
	dest := filepath.Join(sourceDir, "frontend", "my-skill")
	store := NewMetadataStore()
	store.Set("my-skill", &MetadataEntry{Source: "x", Group: "frontend", Version: "old", TreeHash: "tree"})
	if err := store.Save(sourceDir); err != nil {
		t.Fatal(err)
	}

	installed := installedSkillMeta(store, sourceDir, dest)
	if installed == nil || installed.storeKey != "my-skill" || installed.TreeHash != "tree" {
		t.Fatalf("legacy key lookup: got %+v", installed)
	}
	if err := refreshStoreVersions(sourceDir, []string{installed.storeKey}, "new"); err != nil {
		t.Fatal(err)
	}
	after, err := LoadMetadata(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	entry := after.Get("my-skill")
	if entry == nil || entry.Version != "new" || after.Get("frontend/my-skill") != nil {
		t.Fatalf("refresh must update the legacy key in place, got %+v", after.Entries)
	}
}
