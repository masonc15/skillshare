package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFetchFailedEntry_RedactsRepoURL(t *testing.T) {
	tests := []struct {
		name, repoURL, wantName string
		leaks                   []string
	}{
		{"userinfo", "https://someone:s3cret-token@git.example.com/owner/skills.git", "https://git.example.com/owner/skills.git", []string{"s3cret-token", "someone"}},
		{"token as username", "https://s3cret-token@git.example.com/owner/skills.git", "https://git.example.com/owner/skills.git", []string{"s3cret-token"}},
		{"query token", "https://git.example.com/owner/skills?token=s3cret-token.git", "https://git.example.com/owner/skills", []string{"s3cret-token"}},
		{"fragment", "https://git.example.com/owner/skills.git#s3cret-token", "https://git.example.com/owner/skills.git", []string{"s3cret-token"}},
		{"question mark in password", "https://someone:s3cret?token@git.example.com/owner/skills.git", "https://git.example.com/owner/skills.git", []string{"s3cret?token", "someone"}},
		{"at sign in https path", "https://git.example.com/team@archive/skills.git", "https://git.example.com/team@archive/skills.git", nil},
		{"at sign in file path", "file:///tmp/team@archive.git", "file:///tmp/team@archive.git", nil},
		{"scp style", "git@github.com:owner/skills.git", "git@github.com:owner/skills.git", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New("fatal: unable to access '" + tt.repoURL + "/': Failed to connect")
			got := fetchFailedEntry(tt.repoURL, err)
			if got.name != tt.wantName {
				t.Errorf("name = %q, want %q", got.name, tt.wantName)
			}
			for _, leak := range tt.leaks {
				if strings.Contains(got.name, leak) || strings.Contains(got.errMsg, leak) {
					t.Errorf("leaked %q: name=%q errMsg=%q", leak, got.name, got.errMsg)
				}
			}
			if !strings.Contains(got.errMsg, "Failed to connect") {
				t.Errorf("errMsg lost the git error: %q", got.errMsg)
			}
		})
	}
}
