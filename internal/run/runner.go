package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/igolaizola/retrospec/internal/copilot"
	"github.com/igolaizola/retrospec/internal/feedback"
	"github.com/igolaizola/retrospec/internal/git"
	"github.com/igolaizola/retrospec/internal/scoring"
	sdk "github.com/github/copilot-sdk/go"
)

var trackerRefCleanupRe = regexp.MustCompile(`(?i)(?:^|\s)(?:#\d+|(?:issue|issues|pr|pull request|pull requests)\s*#?\d+)\b`) //nolint:lll

type Runner struct {
	cfg Config
}

type CandidateDraftLog struct {
	Index             int      `json:"index"`
	Style             string   `json:"style"`
	CandidatePrompt   string   `json:"candidatePrompt,omitempty"`
	Rationale         string   `json:"rationale,omitempty"`
	ScopeHints        []string `json:"scopeHints,omitempty"`
	ValidationRetries int      `json:"validationRetries,omitempty"`
	RawSpecResponse   string   `json:"rawSpecResponse,omitempty"`
	PreRealism        float64  `json:"preRealism,omitempty"`
	Novelty           float64  `json:"novelty,omitempty"`
	PreScore          float64  `json:"preScore,omitempty"`
	GenerationError   string   `json:"generationError,omitempty"`
}

type CoderAttemptLog struct {
	CandidateIndex    int                   `json:"candidateIndex"`
	CandidateStyle    string                `json:"candidateStyle"`
	CandidatePrompt   string                `json:"candidatePrompt"`
	CoderError        string                `json:"coderError,omitempty"`
	CoderFinalMessage string                `json:"coderFinalMessage,omitempty"`
	Tech              scoring.TechScore     `json:"tech"`
	Realism           scoring.RealismResult `json:"realism"`
	FinalScore        float64               `json:"finalScore"`
	TestResult        TestRunResult         `json:"testResult"`
	ProducedPatchPath string                `json:"producedPatchPath,omitempty"`
	ProducedFiles     []string              `json:"producedFiles,omitempty"`
}

type IterationLog struct {
	Iteration          int                 `json:"iteration"`
	Drafts             []CandidateDraftLog `json:"drafts"`
	CoderAttempts      []CoderAttemptLog   `json:"coderAttempts"`
	SelectedAttempt    int                 `json:"selectedAttempt"`
	FeedbackPacket     feedback.Packet     `json:"feedbackPacket"`
	IterationBestScore float64             `json:"iterationBestScore"`
}

type RunLog struct {
	Repo          string         `json:"repo"`
	TargetCommit  string         `json:"targetCommit"`
	ParentCommit  string         `json:"parentCommit"`
	Alpha         float64        `json:"alpha"`
	Threshold     float64        `json:"threshold"`
	MaxIters      int            `json:"maxIters"`
	BestIteration int            `json:"bestIteration"`
	Iterations    []IterationLog `json:"iterations"`
	StoppedReason string         `json:"stoppedReason"`
	CommitMessage string         `json:"commitMessage"`
	StartedAt     time.Time      `json:"startedAt"`
	CompletedAt   time.Time      `json:"completedAt"`
}

type Metrics struct {
	TechSimilarity float64 `json:"techSimilarity"`
	RealismScore   float64 `json:"realismScore"`
	FinalScore     float64 `json:"finalScore"`
	Alpha          float64 `json:"alpha"`
	BestIteration  int     `json:"bestIteration"`
}

type bestState struct {
	iteration int
	prompt    string
	patch     string
	tech      float64
	realism   float64
	final     float64
}

type candidateDraftRuntime struct {
	log       CandidateDraftLog
	candidate copilot.SpecCandidate
	valid     bool
}

type coderAttemptRuntime struct {
	log      CoderAttemptLog
	produced git.DiffSnapshot
}

func NewRunner(cfg Config) *Runner {
	return &Runner{cfg: cfg}
}

func (r *Runner) Execute(ctx context.Context) (Result, error) {
	start := time.Now()
	paths, err := r.ensureLayout()
	if err != nil {
		return Result{}, err
	}

	baseRepo, err := git.PrepareBaseRepo(ctx, r.cfg.Repo, r.cfg.Workdir)
	if err != nil {
		return Result{}, err
	}

	commitInfo, err := git.ResolveCommitInfo(ctx, baseRepo, r.cfg.Commit)
	if err != nil {
		return Result{}, err
	}

	target, err := git.SnapshotBetween(ctx, baseRepo, commitInfo.ParentSHA, commitInfo.TargetSHA)
	if err != nil {
		return Result{}, fmt.Errorf("collect target patch: %w", err)
	}
	if err := os.WriteFile(filepath.Join(paths.artifactsDir, "target.patch"), []byte(target.Patch), 0o644); err != nil {
		return Result{}, fmt.Errorf("write target.patch: %w", err)
	}

	manager, err := copilot.NewManager(ctx, r.cfg.Workdir, copilot.Options{Model: r.cfg.Model, Verbose: r.cfg.Verbose})
	if err != nil {
		return Result{}, err
	}
	defer manager.Close()

	specSession, err := manager.CreateSpecWriterSession(ctx, r.cfg.Workdir)
	if err != nil {
		return Result{}, err
	}
	defer specSession.Destroy()

	initialPacket := feedback.BuildInitialPacket(0, target, commitInfo.CommitMessage, r.cfg.MaxPathRefs)
	feedbackText := feedback.PacketText(initialPacket)
	objectiveAnchor := buildObjectiveAnchor(commitInfo.CommitMessage, target)

	runLog := RunLog{
		Repo:          r.cfg.Repo,
		TargetCommit:  commitInfo.TargetSHA,
		ParentCommit:  commitInfo.ParentSHA,
		Alpha:         r.cfg.Alpha,
		Threshold:     r.cfg.Threshold,
		MaxIters:      r.cfg.MaxIters,
		CommitMessage: commitInfo.CommitMessage,
		StartedAt:     start,
	}

	best := bestState{final: -1}
	stoppedReason := "max-iters reached"
	noImprovement := 0
	previousPrompt := ""
	previousOutcome := ""
	promptHistory := []string{}

	for iter := 1; iter <= r.cfg.MaxIters; iter++ {
		if r.cfg.Verbose {
			fmt.Printf("[iter %d] generating %d candidate prompts\n", iter, r.cfg.CandidatesPerIter)
		}

		specFeedback := objectiveAnchor + "\n\n" + feedbackText
		drafts, draftErr := r.generateCandidatePool(
			ctx,
			manager,
			specSession,
			iter,
			specFeedback,
			previousPrompt,
			previousOutcome,
			promptHistory,
			commitInfo.CommitMessage,
			target,
		)
		if draftErr != nil {
			return Result{}, fmt.Errorf("generate candidates for iteration %d: %w", iter, draftErr)
		}

		validDrafts := make([]candidateDraftRuntime, 0, len(drafts))
		draftLogs := make([]CandidateDraftLog, 0, len(drafts))
		for _, d := range drafts {
			draftLogs = append(draftLogs, d.log)
			if d.valid {
				validDrafts = append(validDrafts, d)
				promptHistory = append(promptHistory, d.candidate.CandidatePrompt)
			}
		}
		if len(validDrafts) == 0 {
			return Result{}, fmt.Errorf("all candidate generations failed in iteration %d", iter)
		}

		sort.Slice(validDrafts, func(i, j int) bool {
			return validDrafts[i].log.PreScore > validDrafts[j].log.PreScore
		})

		coderBudget := minInt(r.cfg.CoderRunsPerIter, len(validDrafts))
		attempts := make([]coderAttemptRuntime, 0, coderBudget)

		for rank := 0; rank < coderBudget; rank++ {
			draft := validDrafts[rank]
			runPath := filepath.Join(paths.runsDir, fmt.Sprintf("iter-%03d-cand-%02d", iter, rank+1))
			if err := git.CreateWorktree(ctx, baseRepo, runPath, commitInfo.ParentSHA); err != nil {
				return Result{}, fmt.Errorf("create worktree for iteration %d candidate %d: %w", iter, rank+1, err)
			}

			coderCtx, cancelCoder := context.WithTimeout(ctx, time.Duration(r.cfg.TimeoutSeconds)*time.Second)
			coderRes, coderErr := manager.RunCoder(coderCtx, runPath, draft.candidate.CandidatePrompt)
			cancelCoder()

			produced, snapErr := git.SnapshotWorktree(ctx, runPath)
			if snapErr != nil {
				if !r.cfg.KeepRuns {
					_ = git.RemoveWorktree(ctx, baseRepo, runPath)
				}
				return Result{}, fmt.Errorf("snapshot produced patch for iteration %d candidate %d: %w", iter, rank+1, snapErr)
			}

			tech := scoring.ScoreTechSimilarity(target, produced)
			realism := scoring.ScoreRealismHeuristic(draft.candidate.CandidatePrompt, scoring.RealismConfig{
				MaxPathRefs:    r.cfg.MaxPathRefs,
				MaxIdentifiers: r.cfg.MaxIdentifiers,
				MaxLength:      r.cfg.MaxLength,
			})

			judgeScore := 0.0
			hasJudge := false
			judgeCtx, cancelJudge := context.WithTimeout(ctx, 90*time.Second)
			judge, judgeErr := manager.JudgeRealism(judgeCtx, specSession, draft.candidate.CandidatePrompt)
			cancelJudge()
			if judgeErr == nil {
				hasJudge = true
				judgeScore = judge.Score
				realism.JudgeScore = judge.Score
				if strings.TrimSpace(judge.Justification) != "" {
					realism.Reasons = append(realism.Reasons, "judge: "+strings.TrimSpace(judge.Justification))
				}
			}
			realism.Score = scoring.CombineRealism(realism.HeuristicScore, judgeScore, hasJudge)

			finalScore := r.cfg.Alpha*tech.Score + (1-r.cfg.Alpha)*realism.Score

			testResult := TestRunResult{Ran: false, Passed: true, Category: "not_run", Summary: "coder session failed before test run"}
			if coderErr == nil {
				testTimeout := time.Duration(maxInt(30, r.cfg.TimeoutSeconds/4)) * time.Second
				testResult = RunBestEffortTests(ctx, runPath, testTimeout)
			}

			iterPatchPath := filepath.Join(paths.artifactsDir, fmt.Sprintf("iter-%03d-cand-%02d.patch", iter, rank+1))
			if err := os.WriteFile(iterPatchPath, []byte(produced.Patch), 0o644); err != nil {
				return Result{}, fmt.Errorf("write iteration patch: %w", err)
			}

			attemptLog := CoderAttemptLog{
				CandidateIndex:    draft.log.Index,
				CandidateStyle:    draft.log.Style,
				CandidatePrompt:   draft.candidate.CandidatePrompt,
				CoderFinalMessage: coderRes.FinalMessage,
				Tech:              tech,
				Realism:           realism,
				FinalScore:        finalScore,
				TestResult:        testResult,
				ProducedPatchPath: iterPatchPath,
				ProducedFiles:     append([]string(nil), produced.ChangedFiles...),
			}
			if coderErr != nil {
				attemptLog.CoderError = coderErr.Error()
			}

			attempts = append(attempts, coderAttemptRuntime{log: attemptLog, produced: produced})

			if !r.cfg.KeepRuns {
				if err := git.RemoveWorktree(ctx, baseRepo, runPath); err != nil && r.cfg.Verbose {
					fmt.Printf("warning: failed to cleanup worktree %s: %v\n", runPath, err)
				}
			}
		}

		bestAttemptIdx := 0
		for i := range attempts {
			if attempts[i].log.FinalScore > attempts[bestAttemptIdx].log.FinalScore {
				bestAttemptIdx = i
			}
		}
		bestAttempt := attempts[bestAttemptIdx]

		feedbackPacket := feedback.BuildIterationPacket(
			iter,
			target,
			bestAttempt.produced,
			bestAttempt.log.Tech,
			bestAttempt.log.TestResult.Category,
			r.cfg.MaxPathRefs,
		)
		if bestAttempt.log.CoderError != "" {
			feedbackPacket.IntentGaps = append(feedbackPacket.IntentGaps, "coder execution had issues; refine acceptance criteria and constraints")
		}

		gapCtx, cancelGap := context.WithTimeout(ctx, 90*time.Second)
		llmGap, gapErr := manager.SummarizeIntentGap(gapCtx, specSession, target.Patch, bestAttempt.produced.Patch, 4)
		cancelGap()
		if gapErr == nil && len(llmGap.Gaps) > 0 {
			feedbackPacket.IntentGaps = dedupeStrings(append(feedbackPacket.IntentGaps, llmGap.Gaps...))
		}

		feedbackText = feedback.PacketText(feedbackPacket)

		iterLog := IterationLog{
			Iteration:          iter,
			Drafts:             draftLogs,
			CoderAttempts:      collectAttemptLogs(attempts),
			SelectedAttempt:    bestAttemptIdx,
			FeedbackPacket:     feedbackPacket,
			IterationBestScore: bestAttempt.log.FinalScore,
		}
		runLog.Iterations = append(runLog.Iterations, iterLog)

		if bestAttempt.log.FinalScore > best.final {
			best = bestState{
				iteration: iter,
				prompt:    bestAttempt.log.CandidatePrompt,
				patch:     bestAttempt.produced.Patch,
				tech:      bestAttempt.log.Tech.Score,
				realism:   bestAttempt.log.Realism.Score,
				final:     bestAttempt.log.FinalScore,
			}
			noImprovement = 0
		} else {
			noImprovement++
		}

		previousPrompt = bestAttempt.log.CandidatePrompt
		previousOutcome = fmt.Sprintf(
			"tech %.2f realism %.2f final %.2f test=%s",
			bestAttempt.log.Tech.Score,
			bestAttempt.log.Realism.Score,
			bestAttempt.log.FinalScore,
			bestAttempt.log.TestResult.Category,
		)

		if r.cfg.Verbose {
			fmt.Printf(
				"[iter %d] best attempt final=%.4f tech=%.4f realism=%.4f\n",
				iter,
				bestAttempt.log.FinalScore,
				bestAttempt.log.Tech.Score,
				bestAttempt.log.Realism.Score,
			)
		}

		if bestAttempt.log.FinalScore >= r.cfg.Threshold {
			stoppedReason = "threshold reached"
			break
		}
		if noImprovement >= 3 {
			stoppedReason = "no improvement for 3 iterations"
			break
		}
	}

	if best.iteration == 0 {
		return Result{}, fmt.Errorf("no successful iteration produced a candidate")
	}

	if err := os.WriteFile(filepath.Join(paths.artifactsDir, "best_prompt.md"), []byte(best.prompt+"\n"), 0o644); err != nil {
		return Result{}, fmt.Errorf("write best_prompt.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(paths.artifactsDir, "best.patch"), []byte(best.patch), 0o644); err != nil {
		return Result{}, fmt.Errorf("write best.patch: %w", err)
	}

	runLog.BestIteration = best.iteration
	runLog.StoppedReason = stoppedReason
	runLog.CompletedAt = time.Now()
	if err := writeJSON(filepath.Join(paths.artifactsDir, "run_log.json"), runLog); err != nil {
		return Result{}, fmt.Errorf("write run_log.json: %w", err)
	}

	metrics := Metrics{
		TechSimilarity: best.tech,
		RealismScore:   best.realism,
		FinalScore:     best.final,
		Alpha:          r.cfg.Alpha,
		BestIteration:  best.iteration,
	}
	if err := writeJSON(filepath.Join(paths.artifactsDir, "metrics.json"), metrics); err != nil {
		return Result{}, fmt.Errorf("write metrics.json: %w", err)
	}

	return Result{
		BestIteration:      best.iteration,
		BestTechSimilarity: best.tech,
		BestRealism:        best.realism,
		BestFinalScore:     best.final,
	}, nil
}

type layoutPaths struct {
	runsDir      string
	artifactsDir string
}

func (r *Runner) ensureLayout() (layoutPaths, error) {
	runsDir := filepath.Join(r.cfg.Workdir, "runs")
	artifactsDir := filepath.Join(r.cfg.Workdir, "artifacts")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return layoutPaths{}, fmt.Errorf("create runs dir: %w", err)
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return layoutPaths{}, fmt.Errorf("create artifacts dir: %w", err)
	}
	return layoutPaths{runsDir: runsDir, artifactsDir: artifactsDir}, nil
}

func (r *Runner) generateCandidatePool(
	ctx context.Context,
	manager *copilot.Manager,
	specSession *sdk.Session,
	iteration int,
	feedbackText string,
	previousPrompt string,
	previousOutcome string,
	promptHistory []string,
	commitMessage string,
	target git.DiffSnapshot,
) ([]candidateDraftRuntime, error) {
	styles := candidateStyles(r.cfg.CandidatesPerIter)
	out := make([]candidateDraftRuntime, 0, len(styles))
	validCount := 0

	for idx, style := range styles {
		candidate, raw, retries, err := r.generateValidCandidate(
			ctx,
			manager,
			specSession,
			iteration,
			feedbackText,
			previousPrompt,
			previousOutcome,
			style,
		)

		logEntry := CandidateDraftLog{
			Index:             idx,
			Style:             style,
			ValidationRetries: retries,
			RawSpecResponse:   raw,
		}

		runtime := candidateDraftRuntime{log: logEntry}
		if err != nil {
			runtime.log.GenerationError = err.Error()
			out = append(out, runtime)
			continue
		}

		realism := scoring.ScoreRealismHeuristic(candidate.CandidatePrompt, scoring.RealismConfig{
			MaxPathRefs:    r.cfg.MaxPathRefs,
			MaxIdentifiers: r.cfg.MaxIdentifiers,
			MaxLength:      r.cfg.MaxLength,
		})
		novelty := noveltyScore(candidate.CandidatePrompt, promptHistory)
		pre := 0.8*realism.HeuristicScore + 0.2*novelty

		runtime.log.CandidatePrompt = candidate.CandidatePrompt
		runtime.log.Rationale = candidate.Rationale
		runtime.log.ScopeHints = append([]string(nil), candidate.ScopeHints...)
		runtime.log.PreRealism = realism.HeuristicScore
		runtime.log.Novelty = novelty
		runtime.log.PreScore = pre
		runtime.candidate = candidate
		runtime.valid = true
		validCount++
		out = append(out, runtime)
	}

	if seed, ok := r.makeCommitSeedCandidate(commitMessage, target, promptHistory); ok {
		out = append(out, seed)
		validCount++
	}

	if validCount == 0 {
		return out, fmt.Errorf("no valid candidates generated")
	}
	return out, nil
}

func (r *Runner) makeCommitSeedCandidate(commitMessage string, target git.DiffSnapshot, promptHistory []string) (candidateDraftRuntime, bool) {
	msg := strings.TrimSpace(stripTrackerRefs(commitMessage))
	if msg == "" {
		return candidateDraftRuntime{}, false
	}
	intents := feedback.InferIntents(target)
	scope := []string{"state management", "connection lifecycle", "test coverage"}
	if len(intents) > 0 {
		scope = nil
		for _, in := range intents {
			if len(scope) >= 4 {
				break
			}
			scope = append(scope, in)
		}
	}

	prompt := strings.TrimSpace(
		"# Context\n" +
			"We need to improve the connection lifecycle to support " + strings.ToLower(msg) + " while keeping behavior backward compatible for normal handshakes.\n\n" +
			"# Desired Outcomes\n" +
			"Add a reliable way to capture minimal runtime connection state and resume from it safely, including validation and graceful fallback when resume is invalid or unavailable.\n\n" +
			"# Constraints and Non-Goals\n" +
			"Keep scope focused on resume-related flows, avoid unrelated refactors, and preserve interoperability expectations.\n\n" +
			"# Acceptance Criteria\n" +
			"Resumed sessions behave consistently with fresh sessions for security and correctness, error paths are explicit, and tests cover both successful and unsuccessful resume scenarios.",
	)

	if r.cfg.MaxLength > 0 && len(prompt) > r.cfg.MaxLength {
		prompt = prompt[:r.cfg.MaxLength]
	}

	if err := ValidateNoCodePrompt(prompt, r.cfg.MaxLength); err != nil {
		return candidateDraftRuntime{}, false
	}
	if err := ValidateStructuredPrompt(prompt); err != nil {
		return candidateDraftRuntime{}, false
	}

	realism := scoring.ScoreRealismHeuristic(prompt, scoring.RealismConfig{
		MaxPathRefs:    r.cfg.MaxPathRefs,
		MaxIdentifiers: r.cfg.MaxIdentifiers,
		MaxLength:      r.cfg.MaxLength,
	})
	novelty := noveltyScore(prompt, promptHistory)
	pre := 0.8*realism.HeuristicScore + 0.2*novelty

	candidate := copilot.SpecCandidate{
		CandidatePrompt: prompt,
		Rationale:       "Commit-message anchored seed to stabilize search around likely intent.",
		ScopeHints:      scope,
	}

	logEntry := CandidateDraftLog{
		Index:             1000,
		Style:             "commit-message-seed",
		CandidatePrompt:   prompt,
		Rationale:         candidate.Rationale,
		ScopeHints:        append([]string(nil), scope...),
		ValidationRetries: 0,
		PreRealism:        realism.HeuristicScore,
		Novelty:           novelty,
		PreScore:          pre,
	}

	return candidateDraftRuntime{log: logEntry, candidate: candidate, valid: true}, true
}

func (r *Runner) generateValidCandidate(
	ctx context.Context,
	manager *copilot.Manager,
	specSession *sdk.Session,
	iteration int,
	feedbackText string,
	previousPrompt string,
	previousOutcome string,
	style string,
) (copilot.SpecCandidate, string, int, error) {
	maxAttempts := 5
	violation := ""
	lastRaw := ""
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		req := copilot.GenerateSpecRequest{
			Iteration:       iteration,
			FeedbackText:    feedbackText,
			MaxPathRefs:     r.cfg.MaxPathRefs,
			MaxLength:       r.cfg.MaxLength,
			Style:           style,
			PreviousPrompt:  previousPrompt,
			PreviousOutcome: previousOutcome,
			ViolationReason: violation,
		}

		candidate, raw, err := manager.GenerateSpecCandidate(ctx, specSession, req)
		lastRaw = raw
		if err != nil {
			lastErr = err
			violation = "output must be strict JSON with candidatePrompt/rationale/scopeHints"
			continue
		}

		if err := ValidateNoCodePrompt(candidate.CandidatePrompt, r.cfg.MaxLength); err != nil {
			lastErr = err
			violation = "no-code constraint violation: " + err.Error()
			continue
		}
		if err := ValidateStructuredPrompt(candidate.CandidatePrompt); err != nil {
			lastErr = err
			violation = "structured format violation: " + err.Error()
			continue
		}

		candidate.CandidatePrompt = strings.TrimSpace(candidate.CandidatePrompt)
		candidate.Rationale = strings.TrimSpace(candidate.Rationale)
		return candidate, lastRaw, attempt, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unknown specwriter failure")
	}
	return copilot.SpecCandidate{}, lastRaw, maxAttempts, fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func collectAttemptLogs(attempts []coderAttemptRuntime) []CoderAttemptLog {
	out := make([]CoderAttemptLog, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, a.log)
	}
	return out
}

func buildObjectiveAnchor(commitMessage string, target git.DiffSnapshot) string {
	msg := strings.TrimSpace(stripTrackerRefs(commitMessage))
	if msg == "" {
		msg = "target commit objective unavailable"
	}
	intents := feedback.InferIntents(target)
	if len(intents) > 5 {
		intents = intents[:5]
	}
	if len(intents) == 0 {
		return "Objective anchor: infer the likely behavioral objective behind the target change and keep the prompt high-level."
	}
	return "Objective anchor from target metadata: " + msg + ". Intent signals: " + strings.Join(intents, "; ") + "."
}

func candidateStyles(n int) []string {
	base := []string{
		"balanced high-level design request",
		"minimal-scope request focused on core behavior",
		"acceptance-criteria-first request",
		"resilience and error-handling focused request",
		"test-oriented request emphasizing observable behavior",
	}
	if n <= len(base) {
		return append([]string(nil), base[:n]...)
	}
	out := append([]string(nil), base...)
	for len(out) < n {
		out = append(out, "balanced high-level design request with concise constraints")
	}
	return out
}

func noveltyScore(candidate string, history []string) float64 {
	if len(history) == 0 {
		return 1
	}
	bestSimilarity := 0.0
	candTokens := toTokenSet(candidate)
	for _, h := range history {
		sim := jaccardTokens(candTokens, toTokenSet(h))
		if sim > bestSimilarity {
			bestSimilarity = sim
		}
	}
	return clamp01(1 - bestSimilarity)
}

func toTokenSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		tok = strings.Trim(tok, " \t\n\r.,;:!?()[]{}\"'`")
		if len(tok) < 4 {
			continue
		}
		out[tok] = struct{}{}
	}
	return out
}

func jaccardTokens(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	sort.Strings(out)
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func stripTrackerRefs(s string) string {
	return trackerRefCleanupRe.ReplaceAllString(s, "")
}
