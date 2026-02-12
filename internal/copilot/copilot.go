package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	sdk "github.com/github/copilot-sdk/go"
)

const (
	defaultModel           = "gpt-5.3-codex"
	defaultReasoningEffort = "medium"
)

type Manager struct {
	client  *sdk.Client
	model   string
	verbose bool
}

type Options struct {
	Model   string
	Verbose bool
}

type SpecCandidate struct {
	CandidatePrompt string   `json:"candidatePrompt"`
	Rationale       string   `json:"rationale"`
	ScopeHints      []string `json:"scopeHints"`
}

type JudgeResult struct {
	Score         float64 `json:"score"`
	Justification string  `json:"justification"`
}

type IntentGapResult struct {
	Gaps []string `json:"gaps"`
}

type CoderResult struct {
	FinalMessage string `json:"finalMessage"`
}

type GenerateSpecRequest struct {
	Iteration       int
	FeedbackText    string
	MaxPathRefs     int
	MaxLength       int
	Style           string
	PreviousPrompt  string
	PreviousOutcome string
	ViolationReason string
}

func NewManager(ctx context.Context, cwd string, opts Options) (*Manager, error) {
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = strings.TrimSpace(os.Getenv("COPILOT_MODEL"))
	}
	if model == "" {
		model = defaultModel
	}

	client := sdk.NewClient(&sdk.ClientOptions{Cwd: cwd})
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start copilot sdk client: %w", err)
	}

	return &Manager{
		client:  client,
		model:   model,
		verbose: opts.Verbose,
	}, nil
}

func (m *Manager) Close() error {
	if m.client == nil {
		return nil
	}
	return m.client.Stop()
}

func (m *Manager) CreateSpecWriterSession(ctx context.Context, workingDir string) (*sdk.Session, error) {
	config := &sdk.SessionConfig{
		Model:            m.model,
		ReasoningEffort:  defaultReasoningEffort,
		WorkingDirectory: workingDir,
		InfiniteSessions: &sdk.InfiniteSessionConfig{Enabled: sdk.Bool(false)},
	}
	s, err := m.client.CreateSession(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create specwriter session: %w", err)
	}
	return s, nil
}

func (m *Manager) GenerateSpecCandidate(ctx context.Context, specSession *sdk.Session, req GenerateSpecRequest) (SpecCandidate, string, error) {
	prompt := buildSpecWriterPrompt(req)
	resp, err := specSession.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt})
	if err != nil {
		return SpecCandidate{}, "", fmt.Errorf("specwriter send: %w", err)
	}

	text := ""
	if resp != nil && resp.Data.Content != nil {
		text = strings.TrimSpace(*resp.Data.Content)
	}

	parsed, err := parseSpecCandidateJSON(text)
	if err != nil {
		return SpecCandidate{}, text, err
	}
	return parsed, text, nil
}

func (m *Manager) JudgeRealism(ctx context.Context, specSession *sdk.Session, candidatePrompt string) (JudgeResult, error) {
	judgeReq := strings.TrimSpace(`You are rating prompt realism.
Return STRICT JSON with keys:
{
  "score": number between 0 and 1,
  "justification": "one short sentence"
}
Scoring rubric:
- High score means this looks like a real high-level engineering design/spec request.
- Penalize overfitting language that looks like diff instructions.
- Do not include code, snippets, commands, logs, or markdown.
`) + "\n\nCandidate prompt:\n" + candidatePrompt

	resp, err := specSession.SendAndWait(ctx, sdk.MessageOptions{Prompt: judgeReq})
	if err != nil {
		return JudgeResult{}, err
	}

	text := ""
	if resp != nil && resp.Data.Content != nil {
		text = strings.TrimSpace(*resp.Data.Content)
	}
	jsonBlob, err := extractJSONObject(text)
	if err != nil {
		return JudgeResult{}, err
	}
	var result JudgeResult
	if err := json.Unmarshal([]byte(jsonBlob), &result); err != nil {
		return JudgeResult{}, err
	}
	if math.IsNaN(result.Score) || math.IsInf(result.Score, 0) {
		result.Score = 0
	}
	if result.Score < 0 {
		result.Score = 0
	}
	if result.Score > 1 {
		result.Score = 1
	}
	return result, nil
}

func (m *Manager) SummarizeIntentGap(ctx context.Context, specSession *sdk.Session, targetPatch, producedPatch string, maxItems int) (IntentGapResult, error) {
	if maxItems < 1 {
		maxItems = 1
	}
	if maxItems > 8 {
		maxItems = 8
	}

	limitPatch := func(p string) string {
		p = strings.TrimSpace(p)
		if len(p) <= 12000 {
			return p
		}
		return p[:12000]
	}

	req := fmt.Sprintf(`Summarize behavioral intent differences between two internal change sets.
Return STRICT JSON only:
{
  "gaps": ["short abstract sentence", "..."]
}
Rules:
- No code snippets.
- No diff lines.
- No command lines.
- No stack traces.
- Do not quote exact source lines.
- Use high-level behavioral categories only.
- Maximum %d items.
`, maxItems)

	req += "\nTarget patch (internal use only):\n" + limitPatch(targetPatch)
	req += "\n\nProduced patch (internal use only):\n" + limitPatch(producedPatch)

	resp, err := specSession.SendAndWait(ctx, sdk.MessageOptions{Prompt: req})
	if err != nil {
		return IntentGapResult{}, err
	}

	text := ""
	if resp != nil && resp.Data.Content != nil {
		text = strings.TrimSpace(*resp.Data.Content)
	}
	jsonBlob, err := extractJSONObject(text)
	if err != nil {
		return IntentGapResult{}, err
	}
	var out IntentGapResult
	if err := json.Unmarshal([]byte(jsonBlob), &out); err != nil {
		return IntentGapResult{}, err
	}

	filtered := make([]string, 0, len(out.Gaps))
	for _, g := range out.Gaps {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if strings.Contains(g, "```") || strings.Contains(g, "`") {
			continue
		}
		filtered = append(filtered, g)
		if len(filtered) >= maxItems {
			break
		}
	}
	out.Gaps = filtered
	return out, nil
}

func (m *Manager) RunCoder(ctx context.Context, workingDir, candidatePrompt string) (CoderResult, error) {
	permissionHandler := func(request sdk.PermissionRequest, invocation sdk.PermissionInvocation) (sdk.PermissionRequestResult, error) {
		return sdk.PermissionRequestResult{Kind: "approved"}, nil
	}

	config := &sdk.SessionConfig{
		Model:               m.model,
		ReasoningEffort:     defaultReasoningEffort,
		OnPermissionRequest: permissionHandler,
		WorkingDirectory:    workingDir,
		InfiniteSessions:    &sdk.InfiniteSessionConfig{Enabled: sdk.Bool(false)},
	}

	session, err := m.client.CreateSession(ctx, config)
	if err != nil {
		return CoderResult{}, fmt.Errorf("create coder session: %w", err)
	}
	defer session.Destroy()

	if m.verbose {
		session.On(func(event sdk.SessionEvent) {
			if event.Type == sdk.ToolExecutionComplete && event.Data.ToolName != nil {
				fmt.Printf("[coder] tool finished: %s\n", *event.Data.ToolName)
			}
		})
	}

	prompt := strings.TrimSpace(`You are implementing a design/spec request in this repository checked out at a parent commit.
Apply only the requested behavior with minimal unrelated edits.
Use best effort to run relevant tests before finishing.
`) + "\n\n" + candidatePrompt

	resp, err := session.SendAndWait(ctx, sdk.MessageOptions{Prompt: prompt})
	if err != nil {
		return CoderResult{}, fmt.Errorf("coder send: %w", err)
	}

	final := ""
	if resp != nil && resp.Data.Content != nil {
		final = strings.TrimSpace(*resp.Data.Content)
	}

	return CoderResult{FinalMessage: final}, nil
}

func buildSpecWriterPrompt(req GenerateSpecRequest) string {
	b := strings.Builder{}
	b.WriteString("You are SpecWriter. Produce ONE high-level design/spec request that could plausibly lead to the target commit.\n")
	b.WriteString("Output STRICT JSON only with keys candidatePrompt, rationale, scopeHints.\n")
	b.WriteString("Return plain JSON object only. No markdown wrappers.\n")
	b.WriteString("candidatePrompt must be plain English prose, no code or command-like content.\n")
	b.WriteString("Hard prohibitions for candidatePrompt: no code blocks, no inline code, no diffs, no shell commands, no stack traces, no compiler logs.\n")
	b.WriteString("Do not mention issue numbers, PR numbers, tickets, or references like #123.\n")
	b.WriteString("It must include: problem context, desired behavior, constraints/non-goals, and acceptance criteria.\n")
	b.WriteString("Format candidatePrompt as markdown with exactly these top-level sections in order:\n")
	b.WriteString("# Context\n# Desired Outcomes\n# Constraints and Non-Goals\n# Acceptance Criteria\n")
	b.WriteString("Keep it concise and human-like. Avoid long enumerations of tiny edits.\n")
	if strings.TrimSpace(req.Style) != "" {
		b.WriteString("Style focus: ")
		b.WriteString(strings.TrimSpace(req.Style))
		b.WriteString(".\n")
	}
	if req.MaxLength > 0 {
		b.WriteString(fmt.Sprintf("Keep prompt length <= %d characters.\n", req.MaxLength))
	}
	b.WriteString("Prefer concise language and avoid over-specifying micro-steps.\n")
	b.WriteString(fmt.Sprintf("Use at most %d natural file-path references.\n", req.MaxPathRefs))
	b.WriteString("scopeHints must be a JSON array of short strings.\n")
	b.WriteString("Avoid low-level step-by-step micro-edit instructions.\n")
	b.WriteString("\nContext packet:\n")
	b.WriteString(req.FeedbackText)
	b.WriteString("\n")

	if req.PreviousPrompt != "" {
		b.WriteString("Previous candidate prompt summary: present. Improve realism and technical alignment without becoming diff-like.\n")
	}
	if req.PreviousOutcome != "" {
		b.WriteString("Previous outcome: ")
		b.WriteString(req.PreviousOutcome)
		b.WriteString("\n")
	}
	if req.ViolationReason != "" {
		b.WriteString("Validation failure to fix: ")
		b.WriteString(req.ViolationReason)
		b.WriteString("\n")
	}

	b.WriteString("\nReturn only valid JSON.\n")
	return b.String()
}

func parseSpecCandidateJSON(raw string) (SpecCandidate, error) {
	jsonBlob, err := extractJSONObject(raw)
	if err != nil {
		return SpecCandidate{}, fmt.Errorf("extract specwriter json: %w", err)
	}

	type candidateStrict struct {
		CandidatePrompt string          `json:"candidatePrompt"`
		Rationale       string          `json:"rationale"`
		ScopeHints      json.RawMessage `json:"scopeHints"`
	}
	var strict candidateStrict
	if err := json.Unmarshal([]byte(jsonBlob), &strict); err != nil {
		return SpecCandidate{}, fmt.Errorf("parse specwriter json: %w", err)
	}

	out := SpecCandidate{
		CandidatePrompt: strict.CandidatePrompt,
		Rationale:       strict.Rationale,
	}

	if len(strict.ScopeHints) > 0 && string(strict.ScopeHints) != "null" {
		var arr []string
		if err := json.Unmarshal(strict.ScopeHints, &arr); err == nil {
			out.ScopeHints = arr
		} else {
			var single string
			if err := json.Unmarshal(strict.ScopeHints, &single); err == nil {
				single = strings.TrimSpace(single)
				if single != "" {
					if strings.Contains(single, ",") {
						for _, part := range strings.Split(single, ",") {
							part = strings.TrimSpace(part)
							if part != "" {
								out.ScopeHints = append(out.ScopeHints, part)
							}
						}
					} else {
						out.ScopeHints = []string{single}
					}
				}
			}
		}
	}

	out.CandidatePrompt = strings.TrimSpace(out.CandidatePrompt)
	out.Rationale = strings.TrimSpace(out.Rationale)
	if out.CandidatePrompt == "" {
		return SpecCandidate{}, fmt.Errorf("candidatePrompt is empty")
	}
	if out.Rationale == "" {
		out.Rationale = "Prompt focuses on behavioral outcomes and acceptance criteria."
	}
	if out.ScopeHints == nil {
		out.ScopeHints = []string{}
	}
	return out, nil
}

func extractJSONObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty response")
	}

	start := strings.Index(raw, "{")
	if start < 0 {
		return "", fmt.Errorf("no json object start found")
	}

	inString := false
	escape := false
	depth := 0
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if ch == '\\' {
				escape = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1], nil
			}
		}
	}

	return "", fmt.Errorf("unterminated json object")
}
