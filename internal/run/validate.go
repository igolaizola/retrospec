package run

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	commandLineRe       = regexp.MustCompile(`(?mi)^\s*(?:\$\s*|git\s+\S+|go\s+(?:test|run|build|tool)\b|npm\s+\S+|npx\s+\S+|cargo\s+\S+|make\b|bash\b|sh\b)`) //nolint:lll
	diffMarkerRe        = regexp.MustCompile(`(?m)^(?:diff\s+--git|@@\s|\+\+\+\s|---\s)`)                                                                      //nolint:lll
	stackTraceRe        = regexp.MustCompile(`(?m)^\s*at\s+\S+\s+\(.+?:\d+`)                                                                                   //nolint:lll
	compileErrRe        = regexp.MustCompile(`(?m)[A-Za-z0-9_./-]+:\d+(?::\d+)?:\s`)                                                                           //nolint:lll
	issueRefRe          = regexp.MustCompile(`(?i)(?:^|\s)(?:#\d+|(?:issue|issues|pr|pull request|pull requests)\s*#?\d+)\b`)                                  //nolint:lll
	sectionContextRe    = regexp.MustCompile(`(?im)^\s*#\s*context\b`)
	sectionOutcomeRe    = regexp.MustCompile(`(?im)^\s*#\s*(desired outcomes?|goals?)\b`)
	sectionConstraintRe = regexp.MustCompile(`(?im)^\s*#\s*(constraints?(?:\s+and\s+non-goals?)?|non-goals?|out of scope)\b`)
	sectionAcceptRe     = regexp.MustCompile(`(?im)^\s*#\s*(acceptance criteria|validation|test expectations?)\b`)
)

func ValidateNoCodePrompt(prompt string, maxLength int) error {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return fmt.Errorf("candidatePrompt is empty")
	}
	if maxLength > 0 && len(trimmed) > maxLength {
		return fmt.Errorf("candidatePrompt exceeds max length (%d > %d)", len(trimmed), maxLength)
	}
	if strings.Contains(trimmed, "```") {
		return fmt.Errorf("candidatePrompt contains fenced code block")
	}
	if strings.Contains(trimmed, "`") {
		return fmt.Errorf("candidatePrompt contains inline code marker")
	}
	if diffMarkerRe.MatchString(trimmed) {
		return fmt.Errorf("candidatePrompt contains diff markers")
	}
	if commandLineRe.MatchString(trimmed) {
		return fmt.Errorf("candidatePrompt appears to include command lines")
	}
	if stackTraceRe.MatchString(trimmed) {
		return fmt.Errorf("candidatePrompt appears to include stack trace lines")
	}
	if compileErrRe.MatchString(trimmed) {
		return fmt.Errorf("candidatePrompt appears to include compiler/log output")
	}
	if issueRefRe.MatchString(trimmed) {
		return fmt.Errorf("candidatePrompt includes issue/PR references (for example #123)")
	}

	for _, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			if len(l) > 1 && l[1] != ' ' {
				return fmt.Errorf("candidatePrompt has code-like prefixed lines")
			}
		}
	}

	return nil
}

func ValidateStructuredPrompt(prompt string) error {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return fmt.Errorf("candidatePrompt is empty")
	}
	if !sectionContextRe.MatchString(trimmed) {
		return fmt.Errorf("missing # Context section")
	}
	if !sectionOutcomeRe.MatchString(trimmed) {
		return fmt.Errorf("missing # Desired Outcomes section")
	}
	if !sectionConstraintRe.MatchString(trimmed) {
		return fmt.Errorf("missing # Constraints and Non-Goals section")
	}
	if !sectionAcceptRe.MatchString(trimmed) {
		return fmt.Errorf("missing # Acceptance Criteria section")
	}
	return nil
}
