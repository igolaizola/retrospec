package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type TestRunResult struct {
	Ran      bool   `json:"ran"`
	Passed   bool   `json:"passed"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

type testCmd struct {
	name string
	args []string
	gate string
}

func RunBestEffortTests(ctx context.Context, repoPath string, timeout time.Duration) TestRunResult {
	commands := []testCmd{
		{name: "go", args: []string{"test", "./..."}, gate: "go.mod"},
		{name: "npm", args: []string{"test"}, gate: "package.json"},
		{name: "cargo", args: []string{"test"}, gate: "Cargo.toml"},
	}

	runAny := false
	for _, tc := range commands {
		if _, err := os.Stat(filepath.Join(repoPath, tc.gate)); err != nil {
			continue
		}
		runAny = true
		res := runSingleTestCommand(ctx, repoPath, timeout, tc.name, tc.args...)
		if !res.Passed {
			return res
		}
	}

	if !runAny {
		return TestRunResult{Ran: false, Passed: true, Category: "not_run", Summary: "no recognized test command at repository root"}
	}
	return TestRunResult{Ran: true, Passed: true, Category: "pass", Summary: "best-effort root tests passed"}
}

func runSingleTestCommand(ctx context.Context, repoPath string, timeout time.Duration, cmdName string, args ...string) TestRunResult {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(tctx, cmdName, args...)
	cmd.Dir = repoPath
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	output := strings.ToLower(stdout.String() + "\n" + stderr.String())
	if tctx.Err() == context.DeadlineExceeded {
		return TestRunResult{Ran: true, Passed: false, Category: "timeout", Summary: fmt.Sprintf("%s timed out", cmdName)}
	}
	if err != nil {
		category := classifyTestFailure(output)
		return TestRunResult{
			Ran:      true,
			Passed:   false,
			Category: category,
			Summary:  fmt.Sprintf("%s failed (%s)", cmdName, category),
		}
	}

	return TestRunResult{Ran: true, Passed: true, Category: "pass", Summary: fmt.Sprintf("%s passed", cmdName)}
}

func classifyTestFailure(output string) string {
	switch {
	case strings.Contains(output, "compile") || strings.Contains(output, "build failed") || strings.Contains(output, "syntax error"):
		return "compilation"
	case strings.Contains(output, "assert") || strings.Contains(output, "expected") || strings.Contains(output, "failed") || strings.Contains(output, "panic"):
		return "unit-test"
	default:
		return "test-failure"
	}
}
