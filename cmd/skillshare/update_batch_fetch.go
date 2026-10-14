package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"skillshare/internal/git"
	"skillshare/internal/install"
)

// updateFetchWorkers bounds how many repositories a batch update fetches at
// once. Fetching is network-bound; applying stays sequential.
const updateFetchWorkers = 8

// repoGroupJob is one repository's share of a batch update.
type repoGroupJob struct {
	repoURL        string
	branch         string
	targets        []updateTarget
	skillTargetMap map[string]string       // repo subdir → install path
	pathToTarget   map[string]updateTarget // repo subdir → target

	// Set by fetchRepoGroups.
	upToDate bool // remote ref matches every target's recorded commit; nothing fetched
	update   *install.RepoUpdate
	err      error
}

// newRepoGroupJob builds the job for a group key and returns targets skipped
// for lacking metadata.
func newRepoGroupJob(groupKey string, targets []updateTarget) (*repoGroupJob, []updateTarget) {
	repoURL, branch := splitURLBranch(groupKey)
	job := &repoGroupJob{
		repoURL:        repoURL,
		branch:         branch,
		skillTargetMap: make(map[string]string),
		pathToTarget:   make(map[string]updateTarget),
	}
	var missing []updateTarget
	for _, t := range targets {
		if t.meta == nil {
			missing = append(missing, t)
			continue
		}
		subdir := t.meta.Subdir
		if subdir == "" {
			subdir = "."
		}
		job.targets = append(job.targets, t)
		job.skillTargetMap[subdir] = t.path
		job.pathToTarget[subdir] = t
	}
	return job, missing
}

// fetchRepoGroups fetches jobs on up to updateFetchWorkers goroutines and
// sends each finished job on the returned channel, which closes after the
// last one. The channel is unbuffered, so at most updateFetchWorkers clones
// wait on disk for the consumer. With skipCurrent, a job whose remote ref
// still matches every recorded commit is marked upToDate without cloning.
// opts.OnProgress is ignored: progress callbacks would race across workers.
func fetchRepoGroups(jobs []*repoGroupJob, opts install.InstallOptions, skipCurrent bool) <-chan *repoGroupJob {
	opts.OnProgress = nil
	out := make(chan *repoGroupJob)
	queue := make(chan *repoGroupJob)

	workers := min(updateFetchWorkers, len(jobs))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				switch {
				case len(job.skillTargetMap) == 0:
					// Every target lacked metadata; nothing to fetch.
				case skipCurrent && repoGroupIsCurrent(job.repoURL, job.branch, job.targets):
					job.upToDate = true
				default:
					job.update, job.err = install.PrepareRepoUpdate(job.repoURL, job.branch, job.skillTargetMap, opts)
				}
				out <- job
			}
		}()
	}
	go func() {
		for _, job := range jobs {
			queue <- job
		}
		close(queue)
		wg.Wait()
		close(out)
	}()
	return out
}

// repoGroupIsCurrent reports whether the remote ref still points at the commit
// every target was installed from, so the clone can be skipped. Any lookup
// failure returns false and the caller clones as before.
func repoGroupIsCurrent(repoURL, branch string, targets []updateTarget) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t.meta == nil || (t.meta.Commit == "" && !install.IsCommitSHA(t.meta.Version)) {
			return false
		}
		// Metadata outlives a deleted install; let the fetch restore it.
		if info, err := os.Stat(filepath.Join(t.path, "SKILL.md")); err != nil || info.IsDir() {
			return false
		}
	}
	remote, err := git.GetRemoteRefCommitWithAuth(repoURL, branch)
	if err != nil {
		return false
	}
	for _, t := range targets {
		if !recordedCommitMatches(t.meta, remote) {
			return false
		}
	}
	return true
}

// recordedCommitMatches compares a full remote SHA with the commit recorded at
// install time, falling back to the abbreviated version for entries that
// predate the commit field.
func recordedCommitMatches(meta *install.MetadataEntry, remote string) bool {
	remote = strings.ToLower(strings.TrimSpace(remote))
	if remote == "" {
		return false
	}
	if commit := strings.ToLower(strings.TrimSpace(meta.Commit)); commit != "" {
		return commit == remote
	}
	version := strings.ToLower(strings.TrimSpace(meta.Version))
	return install.IsCommitSHA(version) && strings.HasPrefix(remote, version)
}
