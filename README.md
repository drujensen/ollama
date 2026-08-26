# Custom Models for Ollama

This is how you create custom models:

```bash
ollama create qwen3-coder:30b-32k -f qwen3-coder.custom
ollama create devstral:24b-32k -f devstral.custom
ollama create gpt-oss:20b-32k -f gpt-oss.custom
```

## Benchmarking

`bench/` holds `ollama-bench`, a Go tool that answers two separate questions.

**How fast is it** - time-to-first-token, prefill and decode throughput reported
separately, decode speed vs KV cache depth, and concurrency scaling.

**Is it right** - 18 auto-graded accuracy tasks across six groups, a multi-turn
tool-calling suite, and a coding eval that executes generated code against unit
tests in a sandbox.

`benchmark.sh` is the entry point. Edit the `MODELS` list at the top, or
override it per run:

```bash
./benchmark.sh                            # perf + accuracy + tool calling
MODELS=qwen3-coder:latest ./benchmark.sh  # one model
./benchmark.sh -suites accuracy,tools     # just the quality suites
./benchmark.sh eval                       # coding eval (executes code)
./benchmark.sh list                       # what the server has
./benchmark.sh compare A.json B.json      # diff two runs
```

Environment overrides: `MODELS`, `SUITES`, `ITERATIONS`, `THINK`, `OUT`.
Results land in `results/` as JSON + CSV, diffable with `compare`.

See [bench/README.md](bench/README.md) for the metrics, the suites, and the
measurement hygiene that keeps the numbers honest. Model and suite defaults
live in [bench.json](bench.json).
