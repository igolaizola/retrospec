package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/igolaizola/retrospec/internal/run"
)

func main() {
	var cfg run.Config

	flag.StringVar(&cfg.Repo, "repo", "", "Git repository URL or local path")
	flag.StringVar(&cfg.Commit, "commit", "", "Target commit SHA")
	flag.StringVar(&cfg.Workdir, "workdir", "./work", "Working directory for clones, runs, and artifacts")
	flag.IntVar(&cfg.MaxIters, "max-iters", 8, "Maximum optimization iterations")
	flag.Float64Var(&cfg.Threshold, "threshold", 0.9, "Stop when final score reaches this threshold")
	flag.IntVar(&cfg.TimeoutSeconds, "timeout-seconds", 600, "Per-iteration timeout for Copilot coder run")
	flag.BoolVar(&cfg.KeepRuns, "keep-runs", false, "Keep per-iteration worktrees")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose logs")
	flag.Float64Var(&cfg.Alpha, "alpha", 0.75, "Weight on technical similarity vs realism")
	flag.IntVar(&cfg.MaxPathRefs, "max-path-refs", 3, "Max path references encouraged in spec prompt")
	flag.IntVar(&cfg.MaxIdentifiers, "max-identifiers", 25, "Heuristic threshold for identifier density in candidate prompt")
	flag.IntVar(&cfg.MaxLength, "max-length", 0, "Maximum candidate prompt length (0 = unlimited)")
	flag.IntVar(&cfg.CandidatesPerIter, "candidates-per-iter", 3, "How many spec candidates to generate each iteration")
	flag.IntVar(&cfg.CoderRunsPerIter, "coder-runs-per-iter", 2, "How many top candidates to execute with coder each iteration")
	flag.StringVar(&cfg.Model, "model", "", "Optional Copilot model override for all sessions (otherwise COPILOT_MODEL/env default)")
	flag.Parse()

	if cfg.Repo == "" || cfg.Commit == "" {
		fmt.Fprintln(os.Stderr, "error: --repo and --commit are required")
		flag.Usage()
		os.Exit(2)
	}

	absWorkdir, err := filepath.Abs(cfg.Workdir)
	if err != nil {
		log.Fatalf("resolve workdir: %v", err)
	}
	cfg.Workdir = absWorkdir

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid flags: %v", err)
	}

	ctx := context.Background()
	runner := run.NewRunner(cfg)
	result, err := runner.Execute(ctx)
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}

	fmt.Printf("best iteration: %d\n", result.BestIteration)
	fmt.Printf("tech similarity: %.4f\n", result.BestTechSimilarity)
	fmt.Printf("realism score: %.4f\n", result.BestRealism)
	fmt.Printf("final score: %.4f\n", result.BestFinalScore)
	fmt.Printf("artifacts: %s\n", filepath.Join(cfg.Workdir, "artifacts"))
	fmt.Printf("completed at: %s\n", time.Now().Format(time.RFC3339))
}
