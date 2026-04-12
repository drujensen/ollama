#!/usr/bin/env bash
#MODEL="qwen3-5:latest"
MODEL="qwen3-coder:latest"
#MODEL="gemma4:latest"
#MODEL="deepseek:8b-32k"
#MODEL="llama:8b-32k"
#MODEL="gpt-oss:20b-32k"
#MODEL="gemma3:27b-32k"
#MODEL="devstral:24b-32k"
#MODEL="phi4-mini:3.8b-32k"
#MODEL="ministral:8b-32k"

PROMPT='tell me a story about a frog and a princess'
ITERATIONS=1

echo "=== OLLAMA BENCHMARK DEBUGGING ==="
echo "Model: $MODEL"
echo "Prompt: $PROMPT"
echo ""

TOTAL_TOTAL_DURATION=0
TOTAL_PROMPT_EVAL_COUNT=0
TOTAL_EVAL_COUNT=0
TOTAL_RESPONSES=0

for i in $(seq 1 $ITERATIONS); do
    echo "=== Request $i ==="
    
    # Test generate endpoint
    echo "Testing /api/generate endpoint..."
    
    # Measure request time
    start_time=$(date +%s%3N)
    
    response_generate=$(curl -s -X POST http://localhost:11434/api/generate \
        -H "Content-Type: application/json" \
        -d "{\"model\":\"$MODEL\",\"prompt\":\"$PROMPT\",\"stream\":false}")
    
    end_time=$(date +%s%3N)
    
    # Parse response for token counts and duration
    total_duration=$(echo "$response_generate" | jq -r '.total_duration' 2>/dev/null || echo "0")
    prompt_eval_count=$(echo "$response_generate" | jq -r '.prompt_eval_count' 2>/dev/null || echo "0")
    eval_count=$(echo "$response_generate" | jq -r '.eval_count' 2>/dev/null || echo "0")
    response_text=$(echo "$response_generate" | jq -r '.response' 2>/dev/null || echo "No response")
    
    # Calculate response time in milliseconds
    duration=$((end_time - start_time))
    
    # Accumulate totals
    TOTAL_TOTAL_DURATION=$((TOTAL_TOTAL_DURATION + total_duration))
    TOTAL_PROMPT_EVAL_COUNT=$((TOTAL_PROMPT_EVAL_COUNT + prompt_eval_count))
    TOTAL_EVAL_COUNT=$((TOTAL_EVAL_COUNT + eval_count))
    TOTAL_RESPONSES=$((TOTAL_RESPONSES + 1))
    
    echo "Request $i took $duration ms"
    echo "Response details:"
    echo "  total_duration: $total_duration"
    echo "  prompt_eval_count: $prompt_eval_count"
    echo "  eval_count: $eval_count"
    echo "  total_tokens: $((prompt_eval_count + eval_count))"
    echo "  response: $response_text"
    echo ""
done

if [ $TOTAL_RESPONSES -gt 0 ]; then
    # Calculate total tokens and TPS
    total_tokens=$((TOTAL_PROMPT_EVAL_COUNT + TOTAL_EVAL_COUNT))
    total_duration_ms=$((TOTAL_TOTAL_DURATION / 1000000))  # Convert nanoseconds to milliseconds
    avg_duration_ms=$((total_duration_ms / TOTAL_RESPONSES))
    
    # Calculate tokens per second
    if [ $total_duration_ms -gt 0 ]; then
        tps=$(echo "scale=2; $total_tokens * 1000 / $total_duration_ms" | bc)
    else
        tps=0
    fi
    
    echo "=== FINAL RESULTS ==="
    echo "Total requests: $TOTAL_RESPONSES"
    echo "Total time:   $total_duration_ms ms"
    echo "Average time: $avg_duration_ms ms"
    echo "Total tokens: $total_tokens"
    echo "Tokens per second: $tps"
else
    echo "Error: No valid responses received"
fi
