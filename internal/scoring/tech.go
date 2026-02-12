package scoring

import (
	"math"
	"sort"
	"strings"

	"github.com/igolaizola/retrospec/internal/git"
)

type PerFileScore struct {
	Path                 string  `json:"path"`
	Similarity           float64 `json:"similarity"`
	TargetLinesAdded     int     `json:"targetLinesAdded"`
	TargetLinesRemoved   int     `json:"targetLinesRemoved"`
	ProducedLinesAdded   int     `json:"producedLinesAdded"`
	ProducedLinesRemoved int     `json:"producedLinesRemoved"`
}

type TechScore struct {
	FileJaccard       float64        `json:"fileJaccard"`
	DiffSimilarity    float64        `json:"diffSimilarity"`
	LinePrecision     float64        `json:"linePrecision"`
	LineRecall        float64        `json:"lineRecall"`
	LineF1            float64        `json:"lineF1"`
	Score             float64        `json:"score"`
	PerFile           []PerFileScore `json:"perFile"`
	TargetFiles       int            `json:"targetFiles"`
	ProducedFiles     int            `json:"producedFiles"`
	TargetTotalAdds   int            `json:"targetTotalAdds"`
	TargetTotalDels   int            `json:"targetTotalDels"`
	ProducedTotalAdds int            `json:"producedTotalAdds"`
	ProducedTotalDels int            `json:"producedTotalDels"`
}

type parsedPatch struct {
	fileLines map[string]map[string]int
	global    map[string]int
}

func ScoreTechSimilarity(target, produced git.DiffSnapshot) TechScore {
	targetSet := toSet(target.ChangedFiles)
	producedSet := toSet(produced.ChangedFiles)
	fileJaccard := jaccardSet(targetSet, producedSet)

	targetParsed := parseUnifiedDiff(target.Patch)
	producedParsed := parseUnifiedDiff(produced.Patch)
	diffSimilarity := weightedJaccard(targetParsed.global, producedParsed.global)

	tp := multisetIntersectionCount(targetParsed.global, producedParsed.global)
	targetN := multisetCount(targetParsed.global)
	producedN := multisetCount(producedParsed.global)
	precision := safeDiv(float64(tp), float64(producedN))
	recall := safeDiv(float64(tp), float64(targetN))
	f1 := safeDiv(2*precision*recall, precision+recall)

	perFile := buildPerFileScores(target, produced, targetParsed, producedParsed)

	tAdds, tDels := totalAddsRemoves(target.FileStats)
	pAdds, pDels := totalAddsRemoves(produced.FileStats)

	final := clamp01(0.4*fileJaccard + 0.45*diffSimilarity + 0.15*f1)

	return TechScore{
		FileJaccard:       fileJaccard,
		DiffSimilarity:    diffSimilarity,
		LinePrecision:     precision,
		LineRecall:        recall,
		LineF1:            f1,
		Score:             final,
		PerFile:           perFile,
		TargetFiles:       len(targetSet),
		ProducedFiles:     len(producedSet),
		TargetTotalAdds:   tAdds,
		TargetTotalDels:   tDels,
		ProducedTotalAdds: pAdds,
		ProducedTotalDels: pDels,
	}
}

func buildPerFileScores(target, produced git.DiffSnapshot, targetParsed, producedParsed parsedPatch) []PerFileScore {
	pathsSet := map[string]struct{}{}
	for _, p := range target.ChangedFiles {
		pathsSet[p] = struct{}{}
	}
	for _, p := range produced.ChangedFiles {
		pathsSet[p] = struct{}{}
	}

	paths := make([]string, 0, len(pathsSet))
	for p := range pathsSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]PerFileScore, 0, len(paths))
	for _, p := range paths {
		tLines := targetParsed.fileLines[p]
		pLines := producedParsed.fileLines[p]
		sim := weightedJaccard(tLines, pLines)
		t := target.FileStats[p]
		pr := produced.FileStats[p]
		out = append(out, PerFileScore{
			Path:                 p,
			Similarity:           sim,
			TargetLinesAdded:     t.Added,
			TargetLinesRemoved:   t.Removed,
			ProducedLinesAdded:   pr.Added,
			ProducedLinesRemoved: pr.Removed,
		})
	}
	return out
}

func parseUnifiedDiff(patch string) parsedPatch {
	result := parsedPatch{
		fileLines: map[string]map[string]int{},
		global:    map[string]int{},
	}

	current := ""
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Split(line, " ")
			if len(parts) >= 4 {
				current = strings.TrimPrefix(parts[3], "b/")
				if _, ok := result.fileLines[current]; !ok {
					result.fileLines[current] = map[string]int{}
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			addDiffLine(result, current, "+", line[1:])
			continue
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			addDiffLine(result, current, "-", line[1:])
			continue
		}
	}

	return result
}

func addDiffLine(p parsedPatch, file, prefix, raw string) {
	normalized := normalizeLine(raw)
	if normalized == "" {
		return
	}
	key := prefix + normalized
	p.global[key]++
	if file != "" {
		if _, ok := p.fileLines[file]; !ok {
			p.fileLines[file] = map[string]int{}
		}
		p.fileLines[file][key]++
	}
}

func normalizeLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func weightedJaccard(a, b map[string]int) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	unionKeys := map[string]struct{}{}
	for k := range a {
		unionKeys[k] = struct{}{}
	}
	for k := range b {
		unionKeys[k] = struct{}{}
	}

	var inter int
	var uni int
	for k := range unionKeys {
		av := a[k]
		bv := b[k]
		inter += minInt(av, bv)
		uni += maxInt(av, bv)
	}
	return safeDiv(float64(inter), float64(uni))
}

func multisetIntersectionCount(a, b map[string]int) int {
	var n int
	for k, av := range a {
		n += minInt(av, b[k])
	}
	return n
}

func multisetCount(a map[string]int) int {
	var n int
	for _, v := range a {
		n += v
	}
	return n
}

func jaccardSet(a, b map[string]struct{}) float64 {
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
	return safeDiv(float64(inter), float64(union))
}

func toSet(items []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, it := range items {
		out[it] = struct{}{}
	}
	return out
}

func totalAddsRemoves(stats map[string]git.FileStat) (int, int) {
	adds := 0
	rems := 0
	for _, s := range stats {
		adds += s.Added
		rems += s.Removed
	}
	return adds, rems
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		if a == 0 {
			return 1
		}
		return 0
	}
	return a / b
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
