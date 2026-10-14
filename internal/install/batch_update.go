package install

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"skillshare/internal/utils"
)

// BatchUpdateProgressPrefix marks per-skill grouped update progress messages
// emitted through InstallOptions.OnProgress.
const BatchUpdateProgressPrefix = "skillshare:batch-update:"

// BatchUpdateResult holds results for a batch of skills from the same repository
type BatchUpdateResult struct {
	RepoURL    string
	CommitHash string
	Results    map[string]*InstallResult // map[skillRelPath]result
	Errors     map[string]error          // map[skillRelPath]error
	// Current lists subdirs skipped because the installed copy already matches
	// the fetched repo state.
	Current map[string]bool
}

// RepoUpdate is a repository fetched for a batch update but not yet applied.
// PrepareRepoUpdate only touches a temp dir, so callers may prepare several
// repos concurrently; Apply writes the source dir and metadata and must not run
// concurrently with another Apply. Close removes the temp clone.
type RepoUpdate struct {
	RepoURL      string
	CommitHash   string
	discovery    *DiscoveryResult
	skillTargets map[string]string
	opts         InstallOptions
}

// UpdateSkillsFromRepo updates multiple skills that belong to the same git repository.
// It clones the repository once to a temporary directory and then installs/updates
// each requested skill from that local copy.
//
// skillTargets maps repo-internal subdir (meta.Subdir) → local absolute destination path.
// This avoids assuming the local path mirrors the repo structure.
// branch is the branch the skills were installed from (empty = remote default).
func UpdateSkillsFromRepo(repoURL, branch string, skillTargets map[string]string, opts InstallOptions) (*BatchUpdateResult, error) {
	update, err := PrepareRepoUpdate(repoURL, branch, skillTargets, opts)
	if err != nil {
		return nil, err
	}
	defer update.Close()
	return update.Apply(opts.OnProgress), nil
}

// PrepareRepoUpdate clones repoURL once for the skills in skillTargets. When
// every target is a subdir it uses a sparse checkout of just those subdirs and
// falls back to a full clone if that fails. opts.OnProgress receives clone
// progress.
func PrepareRepoUpdate(repoURL, branch string, skillTargets map[string]string, opts InstallOptions) (*RepoUpdate, error) {
	if repoURL == "" {
		return nil, fmt.Errorf("repoURL is required")
	}

	source, err := ParseSource(repoURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repo URL %q: %w", repoURL, err)
	}
	source.Branch = branch

	discovery, err := discoverForBatchUpdate(source, skillTargets, opts.OnProgress)
	if err != nil {
		return nil, err
	}

	update := &RepoUpdate{
		RepoURL:      repoURL,
		CommitHash:   discovery.CommitHash,
		discovery:    discovery,
		skillTargets: skillTargets,
		opts:         opts,
	}
	if update.CommitHash == "" {
		if hash, hashErr := getGitCommit(update.repoPath()); hashErr == nil {
			update.CommitHash = hash
		}
	}
	return update, nil
}

// discoverForBatchUpdate clones only the requested skill subdirs when it can.
// A root skill ("" or ".") needs the whole tree, so it keeps the full clone.
func discoverForBatchUpdate(source *Source, skillTargets map[string]string, onProgress ProgressCallback) (*DiscoveryResult, error) {
	if subdirs, ok := sparseBatchSubdirs(skillTargets); ok && gitSupportsSparseCheckout() {
		sparseSource := *source
		if discovery, err := discoverFromGitPaths(&sparseSource, subdirs, onProgress); err == nil {
			*source = sparseSource
			return discovery, nil
		}
	}
	return DiscoverFromGitWithProgress(source, onProgress)
}

// sparseBatchSubdirs returns the cleaned, sorted subdirs of skillTargets, or
// false when any target is the repo root or escapes it.
func sparseBatchSubdirs(skillTargets map[string]string) ([]string, bool) {
	if len(skillTargets) == 0 {
		return nil, false
	}
	subdirs := make([]string, 0, len(skillTargets))
	for subdir := range skillTargets {
		cleaned := path.Clean(strings.ReplaceAll(strings.TrimSpace(subdir), "\\", "/"))
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
			return nil, false
		}
		subdirs = append(subdirs, cleaned)
	}
	sort.Strings(subdirs)
	return subdirs, true
}

func (u *RepoUpdate) repoPath() string {
	return filepath.Join(u.discovery.RepoPath, "repo")
}

// Close removes the temporary clone. It is safe to call more than once.
func (u *RepoUpdate) Close() {
	if u == nil || u.discovery == nil {
		return
	}
	CleanupDiscovery(u.discovery)
	u.discovery = nil
}

// Apply installs each requested skill from the fetched repo. onProgress
// receives one BatchUpdateProgressPrefix line per skill.
func (u *RepoUpdate) Apply(onProgress ProgressCallback) *BatchUpdateResult {
	opts := u.opts
	opts.OnProgress = onProgress
	discovery := u.discovery
	skillTargets := u.skillTargets

	result := &BatchUpdateResult{
		RepoURL:    u.RepoURL,
		CommitHash: u.CommitHash,
		Results:    make(map[string]*InstallResult),
		Errors:     make(map[string]error),
		Current:    make(map[string]bool),
	}
	repoPath := u.repoPath()
	fullHash, _ := getGitFullHash(repoPath)

	// Create a map for quick lookup in discovery
	discoveryMap := make(map[string]SkillInfo)
	for _, s := range discovery.Skills {
		discoveryMap[s.Path] = s
	}
	treeHashCache := make(map[string]string)
	// Read-only snapshot: each skill's own entry is read before its install
	// rewrites the store, so later writes cannot invalidate it.
	var store *MetadataStore
	if opts.SourceDir != "" {
		store, _ = loadMetadataFile(opts.SourceDir)
	}
	totalSkills := len(skillTargets)
	processedSkills := 0

	// 2. Install/Update each requested skill from the discovered repo
	for repoSubdir, destPath := range skillTargets {
		lookupKey := repoSubdir
		if lookupKey == "" {
			lookupKey = "."
		}
		skillInfo, ok := discoveryMap[lookupKey]
		if !ok {
			// Discovery skips target dot-dirs (.claude, .codex, ...), so a skill
			// installed from such a subdir never appears in the map even though it
			// still exists upstream. Resolve the subdir directly before declaring
			// the path deleted, otherwise the skill is misreported as stale.
			skillInfo, ok = lookupSkillSubdir(repoPath, lookupKey)
		}
		if !ok {
			result.Errors[repoSubdir] = fmt.Errorf("skill path %q not found in repository", repoSubdir)
			processedSkills++
			emitBatchSkillProgress(opts.OnProgress, processedSkills, totalSkills, repoSubdir)
			continue
		}
		// Fast path for update --all on huge inventories:
		// if installed metadata already points at this exact repo commit/tree,
		// skip reinstall/copy entirely.
		// Metadata outlives a deleted install, so require the skill on disk.
		if opts.Update && !opts.DryRun && !opts.Reinstall && skillFileExists(destPath) {
			installed := installedSkillMeta(store, opts.SourceDir, destPath)
			if isSkillCurrentAtRepoState(installed, repoSubdir, result.CommitHash, repoPath, treeHashCache) {
				// Best-effort metadata refresh:
				// when skipping by tree-hash match on a moved HEAD, keep meta.Version
				// aligned to latest commit to avoid stale version reporting.
				_ = refreshSkillMetaVersionIfNeeded(opts.SourceDir, destPath, installed, result.CommitHash, fullHash)
				result.Current[repoSubdir] = true
				processedSkills++
				emitBatchSkillProgress(opts.OnProgress, processedSkills, totalSkills, repoSubdir)
				continue
			}
		}

		// Use a local copy of options to avoid side effects
		skillOpts := opts
		skillOpts.OnProgress = nil // Progress handled by clone stage

		installResult, err := InstallFromDiscovery(discovery, skillInfo, destPath, skillOpts)
		if err != nil {
			result.Errors[repoSubdir] = err
			processedSkills++
			emitBatchSkillProgress(opts.OnProgress, processedSkills, totalSkills, repoSubdir)
			continue
		}
		result.Results[repoSubdir] = installResult
		processedSkills++
		emitBatchSkillProgress(opts.OnProgress, processedSkills, totalSkills, repoSubdir)
	}

	return result
}

// lookupSkillSubdir resolves a skill subdir that discovery filtered out by
// checking the cloned repo directly for its SKILL.md. Mirrors the direct-stat
// fast path used by single-skill updates (resolveSubdir).
func lookupSkillSubdir(repoPath, subdir string) (SkillInfo, bool) {
	if repoPath == "" || subdir == "" || subdir == "." {
		return SkillInfo{}, false
	}
	cleaned := path.Clean(strings.ReplaceAll(subdir, "\\", "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return SkillInfo{}, false
	}
	skillFile := filepath.Join(repoPath, filepath.FromSlash(cleaned), "SKILL.md")
	info, err := os.Stat(skillFile)
	if err != nil || info.IsDir() {
		return SkillInfo{}, false
	}
	fm := utils.ParseFrontmatterFields(skillFile, []string{"description", "license"})
	return SkillInfo{
		Name:        path.Base(cleaned),
		Path:        cleaned,
		License:     fm["license"],
		Description: fm["description"],
	}, true
}

// skillFileExists reports whether dir holds an installed SKILL.md.
func skillFileExists(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && !info.IsDir()
}

// installedMeta is the recorded state of an installed skill, read from the
// centralized store or, for installs that predate it, the legacy sidecar.
type installedMeta struct {
	Version  string
	TreeHash string
	storeKey string // store entry key; empty when read from a sidecar
}

// installedSkillMeta looks up destPath's metadata. store may be nil.
func installedSkillMeta(store *MetadataStore, sourceDir, destPath string) *installedMeta {
	if store != nil && sourceDir != "" {
		if rel, err := filepath.Rel(sourceDir, destPath); err == nil {
			rel = filepath.ToSlash(rel)
			if entry := store.GetByPath(rel); entry != nil {
				return &installedMeta{Version: entry.Version, TreeHash: entry.TreeHash, storeKey: storeKeyFor(store, rel, entry)}
			}
		}
	}
	meta, err := ReadMeta(destPath)
	if err != nil || meta == nil {
		return nil
	}
	return &installedMeta{Version: meta.Version, TreeHash: meta.TreeHash}
}

// storeKeyFor returns the key GetByPath(rel) resolved entry under.
func storeKeyFor(store *MetadataStore, rel string, entry *MetadataEntry) string {
	if store.Entries[rel] == entry {
		return rel
	}
	return path.Base(rel)
}

// isSkillCurrentAtRepoState returns true when installed metadata already
// matches the latest fetched repo state for this skill path.
func isSkillCurrentAtRepoState(meta *installedMeta, repoSubdir, commitHash, repoPath string, treeHashCache map[string]string) bool {
	if commitHash == "" || repoPath == "" || meta == nil {
		return false
	}

	subdir := strings.TrimSpace(repoSubdir)
	metaVersion := strings.TrimSpace(meta.Version)
	commitHash = strings.TrimSpace(commitHash)

	// Fast path: exact repo commit match means installed content is already current.
	// This avoids per-skill git subtree lookups on very large batches.
	if metaVersion != "" && metaVersion == commitHash {
		return true
	}

	if subdir == "" || subdir == "." {
		// Root installs don't have a stable tree hash field; require commit match.
		return false
	}
	if strings.TrimSpace(meta.TreeHash) == "" {
		return false
	}

	treeHash, ok := treeHashCache[subdir]
	if !ok {
		treeHash = getSubdirTreeHash(repoPath, subdir)
		treeHashCache[subdir] = treeHash
	}
	return treeHash != "" && meta.TreeHash == treeHash
}

func emitBatchSkillProgress(onProgress ProgressCallback, done, total int, repoSubdir string) {
	if onProgress == nil || total <= 0 {
		return
	}
	label := strings.TrimSpace(repoSubdir)
	if label == "" {
		label = "."
	}
	onProgress(fmt.Sprintf("%s%d/%d %s", BatchUpdateProgressPrefix, done, total, label))
}

// refreshSkillMetaVersionIfNeeded records commitHash (and fullHash, when
// known) on a skill skipped as current, so a tree-hash match on a moved HEAD
// does not leave a stale version behind.
func refreshSkillMetaVersionIfNeeded(sourceDir, destPath string, installed *installedMeta, commitHash, fullHash string) error {
	commitHash = strings.TrimSpace(commitHash)
	if commitHash == "" || installed == nil {
		return nil
	}
	if installed.storeKey == "" {
		meta, err := ReadMeta(destPath)
		if err != nil || meta == nil {
			return err
		}
		if strings.TrimSpace(meta.Version) == commitHash {
			return nil
		}
		meta.Version = commitHash
		return WriteMeta(destPath, meta)
	}

	store, err := loadMetadataFile(sourceDir)
	if err != nil {
		return err
	}
	entry := store.Get(installed.storeKey)
	if entry == nil {
		return nil
	}
	if strings.TrimSpace(entry.Version) == commitHash && (fullHash == "" || entry.Commit == fullHash) {
		return nil
	}
	entry.Version = commitHash
	if fullHash != "" {
		entry.Commit = fullHash
	}
	return store.Save(sourceDir)
}
