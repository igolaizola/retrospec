# retrospec

`retrospec` is a CLI that tries to answer this question:

"What single high-level spec could someone have written so a coding agent would produce this commit?"

You give it a repository and a target commit SHA. It runs an iterative search using GitHub Copilot SDK sessions and outputs the best prompt it found.

## Why This Exists

- Understand intent behind historical commits
- Generate reusable task specs from real code changes
- Build datasets of realistic product/engineering requests
- Compare how "spec quality" maps to code outcomes

## What The Tool Optimizes

Every candidate prompt is scored on two axes:

- Technical similarity: how close generated changes are to the target commit
- Spec realism: how likely the prompt looks like a real human design request

Final score:

`finalScore = alpha * techSimilarity + (1 - alpha) * realismScore`

Default `alpha` is `0.75`.

## Prompt Rules (Enforced)

The discovered prompt is always structured markdown and must include these sections:

- `# Context`
- `# Desired Outcomes`
- `# Constraints and Non-Goals`
- `# Acceptance Criteria`

Hard constraints:

- No code blocks
- No inline code formatting
- No diffs/snippets/commands/log dumps
- No stack traces
- No issue or PR references like `#41`, `PR 12`, `issue 9`

## Repository Input Modes

`--repo` accepts all of these:

- Local path to an existing clone: `/path/to/repo`
- Full URL: `https://github.com/owner/repo`
- Host/path shorthand: `github.com/owner/repo`
- Owner/repo shorthand: `owner/repo` (treated as GitHub)

If the target commit SHA is not in advertised refs, the tool attempts additional fetch strategies automatically.

## Requirements

- Go
- Git
- GitHub Copilot CLI installed and authenticated
- Access to `github.com/github/copilot-sdk/go`

Optional model override:

- Environment: `COPILOT_MODEL`
- CLI flag: `--model`

Default model is `gpt-5.3-codex`.

## Install / Build

```bash
go build ./cmd/retrospec
```

## Quick Start

Remote repository:

```bash
./retrospec \
  --repo https://github.com/pion/dtls \
  --commit 5722cdfd18abc06836de6a8cbb20f91e67589907 \
  --workdir ./work
```

Local clone:

```bash
./retrospec \
  --repo /path/to/local/clone \
  --commit 5722cdfd18abc06836de6a8cbb20f91e67589907 \
  --workdir ./work
```

## Common Flags

- `--repo` repository URL or local path
- `--commit` target commit SHA
- `--workdir` output workspace for base clone, runs, and artifacts
- `--max-iters` optimization iterations
- `--threshold` stop early when score is good enough
- `--timeout-seconds` per coder run timeout
- `--alpha` trade-off between technical match and realism
- `--candidates-per-iter` spec drafts generated per iteration
- `--coder-runs-per-iter` top drafts executed by coder each iteration
- `--max-length` prompt length cap (`0` means unlimited)
- `--max-path-refs` realism heuristic threshold for path mentions
- `--max-identifiers` realism heuristic threshold for identifier density
- `--model` model override for all Copilot sessions
- `--keep-runs` keep per-iteration worktrees
- `--verbose` print iteration progress

## Output Artifacts

Written under `<workdir>/artifacts`:

- `best_prompt.md` best discovered spec prompt
- `metrics.json` best score summary
- `run_log.json` all iterations, candidates, and scores
- `target.patch` target commit patch
- `best.patch` best produced patch

## How It Works (High Level)

1. Clone/copy repo into an isolated workspace.
2. Resolve target commit and parent commit.
3. Compute target patch once.
4. Iterate:
   - Generate multiple structured candidate specs.
   - Validate strict no-code/no-reference rules.
   - Execute top candidates on fresh parent worktrees with Copilot coder sessions.
   - Score technical similarity + realism.
   - Feed abstract non-code gap summaries back into next iteration.
5. Save best prompt + metrics + patches.

## Notes

- This is a heuristic search problem, so scores vary run to run.
- Higher realism may reduce overfit but can lower immediate patch similarity.
- For difficult commits, increase `--max-iters`, `--candidates-per-iter`, and timeout.
