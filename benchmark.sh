#!/usr/bin/env sh
# Wrapper around bench/ollama-bench.
#
#   ./benchmark.sh                     performance + accuracy + tool-calling
#   ./benchmark.sh eval                coding eval (sandboxed, executes code)
#   ./benchmark.sh list                what the server has
#   ./benchmark.sh compare A.json B.json
#
# Anything after the subcommand is passed through, so flags still work:
#   ./benchmark.sh -suites accuracy,tools
#   ./benchmark.sh eval -samples 5

set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="$HERE/bench/ollama-bench"

# --- models to benchmark ---------------------------------------------------
# Edit this list, or override per run:  MODELS=gemma4:latest ./benchmark.sh
MODELS="${MODELS:-${MODEL:-ornith1.5:latest,qwen3.8:latest}}"

# Others available locally - add them to MODELS above as needed:
#   qwen3-coder:latest      qwen3.8:27b        gemma4:latest
#   ornith:latest           qwen3-coder:30b    gemma4:26b
#   llama-vision:latest     llama3.2:1b
# ---------------------------------------------------------------------------

SUITES="${SUITES:-latency,prefill,depth,concurrency,sanity,accuracy,tools}"
ITERATIONS="${ITERATIONS:-3}"
OUT="${OUT:-$HERE/results}"

# Thinking level for thinking models: "" (model default), low, medium, high, off.
THINK="${THINK:-}"
case "$THINK" in
    ""|low|medium|high|max|off) ;;
    *) echo "Invalid THINK: $THINK (low|medium|high|max|off)" >&2; exit 1 ;;
esac

if [ ! -x "$BIN" ]; then
    echo "building ollama-bench..." >&2
    (cd "$HERE/bench" && go build -o ollama-bench .)
fi

# Subcommands that take their own arguments are passed straight through.
case "${1:-}" in
    list|compare)
        exec "$BIN" "$@"
        ;;
    eval)
        shift
        exec "$BIN" eval -models "$MODELS" -out "$OUT" \
            ${THINK:+-think "$THINK"} "$@"
        ;;
esac

exec "$BIN" \
    -models "$MODELS" \
    -suites "$SUITES" \
    -repeat "$ITERATIONS" \
    -out "$OUT" \
    ${THINK:+-think "$THINK"} \
    "$@"
