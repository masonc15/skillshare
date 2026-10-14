package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"skillshare/internal/install"
)

func TestRecordedCommitMatches(t *testing.T) {
	full := "9d7ba42a94bc4ec018e624f3033602188f64af63"
	tests := []struct {
		name string
		meta install.MetadataEntry
		want bool
	}{
		{"full commit match", install.MetadataEntry{Commit: full, Version: "0000000"}, true},
		{"full commit mismatch wins over version", install.MetadataEntry{Commit: strings.Repeat("a", 40), Version: "9d7ba42"}, false},
		{"legacy version prefix", install.MetadataEntry{Version: "9d7ba42"}, true},
		{"legacy version mismatch", install.MetadataEntry{Version: "1234567"}, false},
		{"non-hash version", install.MetadataEntry{Version: "v1.2.3"}, false},
		{"nothing recorded", install.MetadataEntry{}, false},
	}
	for _, tt := range tests {
		if got := recordedCommitMatches(&tt.meta, full); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRepoGroupIsCurrent(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "c1")
	head := run("rev-parse", "HEAD")
	url := "file://" + repo

	installed := t.TempDir()
	if err := os.WriteFile(filepath.Join(installed, "SKILL.md"), []byte("# a"), 0644); err != nil {
		t.Fatal(err)
	}
	current := []updateTarget{{name: "a", path: installed, meta: &install.MetadataEntry{Commit: head}}}
	if !repoGroupIsCurrent(url, "", current) || !repoGroupIsCurrent(url, "main", current) {
		t.Fatal("expected group at remote HEAD to be current")
	}
	stale := append(current, updateTarget{name: "b", path: installed, meta: &install.MetadataEntry{Commit: strings.Repeat("a", 40)}})
	if repoGroupIsCurrent(url, "", stale) {
		t.Fatal("one stale target must force a fetch")
	}
	unrecorded := []updateTarget{{name: "c", path: installed, meta: &install.MetadataEntry{}}}
	deleted := []updateTarget{{name: "d", path: filepath.Join(installed, "gone"), meta: &install.MetadataEntry{Commit: head}}}
	if repoGroupIsCurrent(url, "", deleted) {
		t.Fatal("a deleted install must force a fetch even when its metadata is current")
	}
	if repoGroupIsCurrent(url, "", unrecorded) {
		t.Fatal("a target without a recorded commit must force a fetch")
	}
	if repoGroupIsCurrent(url, "no-such-branch", current) {
		t.Fatal("a missing ref must force a fetch")
	}
	// A skill pinned to a full commit never moves; an abbreviated pin cannot
	// be compared with a recorded full SHA, so it fetches.
	if !repoGroupIsCurrent(url, head, current) {
		t.Fatal("a full-SHA pin matching the recorded commit must be current")
	}
	if repoGroupIsCurrent(url, head[:7], current) {
		t.Fatal("an abbreviated pin must force a fetch")
	}
}
