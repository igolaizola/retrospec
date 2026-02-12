package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	hostPathRepoRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?(?:\.git)?$`)
	ownerRepoShortRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`)
)

type FileStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

type DiffSnapshot struct {
	Patch        string              `json:"patch"`
	ChangedFiles []string            `json:"changedFiles"`
	FileStats    map[string]FileStat `json:"fileStats"`
}

type CommitInfo struct {
	TargetSHA     string `json:"targetSHA"`
	ParentSHA     string `json:"parentSHA"`
	CommitMessage string `json:"commitMessage"`
}

func PrepareBaseRepo(ctx context.Context, repoArg, workdir string) (string, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", fmt.Errorf("create workdir: %w", err)
	}
	base := filepath.Join(workdir, "base")
	if _, err := os.Stat(base); err == nil {
		if err := os.RemoveAll(base); err != nil {
			return "", fmt.Errorf("remove existing base repo: %w", err)
		}
	}

	localSourcePath := detectLocalSourcePath(repoArg)
	cloneSource, err := resolveCloneSource(repoArg)
	if err != nil {
		return "", err
	}

	if _, err := runCmd(ctx, "", "git", "clone", "--no-hardlinks", cloneSource, base); err != nil {
		return "", err
	}

	if localSourcePath != "" {
		if upstreamURL, err := readOriginRemoteURL(ctx, localSourcePath); err == nil && strings.TrimSpace(upstreamURL) != "" {
			_, _ = runCmd(ctx, base, "git", "remote", "set-url", "origin", strings.TrimSpace(upstreamURL))
		}
	}

	return base, nil
}

func detectLocalSourcePath(repoArg string) string {
	repoArg = strings.TrimSpace(repoArg)
	if local, ok := existingLocalPath(repoArg); ok {
		return local
	}
	if strings.HasPrefix(repoArg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			expanded := filepath.Join(home, strings.TrimPrefix(repoArg, "~/"))
			if local, ok := existingLocalPath(expanded); ok {
				return local
			}
		}
	}
	return ""
}

func readOriginRemoteURL(ctx context.Context, repoPath string) (string, error) {
	out, err := runCmd(ctx, repoPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func resolveCloneSource(repoArg string) (string, error) {
	repoArg = strings.TrimSpace(repoArg)
	if repoArg == "" {
		return "", fmt.Errorf("empty repository argument")
	}

	if local, ok := existingLocalPath(repoArg); ok {
		return local, nil
	}

	if strings.HasPrefix(repoArg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			expanded := filepath.Join(home, strings.TrimPrefix(repoArg, "~/"))
			if local, ok := existingLocalPath(expanded); ok {
				return local, nil
			}
		}
	}

	if isLikelyURL(repoArg) {
		return repoArg, nil
	}

	if strings.HasPrefix(repoArg, "github.com/") || strings.HasPrefix(repoArg, "gitlab.com/") || strings.HasPrefix(repoArg, "bitbucket.org/") {
		return "https://" + repoArg, nil
	}

	if ownerRepoShortRe.MatchString(repoArg) {
		return "https://github.com/" + repoArg, nil
	}

	if hostPathRepoRe.MatchString(repoArg) {
		return "https://" + repoArg, nil
	}

	return "", fmt.Errorf("repository path not found locally and not recognized as URL: %s", repoArg)
}

func existingLocalPath(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false
	}
	return abs, true
}

func ResolveCommitInfo(ctx context.Context, repoPath, targetCommit string) (CommitInfo, error) {
	if err := EnsureCommitAvailable(ctx, repoPath, targetCommit); err != nil {
		return CommitInfo{}, err
	}

	target, err := runCmd(ctx, repoPath, "git", "rev-parse", strings.TrimSpace(targetCommit))
	if err != nil {
		return CommitInfo{}, err
	}
	parent, err := runCmd(ctx, repoPath, "git", "rev-parse", strings.TrimSpace(targetCommit)+"^")
	if err != nil {
		return CommitInfo{}, fmt.Errorf("resolve parent commit (target must have a parent): %w", err)
	}
	msg, err := runCmd(ctx, repoPath, "git", "show", "-s", "--format=%s%n%b", strings.TrimSpace(targetCommit))
	if err != nil {
		return CommitInfo{}, err
	}

	return CommitInfo{
		TargetSHA:     strings.TrimSpace(target),
		ParentSHA:     strings.TrimSpace(parent),
		CommitMessage: strings.TrimSpace(msg),
	}, nil
}

func EnsureCommitAvailable(ctx context.Context, repoPath, commit string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return fmt.Errorf("empty commit")
	}
	if _, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", commit+"^{commit}"); err == nil {
		return nil
	}
	if _, err := runCmd(ctx, repoPath, "git", "fetch", "--no-tags", "origin", commit); err == nil {
		if _, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", commit+"^{commit}"); err == nil {
			return nil
		}
	}

	// Fallback for remotes that do not allow SHA fetches.
	_, _ = runCmd(ctx, repoPath, "git", "fetch", "--tags", "origin")
	_, _ = runCmd(ctx, repoPath, "git", "fetch", "--no-tags", "origin", "+refs/heads/*:refs/remotes/origin/*")
	if _, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", commit+"^{commit}"); err == nil {
		return nil
	}

	if shallow, _ := isShallowRepo(ctx, repoPath); shallow {
		_, _ = runCmd(ctx, repoPath, "git", "fetch", "--unshallow", "origin")
		if _, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", commit+"^{commit}"); err == nil {
			return nil
		}
	}

	if _, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", commit+"^{commit}"); err != nil {
		return fmt.Errorf("target commit not available after fetch: %w", err)
	}
	return nil
}

func isShallowRepo(ctx context.Context, repoPath string) (bool, error) {
	out, err := runCmd(ctx, repoPath, "git", "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

func SnapshotBetween(ctx context.Context, repoPath, fromRev, toRev string) (DiffSnapshot, error) {
	patch, err := runCmd(ctx, repoPath, "git", "diff", "--no-color", "--find-renames", fromRev, toRev)
	if err != nil {
		return DiffSnapshot{}, err
	}
	filesOut, err := runCmd(ctx, repoPath, "git", "diff", "--name-only", fromRev, toRev)
	if err != nil {
		return DiffSnapshot{}, err
	}
	numstatOut, err := runCmd(ctx, repoPath, "git", "diff", "--numstat", fromRev, toRev)
	if err != nil {
		return DiffSnapshot{}, err
	}

	return DiffSnapshot{
		Patch:        patch,
		ChangedFiles: parseLines(filesOut),
		FileStats:    parseNumstat(numstatOut),
	}, nil
}

func SnapshotWorktree(ctx context.Context, repoPath string) (DiffSnapshot, error) {
	patch, err := runCmd(ctx, repoPath, "git", "diff", "--no-color", "--find-renames")
	if err != nil {
		return DiffSnapshot{}, err
	}
	filesOut, err := runCmd(ctx, repoPath, "git", "diff", "--name-only")
	if err != nil {
		return DiffSnapshot{}, err
	}
	numstatOut, err := runCmd(ctx, repoPath, "git", "diff", "--numstat")
	if err != nil {
		return DiffSnapshot{}, err
	}

	return DiffSnapshot{
		Patch:        patch,
		ChangedFiles: parseLines(filesOut),
		FileStats:    parseNumstat(numstatOut),
	}, nil
}

func CreateWorktree(ctx context.Context, baseRepoPath, runPath, commit string) error {
	// Best-effort cleanup for stale registrations from previous runs.
	_, _ = runCmd(ctx, baseRepoPath, "git", "worktree", "remove", "--force", runPath)
	_, _ = runCmd(ctx, baseRepoPath, "git", "worktree", "prune")

	if err := os.RemoveAll(runPath); err != nil {
		return fmt.Errorf("clean worktree path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(runPath), 0o755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}
	_, err := runCmd(ctx, baseRepoPath, "git", "worktree", "add", "--detach", runPath, commit)
	return err
}

func RemoveWorktree(ctx context.Context, baseRepoPath, runPath string) error {
	_, err := runCmd(ctx, baseRepoPath, "git", "worktree", "remove", "--force", runPath)
	if err != nil {
		return err
	}
	_, _ = runCmd(ctx, baseRepoPath, "git", "worktree", "prune")
	return nil
}

func parseLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func parseNumstat(s string) map[string]FileStat {
	stats := map[string]FileStat{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		added := parseNum(parts[0])
		removed := parseNum(parts[1])
		path := strings.Join(parts[2:], "\t")
		stats[path] = FileStat{Path: path, Added: added, Removed: removed}
	}
	return stats
}

func parseNum(s string) int {
	if s == "-" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func isLikelyURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@")
}

func runCmd(ctx context.Context, dir, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s %s timed out", bin, strings.Join(args, " "))
		}
		return "", fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}
