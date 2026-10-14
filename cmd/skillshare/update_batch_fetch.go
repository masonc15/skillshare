package main

import (
	"sync"

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
	update *install.RepoUpdate
	err    error
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
// wait on disk for the consumer. opts.OnProgress is ignored: progress
// callbacks would race across workers.
func fetchRepoGroups(jobs []*repoGroupJob, opts install.InstallOptions) <-chan *repoGroupJob {
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
