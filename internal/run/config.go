package run

import (
	"fmt"
)

type Config struct {
	Repo              string
	Commit            string
	Workdir           string
	MaxIters          int
	Threshold         float64
	TimeoutSeconds    int
	KeepRuns          bool
	Verbose           bool
	Alpha             float64
	MaxPathRefs       int
	MaxIdentifiers    int
	MaxLength         int
	CandidatesPerIter int
	CoderRunsPerIter  int
	Model             string
}

func (c Config) Validate() error {
	if c.MaxIters <= 0 {
		return fmt.Errorf("max-iters must be > 0")
	}
	if c.Threshold < 0 || c.Threshold > 1 {
		return fmt.Errorf("threshold must be in [0,1]")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout-seconds must be > 0")
	}
	if c.Alpha < 0 || c.Alpha > 1 {
		return fmt.Errorf("alpha must be in [0,1]")
	}
	if c.MaxPathRefs < 0 {
		return fmt.Errorf("max-path-refs must be >= 0")
	}
	if c.MaxIdentifiers < 1 {
		return fmt.Errorf("max-identifiers must be >= 1")
	}
	if c.MaxLength < 0 {
		return fmt.Errorf("max-length must be >= 0")
	}
	if c.CandidatesPerIter < 1 {
		return fmt.Errorf("candidates-per-iter must be >= 1")
	}
	if c.CoderRunsPerIter < 1 {
		return fmt.Errorf("coder-runs-per-iter must be >= 1")
	}
	if c.CoderRunsPerIter > c.CandidatesPerIter {
		return fmt.Errorf("coder-runs-per-iter must be <= candidates-per-iter")
	}
	return nil
}

type Result struct {
	BestIteration      int
	BestTechSimilarity float64
	BestRealism        float64
	BestFinalScore     float64
}
