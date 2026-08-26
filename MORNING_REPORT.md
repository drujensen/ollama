# Morning report — 2026-08-23 overnight

## TL;DR

**Ollama is wedged and needs one command from you:**

```bash
sudo systemctl restart ollama
```

An orphaned `llama-server` (pid 21870, owned by user `ollama`) is holding
**49 GB of the 62.5 GB GPU pool** and has jammed the scheduler. Even a 1.2 GB
model times out. I could not clear it: `kill` returns *Operation not permitted*,
and `systemctl restart` needs a polkit password prompt I cannot answer.

**A watcher is already running.** It polls every 5 minutes for up to 10 hours,
and the moment Ollama is healthy it runs the full comparison automatically:

```
/tmp/claude-1000/.../scratchpad/autorun.sh   ->   autorun.log
```

So restarting Ollama is sufficient — results will be waiting. Nothing else is
needed from you.

The machine itself is fine: **zero kernel errors all evening**, taint still 128
(the pre-existing boot-time `gst-plugin-scan` oops, unrelated to us).

## What happened to the eviction test

**Inconclusive — it never reached the eviction step.**

Loading the 52 GB model cold takes far longer than the 5.2 s warm load I had
measured, and my first attempt used a 10-minute timeout. When curl disconnected,
Ollama logged `Load failed` but **left the llama-server running and holding its
GPU memory**. Every later attempt then queued behind that orphan.

That is itself a finding worth keeping:

> Aborting a model load mid-flight leaves an orphaned `llama-server` holding the
> full GPU allocation, and Ollama neither reclaims it nor reports it in
> `/api/ps`. The scheduler wedges behind it.

For opencode this matters: a cancelled request during a large model load can
take the whole server down until it is restarted.

The eviction question — *does the stable kernel fix the TTM eviction crash?* —
remains **open**. The test is written and ready (`scratchpad/eviction.sh`); it
just needs a working server and a warm model.

## What was completed

All three planned enhancements are implemented, unit-tested, and documented.
**58 tests pass**, vet clean, 6,029 lines.

### 1. File-editing suite (`-suites edit`) — the one aimed at your opencode problem

Hands the model a real file plus a change request, requires aider/opencode-style
SEARCH/REPLACE blocks, applies the result, and runs tests against it.

Eight tasks: one-line constant, off-by-one fix, multi-hunk, adding a function
without touching neighbours, rename across call sites, indentation-sensitive
change in a nested block, disambiguating two near-identical functions, and
extracting a shared helper.

**Headline metric: attempts per success.** On a malformed or non-applying edit
the error is fed back and the model retries. That is the countable form of
"kept making mistakes it had to correct".

Failures are separated: `parse-failure` (malformed block), `apply-failure`
(SEARCH absent or matching several places), `test-failure` (applied but wrong),
plus a **drift** counter for code changed outside the requested region.

All eight reference edits are validated in the sandbox by `go test`.

### 2. Harder problem sets + validation

- Downloaded **HumanEval (164 problems)** — `bench/problems/HumanEval.jsonl`
- New `eval -validate` mode runs every reference solution against its own tests
- Both sets verified: **18/18 bundled, 164/164 HumanEval**

Validation matters because a mismatched problem file produces garbage silently
rather than erroring. HumanEval stores a bare *completion* where our set stores
a full solution; the loader now handles both shapes.

### 3. Hardened tool suite — 7 scenarios to 11

New: `error-recovery` (a tool returns an error — does it change approach or
reissue the identical failing call), `distractors` (13 tools, one fits),
`long-horizon` (four dependent turns: list, read, fix, test), `state-tracking`
(a later turn needs a value only present in an earlier result).

New metric: **repeated calls**, counting a model reissuing a call it already
made — looping rather than recovering.

### Also fixed

- `format-1` asked for "title case", which is ambiguous: Chicago and AP lowercase
  articles mid-title, so qwen3-coder was marked wrong for being *more* correct.
  The prompt now states the capitalization rule explicitly.
- `untouchedDrift` used substring matching, so `"return 2"` matched inside
  `"return 22"` and drift went undetected. Now matches on line boundaries.
  (Caught by a test written before the implementation.)

## Standing results (before the wedge)

Three-way comparison at 262k context, all suites, kernel clean:

| | qwen3-coder | ornith1.5 | qwen3.8 |
|---|---:|---:|---:|
| Decode | 59.2 tok/s | **73.6** | 26.1 |
| TTFT | 846 ms | 707 ms | **294 ms** |
| Inter-token p95 | 17.5 ms | **14.2 ms** | 125 ms |
| Accuracy | 94.4% | **100%** | **100%** |
| Tool calling (old, easy) | 100% | 100% | 100% |
| Coding eval (old, easy) | **100%**, 158 tok | 100%, 348 tok | 100%, 486 tok |

Your instinct was right that these do not separate the models. The new suites
are built precisely to.

## What I expect the new suites to show

A genuine prediction, recorded before seeing results so it can be wrong:

- **The edit suite will separate them.** ornith wins every throughput and
  accuracy measure yet failed you in opencode, and file editing is the one
  skill nothing tested. If `mean attempts` is meaningfully above 1.0 for ornith
  and near 1.0 for qwen3-coder, that explains your experience directly.
- **HumanEval will stay near-saturated.** 164 easy-to-moderate problems will
  likely leave all three above 90%. If so, the next step is EvalPlus or
  LiveCodeBench, not more HumanEval.
- **`error-recovery` and `long-horizon` are where tool calling should crack.**
  Two-turn happy paths were always going to be 100%.

If the edit suite also comes back 100% across the board, then the opencode
failure is not edit-format competence and the next suspect is context length or
prompt-cache invalidation over a long session.
