# AGENTS.md

Notes for working in `bench/`. This is **`ollama-bench`**, a stdlib-only Go CLI that benchmarks local Ollama models. A Python port (`benchmark.py`) at the repo root is the current primary path; this Go tool is the richer, more detailed implementation (better metrics, the `eval` coding suite, `compare`, tool-calling suite). Both coexist — don't assume bench is the only benchmark.

## What it does

- `go build` produces one static binary, no network / no `go mod download` (stdlib only).
- Default command: **how fast** a model is (TTFT, prefill/decode tok/s, ITL, cold load, client overhead).
- `eval` subcommand: **whether it is right** — generates code, extracts it, runs unit tests in a sandbox, reports pass@1 and failure breakdown.
- `list`, `compare` subcommands.

## Build / test / run

```bash
cd bench
go build -o ollama-bench .        # stdlib only; need both for `go build`
go test ./...                     # includes sandbox execution of python
go test -short ./...              # skips anything that executes python
```

Run against a live Ollama server (defaults to `http://localhost:11434`):

```bash
./ollama-bench list
./ollama-bench -models gemma4:latest          # performance benchmark
./ollama-bench -config ../bench.json          # benchmark the configured set
./ollama-bench eval -models gemma4:latest     # coding eval
./ollama-bench compare old.json new.json       # diff two runs
```

## Conventions to follow

- **Stdlib only.** Never add external module dependencies. Config is JSON (not YAML) *deliberately* to keep the binary dependency-free; config JSON allows `//` comments (see `comment.go` / a comment stripper that decodes the file).
- **No comments in your edits** unless explicitly requested — this codebase is unusual in that. Match the existing sparse style.
- One small, well-scoped change per file. Types live in small groups per concern (`Config`/`ModelCfg`, `Record`, `runner`, `Client`, `Sandbox`, tool-scenario structs).
- Every report also writes `results/<timestamp>.{json,csv}` (+ `.md` with `-markdown`); the JSON stores full records, summaries, and environment (Ollama version, CPU/RAM, git SHA + dirty flag, per-model quant/params/`num_ctx`/VRAM residency).

## Key files

- `main.go` — CLI entry, subcommand dispatch (`eval`/`list`/`compare`/default), flag definitions, `flags` struct.
- `config.go` — `Config` struct, `DefaultConfig()`, `LoadConfig`, `Validate`. Config comment-stripping lives here too.
- `runner.go` — `Runner`; `exec` (`/api/generate`) and `execChat` (`/api/chat`) derive every `Record` field in one place.
- `client.go` — Ollama HTTP client (`Version`, `Tags`, `Generate`, `Chat`, `PS`, `Unload`).
- `metrics.go` / `evaluate.go` / `suites.go` — aggregation, stats (n/mean/median/p95/CV), suite definitions.
- `tools.go` — multi-turn tool-calling scenarios.
- `accuracy.go` — accuracy suite.
- `eval.go` / `sandbox.go` — coding eval + layered sandbox isolation.
- `README.md` — authoritative method docs; read before making metrics/metering changes.

## Do NOT change without reading the README first

- **Metering / metrics** (TTFT, prefill/decode, ITL, cold load, CV `!` flag). Method matters more than formula; wrong numbers look plausible and are wrong.
- **Sandboxing** (`sandbox.go`). Executing model code is the one genuinely dangerous part. Isolation is layered: `unshare -rn` netns + `prlimit` caps + `python3 -I` + scratch dir + hard timeout in the child's process group. The isolation level *actually achieved* is printed and stored, and each layer is probed then degrades explicitly. Never weaken or fake the reported level.
- **Config comment stripping / determinism** (unique seeded filler per condition, no shared prompts, warmup discarded, settle-per-condition).
- Always prefer **`/api/chat` over `/api/generate`** for content-graded suites — `generate` leaks reasoning into the response for models whose template pre-injects a thinking tag, which mis-scores exact-output graders.

## Tests

The suite self-tests the harness: every bundled problem's reference solution must pass in the real sandbox (100% required), plus failure classification and that the sandbox network is unreachable. `bench -short` skips python-executing tests.

## Misc

- `bench.json` at the repo root drives a configured benchmark set (`//` comments allowed).
- Bundled coding problems live in `problems/bundled.jsonl` (HumanEval-style JSONL, shipped in the binary for offline runs).
- Build output `ollama-bench` and `results/`/`reports/` are git-ignored (see repo-root `.gitignore`).
- Ctrl-C cancels in-flight work but still writes what was measured (returns exit 130 when interrupted); run-regression `compare` returns exit 1 beyond `-threshold`.
