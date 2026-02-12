package feedback

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/igolaizola/retrospec/internal/git"
	"github.com/igolaizola/retrospec/internal/scoring"
)

var issueRefCleanupRe = regexp.MustCompile(`(?i)(?:^|\s)(?:#\d+|(?:issue|issues|pr|pull request|pull requests)\s*#?\d+)\b`) //nolint:lll

type Packet struct {
	Iteration             int      `json:"iteration"`
	TargetFilesChanged    int      `json:"targetFilesChanged"`
	ProducedFilesChanged  int      `json:"producedFilesChanged"`
	RepresentativePaths   []string `json:"representativePaths,omitempty"`
	LineCountSummaries    []string `json:"lineCountSummaries,omitempty"`
	MissingFiles          []string `json:"missingFiles,omitempty"`
	UnexpectedFiles       []string `json:"unexpectedFiles,omitempty"`
	IntentGaps            []string `json:"intentGaps,omitempty"`
	TargetIntentSignals   []string `json:"targetIntentSignals,omitempty"`
	ProducedIntentSignals []string `json:"producedIntentSignals,omitempty"`
	TestCategory          string   `json:"testCategory,omitempty"`
	TechSummary           string   `json:"techSummary,omitempty"`
	ExtraNotes            []string `json:"extraNotes,omitempty"`
}

func BuildInitialPacket(iteration int, target git.DiffSnapshot, commitMessage string, maxPathRefs int) Packet {
	intents := InferIntents(target)
	reps := limitSorted(target.ChangedFiles, maxPathRefs)
	notes := []string{}
	if commitMessage != "" {
		notes = append(notes, sanitizeOneLine(commitMessage))
	}

	return Packet{
		Iteration:           iteration,
		TargetFilesChanged:  len(target.ChangedFiles),
		RepresentativePaths: reps,
		TargetIntentSignals: intents,
		ExtraNotes:          notes,
	}
}

func BuildIterationPacket(iteration int, target, produced git.DiffSnapshot, tech scoring.TechScore, testCategory string, maxPaths int) Packet {
	missing := difference(target.ChangedFiles, produced.ChangedFiles)
	extra := difference(produced.ChangedFiles, target.ChangedFiles)

	tIntents := InferIntents(target)
	pIntents := InferIntents(produced)
	gaps := summarizeIntentGap(tIntents, pIntents)

	techSummary := fmt.Sprintf(
		"file overlap %.2f, diff similarity %.2f, line F1 %.2f",
		tech.FileJaccard,
		tech.DiffSimilarity,
		tech.LineF1,
	)

	return Packet{
		Iteration:             iteration,
		TargetFilesChanged:    len(target.ChangedFiles),
		ProducedFilesChanged:  len(produced.ChangedFiles),
		RepresentativePaths:   limitSorted(target.ChangedFiles, maxPaths),
		LineCountSummaries:    buildLineCountSummaries(tech.PerFile, maxPaths*2),
		MissingFiles:          limitSorted(missing, maxPaths*2),
		UnexpectedFiles:       limitSorted(extra, maxPaths*2),
		IntentGaps:            gaps,
		TargetIntentSignals:   tIntents,
		ProducedIntentSignals: pIntents,
		TestCategory:          testCategory,
		TechSummary:           techSummary,
	}
}

func PacketText(p Packet) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Iteration: %d\n", p.Iteration)
	fmt.Fprintf(&b, "Target changed files: %d\n", p.TargetFilesChanged)
	if p.ProducedFilesChanged > 0 {
		fmt.Fprintf(&b, "Produced changed files: %d\n", p.ProducedFilesChanged)
	}
	if len(p.RepresentativePaths) > 0 {
		fmt.Fprintf(&b, "Representative paths: %s\n", strings.Join(p.RepresentativePaths, ", "))
	}
	if p.TechSummary != "" {
		fmt.Fprintf(&b, "Similarity summary: %s\n", p.TechSummary)
	}
	if len(p.LineCountSummaries) > 0 {
		fmt.Fprintf(&b, "Line count summary by path: %s\n", strings.Join(p.LineCountSummaries, " | "))
	}
	if len(p.MissingFiles) > 0 {
		fmt.Fprintf(&b, "Missing paths in produced change: %s\n", strings.Join(p.MissingFiles, ", "))
	}
	if len(p.UnexpectedFiles) > 0 {
		fmt.Fprintf(&b, "Unexpected produced paths: %s\n", strings.Join(p.UnexpectedFiles, ", "))
	}
	if len(p.TargetIntentSignals) > 0 {
		fmt.Fprintf(&b, "Target intent signals: %s\n", strings.Join(p.TargetIntentSignals, "; "))
	}
	if len(p.ProducedIntentSignals) > 0 {
		fmt.Fprintf(&b, "Produced intent signals: %s\n", strings.Join(p.ProducedIntentSignals, "; "))
	}
	if len(p.IntentGaps) > 0 {
		fmt.Fprintf(&b, "Intent gaps: %s\n", strings.Join(p.IntentGaps, "; "))
	}
	if p.TestCategory != "" {
		fmt.Fprintf(&b, "Tests status category: %s\n", p.TestCategory)
	}
	for _, note := range p.ExtraNotes {
		fmt.Fprintf(&b, "Note: %s\n", note)
	}

	return strings.TrimSpace(b.String())
}

func InferIntents(snapshot git.DiffSnapshot) []string {
	if strings.TrimSpace(snapshot.Patch) == "" && len(snapshot.ChangedFiles) == 0 {
		return nil
	}

	intent := map[string]bool{}
	for _, path := range snapshot.ChangedFiles {
		lp := strings.ToLower(path)
		if strings.Contains(lp, "_test.") || strings.Contains(lp, "/test") || strings.Contains(lp, "/tests") {
			intent["tests/expectations updated"] = true
		}
		if strings.HasSuffix(lp, ".md") || strings.HasPrefix(lp, "docs/") {
			intent["documentation behavior or guidance changed"] = true
		}
		if strings.Contains(lp, "config") || strings.Contains(lp, "settings") {
			intent["configuration behavior changed"] = true
		}
	}

	patch := strings.ToLower(snapshot.Patch)
	if strings.Contains(patch, "new file mode") || strings.Contains(patch, "--- /dev/null") {
		intent["new component introduced"] = true
	}
	if strings.Contains(patch, "deleted file mode") || strings.Contains(patch, "+++ /dev/null") {
		intent["component removal or consolidation"] = true
	}
	if hasAnyToken(patch, []string{"import ", " require(", " from ", " use "}) {
		intent["dependency usage changed"] = true
	}
	if hasAnyToken(patch, []string{"error", "err", "exception", "retry", "fallback", "panic"}) {
		intent["error handling logic differs"] = true
	}
	if hasAnyToken(patch, []string{"log", "logger", "debug", "warn", "trace", "info"}) {
		intent["logging behavior differs"] = true
	}
	if hasAnyToken(patch, []string{"http", "request", "response", "handler", "route", "endpoint"}) {
		intent["request/response behavior changed"] = true
	}
	if hasAnyToken(patch, []string{"cache", "ttl", "evict", "memo"}) {
		intent["caching behavior changed"] = true
	}

	out := make([]string, 0, len(intent))
	for k, v := range intent {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func summarizeIntentGap(targetIntents, producedIntents []string) []string {
	tset := toSet(targetIntents)
	pset := toSet(producedIntents)
	out := []string{}
	for _, t := range targetIntents {
		if _, ok := pset[t]; !ok {
			out = append(out, "target indicates "+t+" but produced change may not")
		}
	}
	for _, p := range producedIntents {
		if _, ok := tset[p]; !ok {
			out = append(out, "produced change may over-focus on "+p)
		}
	}
	if len(out) == 0 {
		out = append(out, "intent categories largely align")
	}
	return out
}

func buildLineCountSummaries(perFile []scoring.PerFileScore, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, minInt(len(perFile), limit))
	for idx, pf := range perFile {
		if idx >= limit {
			break
		}
		s := fmt.Sprintf(
			"%s target(+%d/-%d) produced(+%d/-%d)",
			pf.Path,
			pf.TargetLinesAdded,
			pf.TargetLinesRemoved,
			pf.ProducedLinesAdded,
			pf.ProducedLinesRemoved,
		)
		out = append(out, s)
	}
	return out
}

func difference(left, right []string) []string {
	r := toSet(right)
	out := []string{}
	for _, l := range left {
		if _, ok := r[l]; !ok {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func limitSorted(items []string, max int) []string {
	if max <= 0 {
		return nil
	}
	copyItems := append([]string(nil), items...)
	sort.Strings(copyItems)
	if len(copyItems) > max {
		copyItems = copyItems[:max]
	}
	return copyItems
}

func sanitizeOneLine(s string) string {
	line := strings.ReplaceAll(s, "\n", " ")
	line = stripIssueRefs(line)
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > 220 {
		line = line[:220]
	}
	return line
}

func stripIssueRefs(s string) string {
	return issueRefCleanupRe.ReplaceAllString(s, "")
}

func hasAnyToken(content string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(content, t) {
			return true
		}
	}
	return false
}

func toSet(items []string) map[string]struct{} {
	s := map[string]struct{}{}
	for _, item := range items {
		s[item] = struct{}{}
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
