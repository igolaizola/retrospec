package scoring

import (
	"math"
	"regexp"
	"strings"
)

type RealismConfig struct {
	MaxPathRefs    int
	MaxIdentifiers int
	MaxLength      int
}

type RealismResult struct {
	HeuristicScore float64  `json:"heuristicScore"`
	JudgeScore     float64  `json:"judgeScore"`
	Score          float64  `json:"score"`
	Reasons        []string `json:"reasons"`
}

var (
	pathRe       = regexp.MustCompile(`(?m)(?:^|\s)(?:[A-Za-z0-9._-]+/)+[A-Za-z0-9._-]+`)
	identifierRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]{2,}\b`)
	numericRe    = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	bulletRe     = regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+\.)\s+`)
)

func ScoreRealismHeuristic(prompt string, cfg RealismConfig) RealismResult {
	text := strings.TrimSpace(prompt)
	if text == "" {
		return RealismResult{HeuristicScore: 0}
	}

	score := 0.55
	reasons := make([]string, 0, 8)

	length := len(text)
	if cfg.MaxLength > 0 {
		if length <= cfg.MaxLength {
			score += 0.08
		} else {
			over := float64(length-cfg.MaxLength) / float64(maxInt(1, cfg.MaxLength))
			pen := math.Min(0.25, over*0.35)
			score -= pen
			reasons = append(reasons, "prompt is overly long and likely too prescriptive")
		}
	} else {
		if length <= 2600 {
			score += 0.03
		} else {
			over := float64(length-2600) / 2600.0
			pen := math.Min(0.20, over*0.25)
			score -= pen
			reasons = append(reasons, "prompt is very long and may become too prescriptive")
		}
	}

	pathRefs := countPathRefs(text)
	if pathRefs > cfg.MaxPathRefs {
		score -= math.Min(0.25, float64(pathRefs-cfg.MaxPathRefs)*0.07)
		reasons = append(reasons, "too many file path references make it look diff-driven")
	} else if pathRefs > 0 {
		score += 0.02
	}

	identifierCount := countLikelyIdentifiers(text)
	if identifierCount > cfg.MaxIdentifiers {
		score -= math.Min(0.25, float64(identifierCount-cfg.MaxIdentifiers)*0.02)
		reasons = append(reasons, "identifier density is high for a high-level specification")
	} else {
		score += 0.04
	}

	numericCount := len(numericRe.FindAllString(text, -1))
	if numericCount > 12 {
		score -= 0.12
		reasons = append(reasons, "too many exact constants can indicate overfitting")
	}

	bullets := len(bulletRe.FindAllString(text, -1))
	if bullets > 10 {
		score -= math.Min(0.20, float64(bullets-10)*0.02)
		reasons = append(reasons, "excessive checklists can encode micro-diffs")
	}

	stepWords := keywordCount(strings.ToLower(text), []string{"then", "after that", "step", "next,"})
	if stepWords > 5 {
		score -= math.Min(0.15, float64(stepWords-5)*0.03)
		reasons = append(reasons, "instruction sequence is too low-level")
	}

	if hasAny(strings.ToLower(text), []string{"problem", "motivation", "currently", "pain point", "context"}) {
		score += 0.06
	} else {
		reasons = append(reasons, "missing clear problem statement/motivation")
	}

	if hasAny(strings.ToLower(text), []string{"should", "must", "expected", "behavior", "outcome"}) {
		score += 0.06
	} else {
		reasons = append(reasons, "desired behavior is not explicit enough")
	}

	if hasAny(strings.ToLower(text), []string{"non-goal", "out of scope", "do not", "avoid"}) {
		score += 0.07
	} else {
		reasons = append(reasons, "constraints or non-goals are missing")
	}

	if hasAny(strings.ToLower(text), []string{"acceptance", "test", "verify", "pass"}) {
		score += 0.07
	} else {
		reasons = append(reasons, "acceptance criteria or test expectations are missing")
	}

	return RealismResult{
		HeuristicScore: clamp01(score),
		Reasons:        reasons,
	}
}

func CombineRealism(heuristic, judge float64, hasJudge bool) float64 {
	if !hasJudge {
		return clamp01(heuristic)
	}
	return clamp01(0.6*heuristic + 0.4*judge)
}

func countPathRefs(s string) int {
	matches := pathRe.FindAllString(s, -1)
	if len(matches) == 0 {
		return 0
	}
	uniq := map[string]struct{}{}
	for _, m := range matches {
		m = strings.TrimSpace(m)
		uniq[m] = struct{}{}
	}
	return len(uniq)
}

func countLikelyIdentifiers(s string) int {
	matches := identifierRe.FindAllString(s, -1)
	count := 0
	for _, m := range matches {
		if looksLikeIdentifier(m) {
			count++
		}
	}
	return count
}

func looksLikeIdentifier(tok string) bool {
	if strings.Contains(tok, "_") {
		return true
	}
	if strings.ToUpper(tok) == tok && len(tok) >= 3 {
		return true
	}
	for i := 1; i < len(tok); i++ {
		if tok[i] >= 'A' && tok[i] <= 'Z' {
			return true
		}
	}
	return false
}

func hasAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func keywordCount(s string, keywords []string) int {
	total := 0
	for _, kw := range keywords {
		total += strings.Count(s, kw)
	}
	return total
}
