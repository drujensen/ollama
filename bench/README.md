# ollama-bench

A benchmark for local Ollama models that reports metrics you can act on.

Stdlib-only Go: `go build` needs no network and produces one static binary.

```bash
cd bench && go build -o ollama-bench .
./ollama-bench list                       # what the server has
./ollama-bench -models gemma4:latest      # performance benchmark
./ollama-bench -config ../bench.json      # benchmark the configured set
./ollama-bench eval -models gemma4:latest # coding eval: generate code, run its tests
./ollama-bench compare old.json new.json  # diff two runs
```

Two commands, two different questions. The default command measures **how fast**
a model is. `eval` measures **whether it is right**. Neither substitutes for the
other: a model can win the throughput table and fail half the eval.

## Why not just time the request

Wall-clock time around one non-streaming request answers "how long did that
take" and nothing else. It cannot tell you *why*, and it hides four things that
decide whether a model is usable on a given box:

| Problem | What this reports instead |
|---|---|
| Prefill and decode averaged together produce a number that tracks prompt length, not model speed | `prompt_eval_count/prompt_eval_duration` and `eval_count/eval_duration`, always separate |
| Model load time is buried inside `total_duration` | A discarded warmup request; cold load reported on its own |
| No time-to-first-token | Streaming responses, TTFT and inter-token latency measured client side |
| A single average over a noisy shared GPU | median / p95 / stddev, with cells over 15% CV marked `!` |

## Metrics

Per request:

- **TTFT** - client-side ms until the first token arrives.
- **prefill tok/s** - prompt processing, `prompt_eval_count / prompt_eval_duration`.
- **decode tok/s** - generation, `eval_count / eval_duration`. The number a user waits on.
- **ITL p50/p95** - gap between tokens; high p95 means visible stutter even at a good average.
- **cold load** - `load_duration` from the warmup request, kept out of every other figure.
- **client overhead** - wall time minus server `total_duration`, i.e. HTTP and queueing.
- **done_reason** - so a model truncating at `num_predict` is not mistaken for a fast one.

Aggregated per cell: n, mean, median, p95, min, max, stddev, and coefficient of
variation. A cell with CV over 15% is printed with a trailing `!`: treat close
comparisons against it as a tie.

## Suites

- **latency** - short prompts. TTFT and decode throughput.
- **prefill** - synthetic prompts at 1k/4k/16k tokens with `num_predict: 8`.
  Prompt-processing throughput as a function of prompt length.
- **depth** - decode throughput with the KV cache filled to 0/8k/32k tokens.
  This curve is what separates long-context models on constrained hardware.
- **concurrency** - 1/2/4 parallel streams. Aggregate throughput, per-stream
  throughput, and scaling factor. Flat scaling usually means
  `OLLAMA_NUM_PARALLEL=1`.
- **sanity** - three cheap pass/fail checks (bash script parses under `bash -n`,
  JSON mode produces a valid object with the requested keys, an exact-output
  instruction is followed). Not a quality score; it catches a model that
  benchmarks fast because it is emitting garbage.
- **accuracy** - 18 auto-graded tasks across six groups (math, classify,
  extract, choice, format, instructions), scored per group. Ported from this
  project's `benchmark.py`.
- **tools** - multi-turn tool calling: the pattern a coding agent actually
  drives. See below.
- **edit** - changing an existing file with SEARCH/REPLACE blocks. See below.

## Tool calling

Single-shot code quality can look excellent while an agent still misbehaves,
because agents fail in the *loop* rather than in one completion. The `tools`
suite drives seven scenarios over `/api/chat`, feeding each simulated tool
result back so later turns depend on earlier ones:

| Scenario | What it catches |
|---|---|
| `single-call` | Does it call a tool at all, with the right name |
| `arg-fidelity` | Are the arguments faithful to a literal instruction |
| `tool-choice` | Picking the right tool from four |
| `no-tool-needed` | **Restraint** - calling a tool for everything burns turns and confuses the harness loop |
| `error-recovery` | A tool returns an error. Does it change approach, or reissue the identical failing call |
| `distractors` | 13 tools, only one fits |
| `long-horizon` | Four dependent turns: list, read, fix, test |
| `state-tracking` | A later turn needs a value that only exists in an earlier result |
| `quoting` | Nested quotes and escaping, where malformed arguments surface |
| `multi-turn-edit` | The opencode pattern: read a file, then edit it based on what came back |
| `sequential` | A second call that must use the first call's result |

Failures are classified: `no-tool-call`, `wrong-tool`, `bad-args`,
`unexpected-tool`, `empty`, `error`. Two extra flags are tracked per turn:

- **stray markup** - `<think>`, `</think>`, `<|im_start|>` and friends leaking
  into assistant content. This is what shows up in a harness as "weird
  characters."
- **bad chars** - control characters or replacement runes inside tool
  arguments, which break a harness applying an edit verbatim.
- **repeated calls** - the model reissuing a call it already made. That is
  looping rather than recovering, and it is the countable form of "kept making
  mistakes it had to correct".

A scenario counts as fully passed only if *every* turn passed, so a model that
opens well and then derails is not credited.

## Coding eval

`ollama-bench eval` asks each model to complete a Python function, extracts the
code from the response, runs it against the problem's unit tests in a sandbox,
and reports pass@1 plus a breakdown of *why* each failure failed.

```bash
./ollama-bench eval -models qwen3-coder:latest       # bundled 18-problem set
./ollama-bench eval -problems HumanEval.jsonl        # the real thing
./ollama-bench eval -samples 5 -temperature 0.4      # pass@k
./ollama-bench eval -models a,b -min-pass 50         # exit 1 below 50%
```

**Problem set.** 18 original problems ship inside the binary, so the eval runs
offline with no download. They are original rather than borrowed precisely
because published benchmark problems are likely in these models' training data.
The format is HumanEval's JSONL (`task_id`, `prompt`, `entry_point`, `test`), so
`-problems HumanEval.jsonl` points it at the real 164-problem set and produces
directly comparable pass@1 numbers.

**Outcomes** are separated, because "wrong" and "broken" are different failures:

| Outcome | Meaning |
|---|---|
| `pass` | Code ran, all assertions held |
| `wrong-answer` | Code ran, an assertion failed - the model reasoned incorrectly |
| `error` | Code could not run: syntax error, exception, missing import |
| `timeout` | Exceeded the execution limit, usually an accidental infinite loop or an exponential algorithm |
| `no-code` | No function found in the response |
| `generation-error` | The request to Ollama itself failed |

**pass@k.** With `-samples 1` (default) generation is greedy and pass@1 is just
the pass rate. With `-samples k > 1` sampling is enabled and pass@k uses the
unbiased estimator from the Codex paper, `1 - C(n-c, k)/C(n, k)`.

**Sandboxing.** Executing model-generated code is the one genuinely dangerous
part of this, so isolation is layered and the level *actually achieved* is
printed and stored in the results rather than assumed:

- a network namespace via `unshare -rn`, where the host allows unprivileged user
  namespaces (verified by the test suite actually attempting a connection),
- CPU-seconds, address-space, file-size and file-descriptor caps via `prlimit`,
- `python3 -I` - no user site-packages, no environment influence, no implicit
  cwd on `sys.path`,
- a scratch working directory, a minimal environment, and a hard wall-clock
  timeout that kills the whole process group, not just the direct child.

Each layer is probed at startup and degrades explicitly. If nothing is
available the run prints a prominent warning before executing anything.

**Trusting the harness.** Every bundled problem ships with a reference solution,
and `go test` runs all of them through the real sandbox and requires 100%. If
that self-test fails, a model's bad score says nothing about the model. The test
suite also asserts that a wrong answer, a syntax error, a runtime exception and
an infinite loop are each classified correctly, and that the network is actually
unreachable from inside the sandbox.

## File editing

Generating a function from scratch and surgically editing one that already
exists are different skills, and only the second explains a harness that
"struggles editing files". The `edit` suite hands the model a real file plus a
change request, requires aider/opencode-style SEARCH/REPLACE blocks, applies the
result, and runs tests against it.

Eight tasks scale from a one-line constant change through multi-hunk edits,
renames across call sites, indentation-sensitive changes inside nested blocks,
and disambiguating between two near-identical functions.

The headline metric is **attempts per success**. On a malformed or non-applying
edit the failure is fed back and the model retries (up to `max_attempts`). A
model that always lands it first time behaves very differently inside an agent
loop from one that needs two corrections, even when both eventually succeed.

Failures are separated because they mean different things:

| Outcome | Meaning |
|---|---|
| `parse-failure` | The block was malformed - a formatting failure |
| `apply-failure` | SEARCH text absent, or matching several places (too little context to be unique) |
| `test-failure` | Applied cleanly but the change was wrong |
| `drift` | Code outside the requested region changed - dangerous in a real repo |

Every task ships a reference edit, and `go test` requires all of them to apply
and pass. A broken task would make a model's failure meaningless.

## Validating a problem set

```bash
ollama-bench eval -validate                                  # bundled set
ollama-bench eval -validate -problems problems/HumanEval.jsonl
```

Runs every reference solution against its own tests and exits non-zero if any
fail. Do this before trusting a score from an unfamiliar problem file - a
mismatched format silently produces garbage rather than an error. Both shipped
sets validate 100% (18/18 bundled, 164/164 HumanEval).

## Why the chat endpoint

Every content-graded suite (`accuracy`, `tools`, `eval`) runs through
`/api/chat`, not `/api/generate`. On `/api/generate` a model whose chat
template pre-injects `<think>` emits its reasoning into the *response* field
with a stray `</think>`, and exact-output graders then score the reasoning
instead of the answer. Measured on this repo's models, that artifact cost one
model every single instruction-following task despite it answering correctly.
`/api/chat` separates `thinking` from `content` for every model tested.

## Measurement hygiene

These matter more than the metric formulas, because getting them wrong produces
numbers that look plausible and are wrong:

- **Warmup per model.** The first request pays model load. It is discarded.
- **Settle per condition.** A tiny discarded request runs before each condition.
  Without it the first measurement after a large-context request carries that
  request's teardown - measured here as a 5-10x inflated TTFT, enough to report
  6.1x concurrency scaling on hardware that does not scale at all.
- **Condition-first iteration.** All repeats of one condition run back to back,
  so a neighbouring condition's cache state cannot bleed into the sample.
- **Unique filler per iteration.** Prefill and depth prompts are seeded from
  (model, suite, label, iteration). Reusing a prompt would let Ollama's prompt
  cache serve it from a prefix and report fictitious prefill throughput.
- **Skip what does not fit.** A depth target above 95% of the loaded `num_ctx`
  is skipped and logged. Ollama truncates an over-long prompt silently, which
  would turn a "128k depth" measurement into an unlabelled 32k one.
- **Actual, not estimated, token counts.** Filler length is estimated to hit a
  target, but every report shows the `prompt_eval_count` the server returned.
- **GPU residency check.** `/api/ps` `size_vram` vs `size` is captured per model
  and warned about below 99%. A model that spilled to system RAM explains a bad
  decode number that would otherwise look like a model property.
- **`temperature: 0` and a fixed seed** by default, so output length is stable
  and run-to-run differences belong to the server, not the sampler.

## Output

The terminal report is a headline table plus one table per suite. Every run also
writes:

- `results/<timestamp>.json` - full records, summaries, and the environment
  (Ollama version, host, CPU/RAM, git SHA + dirty flag, per-model quant, params,
  `num_ctx` and GPU residency). Enough to reproduce or discount the run later.
- `results/<timestamp>.csv` - one row per request, for a spreadsheet or a plot.
- `results/<timestamp>.md` with `-markdown`.

## Comparing runs

```bash
ollama-bench compare results/A.json results/B.json -threshold 10
```

Joins on (model, suite, label, concurrency) and reports the delta on the metric
each suite measures - prefill throughput for the prefill suite, decode elsewhere
- plus TTFT. Deltas are signed so negative is always worse. Single-sample cells
are marked informational rather than flagged. Exit status is 1 if anything
regressed beyond the threshold, so it works as a gate after a driver, quant, or
Modelfile change.

Eval runs are compared too: the pass rate per model, plus a table of individual
problems that changed verdict. A pass rate that holds steady while different
problems break is worth seeing.

## Flags

```
-config PATH     JSON config (// comments allowed); see ../bench.json
-models a,b      models to benchmark, overriding the config
-suites a,b      latency,prefill,depth,concurrency,sanity
-repeat N        iterations per condition (default 3)
-think LEVEL     low|medium|high|off; ignored for models without the capability
-num-ctx N       override num_ctx for every model
-url URL         Ollama base URL (default http://localhost:11434)
-out DIR         output directory (default results/, empty to write no files)
-markdown        also write a markdown report
-timeout N       per-request timeout in seconds (default 1800)
-no-warmup       skip the discarded warmup request
-keep-loaded     do not unload between models (skips cold-load timing)
-cooldown N      seconds to idle between models (default 3)
-quiet           suppress per-request progress lines
```

Ctrl-C cancels in flight work and still writes whatever was measured.

## Eval flags

```
-problems PATH    HumanEval-format JSONL (default: the bundled set)
-samples N        samples per problem; >1 enables pass@k and sampling (default 1)
-temperature F    sampling temperature, used only when -samples > 1 (default 0.2)
-num-predict N    max tokens per solution (default 1024)
-exec-timeout N   seconds allowed for each candidate to run its tests (default 15)
-limit N          run only the first N problems
-min-pass F       exit non-zero if any model scores below this pass@1 percentage
```

`-models`, `-config`, `-out`, `-markdown`, `-think`, `-url` and `-quiet` work as
they do for the performance run.

## Tests

```bash
go test ./...          # includes sandbox execution
go test -short ./...   # skips anything that executes python
```

Covers the statistics, the config comment stripper, the sanity verifiers, the
filler determinism the prefill numbers depend on, code extraction from chat
responses, the pass@k estimator, and the sandbox self-test described above.
