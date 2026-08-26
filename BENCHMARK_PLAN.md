# Benchmark improvement plan

> **Status 2026-08-23:** priorities 1-3 are implemented and unit-tested.
> They have **not** been run against the models yet - Ollama wedged on an
> orphaned `llama-server` before the comparison could run (see MORNING_REPORT.md).

## The problem

All three models score at or near 100% on every suite we have:

| Suite | qwen3-coder | ornith1.5 | qwen3.8 |
|---|---:|---:|---:|
| Coding eval (pass@1) | 100% | 100% | 100% |
| Accuracy (18 tasks) | 94.4% | 100% | 100% |
| Tool calling (9 turns) | 100% | 100% | 100% |

A benchmark that everything passes has no discriminating power. Worse, it is
actively misleading here: ornith1.5 wins every measurement we have, yet it is
the model that struggled in opencode — repeated bad edits and self-corrections.

**The suites do not measure the thing that failed.** Specifically:

- The coding eval asks for 18 self-contained functions written from scratch.
  Real agent work is *editing existing code in a repository*.
- The tool suite runs 7 scenarios of at most 2 turns. Real agent loops run
  dozens of turns, recover from tool errors, and carry state.
- **Nothing tests edit format at all** — whether the model emits a diff or
  search/replace block that applies cleanly to a file. That is exactly the
  failure observed in opencode.

## Priority 1 — Edit-format suite (build locally) — **DONE**

The single highest-value addition, because it targets the observed failure
directly and needs no external dataset.

Give the model a real source file and a change request, require a specific
edit format, and grade whether the edit **applies cleanly** and the result
still compiles / passes tests.

Formats to test, since harnesses differ:

- SEARCH/REPLACE blocks (aider, opencode)
- Unified diff
- Whole-file rewrite
- Line-range replacement

Metrics that matter:

| Metric | Why |
|---|---|
| Edit applies cleanly | The binary failure. A malformed block is unusable |
| Result compiles / tests pass | Applied but wrong is still wrong |
| Edits per success | Counts self-correction cycles — the exact symptom |
| Whitespace / indentation fidelity | A frequent silent cause of failed matches |
| Untouched-region drift | Did it rewrite code it was not asked to touch |

Difficulty should scale: single-line change → multi-hunk in one file →
changes across several files → a change requiring reading context first.

**Effort:** moderate. Reuses the existing sandbox and the `tools` scaffolding.
**Tells you:** whether ornith's opencode trouble is edit-format handling.

## Priority 2 — Swap in a harder problem set (cheap) — **DONE (HumanEval 164)**

`ollama-bench eval` already reads HumanEval-format JSONL, so this is a flag
change, not code.

| Dataset | Why | Effort |
|---|---|---|
| **EvalPlus (HumanEval+ / MBPP+)** | Same format, ~80x more tests per problem. Catches solutions that pass 3 asserts but are wrong | `-problems HumanEvalPlus.jsonl` |
| **LiveCodeBench** | Contest problems in time windows, contamination-resistant. Models cannot have memorized recent ones | Small adapter |
| **BigCodeBench** | Library-heavy, complex real-world calls | Small adapter |

Start with EvalPlus. Our 18 bundled problems have ~5-10 asserts each; EvalPlus
averages far more, and its whole purpose is exposing false positives.

## Priority 3 — Aider Polyglot

225 Exercism problems across C++, Go, Java, JavaScript, Python, Rust, with
hidden tests. It is explicitly an **edit-accuracy** benchmark, and it has a
public leaderboard so local numbers can be calibrated against known models.

Runs against an OpenAI-compatible endpoint, which Ollama already exposes.
Directly relevant: it forces models off Python, where SWE-bench is 100%
concentrated.

**Effort:** moderate (external harness, but designed for this).
**Tells you:** how these models rank against published frontier numbers.

## Priority 4 — Harder tool calling — **local hardening DONE; BFCL/τ-bench outstanding**

| Benchmark | What it adds |
|---|---|
| **BFCL v3/v4** | Multi-turn, parallel calls, irrelevance detection (does it refrain when no tool fits), AST-based grading. Runs locally against an OpenAI-compatible endpoint |
| **τ-bench** | Simulated user + domain policies over many turns. Closest thing to real agent behavior |
| **Terminal-Bench** | Long-horizon shell tasks, not just patch emission |

Also worth hardening our own `tools` suite, which is cheap and immediate:

- **Longer horizons** — 5-10 turns instead of 2
- **Error injection** — return a tool error and see whether it recovers or loops
- **Distractor tools** — 15+ tools where only one fits
- **Ambiguity** — a request needing clarification rather than a guess
- **State tracking** — later turns depending on earlier results
- **Parallel calls** — several independent calls in one turn
- **Self-correction counter** — how often it contradicts or retries itself

That last metric deserves emphasis: it is literally the reported symptom, and
nothing currently counts it.

## Priority 5 — SWE-bench Verified (only if the above is not enough)

The most cited repo-repair benchmark, but it needs an agent scaffold and a
Docker image per instance. Heavy alongside a 52GB resident model, and a 2025
analysis found ~20% of "solved" cases were semantically incorrect. Treat as a
later addition, not a starting point.

## A note on published scores

qwen3.8's reported results are for the full-precision model served optimally.
Ours is **Q4_K_M on a Vulkan iGPU**, which is a different artifact. Our own
measurements show it decoding at 26 tok/s with a 125 ms p95 inter-token gap —
visible stutter — against ornith's 74 tok/s and 14 ms. Published quality
rankings do not transfer to a quantized local build without re-measuring, which
is the entire reason for this harness.

## Suggested order

1. Edit-format suite — targets the actual symptom, no dependencies
2. EvalPlus swap — one flag, immediate difficulty increase
3. Harden the local `tools` suite — error recovery, long horizon, self-correction counter
4. Aider Polyglot — external calibration
5. BFCL — rigorous tool-calling measurement
6. SWE-bench — only if still undifferentiated

Items 1-3 are self-contained and would likely separate these three models on
their own. Items 4-6 add external validity.

## Sources

- [Top 10 Open-Source Benchmarks for AI Coding Agents in 2026](https://www.kdnuggets.com/top-10-open-source-benchmarks-for-ai-coding-agents-in-2026)
- [Aider Polyglot Leaderboard 2026](https://agentmarketcap.ai/blog/2026/04/06/aider-polyglot-leaderboard-2026-swe-bench-python-bias)
- [AI Coding Agent Evals: SWE-Bench, Aider Polyglot, Terminal-Bench](https://sureprompts.com/blog/ai-coding-agent-evals-swe-bench-aider-polyglot-terminal-bench)
- [Berkeley Function Calling Leaderboard (BFCL)](https://proceedings.mlr.press/v267/patil25a.html)
- [Tool-Use Benchmarks 2026: BFCL, T-Eval, ToolBench, Tau-Bench Compared](https://benchmarkingagents.com/best-benchmarks-for-tool-use/)
