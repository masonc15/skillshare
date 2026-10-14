package install

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// gitlink is a submodule entry in a repo's tree. skillshare does not fetch
// submodules, so its directory is empty after any clone.
type gitlink struct {
	Path   string
	Commit string
	URL    string // from .gitmodules; "" when not recorded
}

// String quotes the path and URL with %q: both come from an untrusted repo
// and may hold newlines or terminal escape sequences.
func (l gitlink) String() string {
	s := fmt.Sprintf("%q (pinned at %s", l.Path, shortHash(l.Commit))
	if l.URL != "" {
		s += fmt.Sprintf(" from %q", displayURL(l.URL))
	}
	return s + ")"
}

// displayURL drops userinfo, query and fragment from a .gitmodules URL, which
// may carry a token, before it reaches terminal output or an API error. git
// accepts any text there, so this fails closed on the raw string: everything
// before the last "@" goes, keeping only a well-formed "scheme://" prefix.
func displayURL(raw string) string {
	// Userinfo first: a "?" or "#" inside it must not hide the "@".
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		if scheme, _, ok := strings.Cut(raw[:at], "://"); ok {
			raw = scheme + "://" + raw[at+1:]
		} else {
			raw = raw[at+1:]
		}
	}
	raw, _, _ = strings.Cut(raw, "#")
	raw, _, _ = strings.Cut(raw, "?")
	return raw
}

// repoGitlinks lists the submodules recorded in HEAD of repoPath. extraEnv
// authenticates the lazy .gitmodules fetch in a partial clone. Only listing the
// tree can fail; a missing or unreadable .gitmodules just leaves URLs empty.
func repoGitlinks(repoPath string, extraEnv []string) ([]gitlink, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()

	cmd := gitCommand(ctx, "ls-tree", "-r", "-z", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list submodules: %w", err)
	}
	var links []gitlink
	for _, entry := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && len(fields) == 3 && fields[0] == "160000" {
			links = append(links, gitlink{Path: p, Commit: fields[2]})
		}
	}
	if len(links) == 0 {
		return nil, nil
	}

	cmd = gitCommand(ctx, "config", "-z", "--blob", "HEAD:.gitmodules", "--get-regexp", `^submodule\..*\.(path|url)$`)
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err = cmd.Output()
	if err != nil {
		return links, nil
	}
	paths, urls := map[string]string{}, map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		key, val, _ := strings.Cut(entry, "\n")
		if name, ok := strings.CutSuffix(key, ".path"); ok {
			paths[name] = val
		} else if name, ok := strings.CutSuffix(key, ".url"); ok {
			urls[name] = val
		}
	}
	for name, p := range paths {
		for i := range links {
			if links[i].Path == p {
				links[i].URL = urls[name]
			}
		}
	}
	return links, nil
}

// submoduleError refuses a subdir that is a submodule or lies inside one,
// which would otherwise install an empty directory or report a missing path.
// Each ancestor of subdir is matched by name, or by filesystem identity so a
// spelling the filesystem folds to the gitlink's directory (case on Windows
// and macOS, Unicode normalization on APFS) counts too.
func submoduleError(repoPath, subdir string, extraEnv []string) error {
	subdir = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(subdir)), "/")
	parts := strings.Split(subdir, "/")
	links, err := repoGitlinks(repoPath, extraEnv)
	if err != nil {
		// Fail closed: an unchecked path may be an empty submodule directory.
		return fmt.Errorf("check %q for git submodules: %w", subdir, err)
	}
	for _, l := range links {
		linkDir := filepath.Join(repoPath, filepath.FromSlash(l.Path))
		for i := range parts {
			ancestor := strings.Join(parts[:i+1], "/")
			if ancestor == l.Path || sameDir(filepath.Join(repoPath, filepath.FromSlash(ancestor)), linkDir) {
				return fmt.Errorf("%q is in git submodule %s, and skillshare does not fetch submodules; install from that repository instead, or copy the files into this one", subdir, l)
			}
		}
	}
	return nil
}

func sameDir(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

// SubmoduleWarnings names each submodule whose contents a whole-repo install
// skips. It is best effort: a failed listing only loses the warnings.
func SubmoduleWarnings(repoPath string, extraEnv []string) []string {
	links, _ := repoGitlinks(repoPath, extraEnv)
	var warnings []string
	for _, l := range links {
		warnings = append(warnings, fmt.Sprintf("skipped git submodule %s: skillshare does not fetch submodules", l))
	}
	return warnings
}
