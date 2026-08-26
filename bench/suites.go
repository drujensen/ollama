package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// ConcBatch is one concurrency level executed once: N requests in flight at the
// same time. Aggregate throughput is a property of the batch, not of any single
// request, so it is recorded separately.
type ConcBatch struct {
	Model        string  `json:"model"`
	Level        int     `json:"level"`
	Iter         int     `json:"iter"`
	WallMS       float64 `json:"wall_ms"`
	EvalTokens   int     `json:"eval_tokens"`
	AggregateTPS float64 `json:"aggregate_tps"`
	PerStreamTPS float64 `json:"per_stream_tps"`
	OK           int     `json:"ok"`
	Failed       int     `json:"failed"`
}

// depthFits skips a target that would overflow the loaded context window.
// Ollama truncates an over-long prompt silently, which would turn a "128k
// depth" measurement into an unlabelled 32k one.
func (r *Runner) depthFits(info ModelInfo, need int, suite string) bool {
	if info.NumCtx <= 0 || need <= 0 {
		return true
	}
	// 5% headroom for the wrapper text and the chat template.
	if float64(need) <= float64(info.NumCtx)*0.95 {
		return true
	}
	key := fmt.Sprintf("%s/%s/%d", info.Name, suite, need)
	if r.skipped[key] {
		return false
	}
	if r.skipped == nil {
		r.skipped = map[string]bool{}
	}
	r.skipped[key] = true
	r.logf("    %-10s %-14s skipped: needs %s tokens, model context is %s",
		suite, tokLabel(need), tokLabel(need), tokLabel(info.NumCtx))
	return false
}

// settle issues one tiny discarded request. Without it the first measurement
// after a large-context request carries that request's teardown, which showed up
// as a 5-10x inflated TTFT and made concurrency scaling meaningless.
func (r *Runner) settle(ctx context.Context, m ModelCfg) {
	if ctx.Err() != nil {
		return
	}
	r.exec(ctx, m, runSpec{suite: "settle", label: "settle", prompt: "ok", numPredict: 1})
}

// Suites iterate condition-first, not iteration-first: all repeats of one
// condition run back to back after a single settle, so cache state from a
// neighbouring condition cannot bleed into the sample.
func (r *Runner) RunLatency(ctx context.Context, m ModelCfg) {
	for _, p := range r.cfg.Latency.Prompts {
		r.settle(ctx, m)
		for iter := 1; iter <= r.cfg.Repeat; iter++ {
			rec := r.exec(ctx, m, runSpec{
				suite:      "latency",
				label:      p.Label,
				prompt:     p.Text,
				numPredict: r.cfg.Latency.NumPredict,
				iter:       iter,
			})
			r.add(rec)
			r.line(rec)
		}
	}
}

// RunPrefill measures prompt-processing throughput as a function of prompt
// length. num_predict is tiny so decode barely contributes.
func (r *Runner) RunPrefill(ctx context.Context, m ModelCfg, info ModelInfo) {
	for _, d := range r.cfg.Prefill.Depths {
		if !r.depthFits(info, d, "prefill") {
			continue
		}
		label := fmt.Sprintf("%s-prompt", tokLabel(d))
		r.settle(ctx, m)
		for iter := 1; iter <= r.cfg.Repeat; iter++ {
			rec := r.exec(ctx, m, runSpec{
				suite:       "prefill",
				label:       label,
				prompt:      docPrompt(m.Name, "prefill", label, iter, d, "In one sentence, what is the overall topic of the notes above?"),
				numPredict:  r.cfg.Prefill.NumPredict,
				iter:        iter,
				targetDepth: d,
			})
			r.add(rec)
			r.line(rec)
		}
	}
}

// RunDepth measures decode throughput with a filled KV cache. This is the curve
// that separates long-context models on constrained hardware, and the old
// script had no visibility into it at all.
func (r *Runner) RunDepth(ctx context.Context, m ModelCfg, info ModelInfo) {
	for _, d := range r.cfg.Depth.Depths {
		if !r.depthFits(info, d+r.cfg.Depth.NumPredict, "depth") {
			continue
		}
		label := fmt.Sprintf("@%s", tokLabel(d))
		r.settle(ctx, m)
		for iter := 1; iter <= r.cfg.Repeat; iter++ {
			prompt := "Explain how a CPU cache works."
			if d > 0 {
				prompt = docPrompt(m.Name, "depth", label, iter, d, "Summarize the notes above in about 200 words.")
			}
			rec := r.exec(ctx, m, runSpec{
				suite:       "depth",
				label:       label,
				prompt:      prompt,
				numPredict:  r.cfg.Depth.NumPredict,
				iter:        iter,
				targetDepth: d,
			})
			r.add(rec)
			r.line(rec)
		}
	}
}

// RunConcurrency measures how throughput scales, and how per-request latency
// degrades, when several requests are in flight at once.
func (r *Runner) RunConcurrency(ctx context.Context, m ModelCfg) []ConcBatch {
	var out []ConcBatch
	reps := r.cfg.Concurrency.Repeat
	if reps < 1 {
		reps = 1
	}
	for iter := 1; iter <= reps; iter++ {
		for _, level := range r.cfg.Concurrency.Levels {
			label := fmt.Sprintf("x%d", level)
			r.settle(ctx, m)
			recs := make([]Record, level)
			var wg sync.WaitGroup
			start := time.Now()
			for i := 0; i < level; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					// Each stream gets a distinct nonce so the server's prompt
					// cache cannot serve one stream from another's prefix.
					prompt := fmt.Sprintf("Request %d of %d (run %d). Explain how a CPU cache works.", i+1, level, iter)
					recs[i] = r.exec(ctx, m, runSpec{
						suite:       "concurrency",
						label:       label,
						prompt:      prompt,
						numPredict:  r.cfg.Concurrency.NumPredict,
						iter:        iter,
						concurrency: level,
					})
				}(i)
			}
			wg.Wait()
			batch := ConcBatch{Model: m.Name, Level: level, Iter: iter,
				WallMS: float64(time.Since(start).Microseconds()) / 1000}
			var perStream float64
			for _, rec := range recs {
				r.add(rec)
				if rec.OK {
					batch.OK++
					batch.EvalTokens += rec.EvalTokens
					perStream += rec.DecodeTPS()
				} else {
					batch.Failed++
				}
			}
			if batch.WallMS > 0 {
				batch.AggregateTPS = float64(batch.EvalTokens) * 1000 / batch.WallMS
			}
			if batch.OK > 0 {
				batch.PerStreamTPS = perStream / float64(batch.OK)
			}
			out = append(out, batch)
			r.logf("    %-10s %-14s aggregate %6.1f tok/s   per-stream %6.1f tok/s   %d ok / %d failed",
				"concurrency", label, batch.AggregateTPS, batch.PerStreamTPS, batch.OK, batch.Failed)
		}
	}
	return out
}

// sanityCheck is a cheap pass/fail signal. It does not score model quality; it
// catches the case where a model benchmarks fast because it is producing
// garbage.
type sanityCheck struct {
	name    string
	prompt  string
	format  string
	verify  func(resp string) (bool, string)
	predict int
}

func sanityChecks(cfg SanityCfg) []sanityCheck {
	return []sanityCheck{
		{
			name:    "bash-syntax",
			prompt:  "Write a bash script that lists the 5 largest files in the current directory, sorted by size. Reply with the script in a single fenced code block and nothing else.",
			predict: cfg.NumPredict,
			verify:  verifyBash,
		},
		{
			name:    "json-format",
			prompt:  "Return a JSON object with keys \"name\" (string) and \"port\" (number) describing an HTTP server listening on port 8080.",
			format:  "json",
			predict: cfg.NumPredict,
			verify:  verifyJSON,
		},
		{
			name:    "instruction",
			prompt:  "Reply with exactly the single word PONG and nothing else.",
			predict: cfg.NumPredict,
			verify:  verifyPong,
		},
	}
}

func (r *Runner) RunSanity(ctx context.Context, m ModelCfg) {
	r.settle(ctx, m)
	for _, c := range sanityChecks(r.cfg.Sanity) {
		// Chat endpoint, like the other content-graded suites: on
		// /api/generate a model whose template pre-injects <think> returns its
		// reasoning in the response field, and these checks would score that
		// instead of the answer.
		rec, res := r.execChat(ctx, m, chatSpec{
			suite:      "sanity",
			label:      c.name,
			messages:   []ChatMessage{{Role: "user", Content: c.prompt}},
			format:     c.format,
			numPredict: c.predict,
			iter:       1,
		})
		rec.CheckName = c.name
		if rec.OK {
			passed, detail := c.verify(res.Content)
			rec.CheckPassed = &passed
			rec.CheckDetail = detail
			if !passed && rec.Truncated() {
				rec.CheckDetail = "response truncated at num_predict: " + detail
			}
		}
		r.add(rec)
		if r.quiet {
			continue
		}
		status := "ERROR"
		detail := rec.Err
		if rec.CheckPassed != nil {
			if *rec.CheckPassed {
				status = "PASS"
			} else {
				status = "FAIL"
			}
			detail = rec.CheckDetail
		}
		r.logf("    %-10s %-14s %-5s %s", "sanity", c.name, status, detail)
	}
}

// tokLabel renders a token count compactly: 16000 -> "16k".
func tokLabel(n int) string {
	switch {
	case n == 0:
		return "0"
	case n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// docPrompt builds a synthetic document of roughly targetTokens tokens plus a
// question. The filler is unique per (model, suite, label, iteration) so that
// Ollama's prompt cache cannot short-circuit prefill and report a fictitious
// throughput.
func docPrompt(model, suite, label string, iter, targetTokens int, question string) string {
	if targetTokens <= 0 {
		return question
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%s|%d", model, suite, label, iter)
	seed := h.Sum64()

	var b strings.Builder
	fmt.Fprintf(&b, "Notes archive %016x. Read the notes below, then answer the question at the end.\n\n", seed)
	b.WriteString(filler(seed, targetTokens))
	b.WriteString("\n\nQuestion: ")
	b.WriteString(question)
	return b.String()
}

// filler emits pseudo-random English-like prose. Common words are usually one
// token each, so the word count is scaled by an empirical tokens-per-word
// factor. The report always shows the actual prompt_eval_count the server
// returned, never this estimate.
func filler(seed uint64, targetTokens int) string {
	// Calibrated against measured prompt_eval_count; the report always shows
	// the actual server-side count, never this estimate.
	const tokensPerWord = 1.10
	words := int(float64(targetTokens) / tokensPerWord)
	if words < 1 {
		words = 1
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	var b strings.Builder
	b.Grow(words * 7)
	sentence := 0
	target := 8 + rng.IntN(12)
	for i := 0; i < words; i++ {
		w := fillerWords[rng.IntN(len(fillerWords))]
		if sentence == 0 {
			b.WriteString(strings.ToUpper(w[:1]) + w[1:])
		} else {
			b.WriteByte(' ')
			b.WriteString(w)
		}
		sentence++
		if sentence >= target {
			b.WriteString(". ")
			sentence = 0
			target = 8 + rng.IntN(12)
		}
	}
	if sentence > 0 {
		b.WriteByte('.')
	}
	return b.String()
}

var fillerWords = strings.Fields(`
the quantity of engineering practice depends on careful measurement and honest
reporting across every layer of a running system from storage through memory
into the processor pipeline where latency and throughput are traded against
each other under load a cache line is fetched once and reused many times when
locality holds but scattered access defeats prediction and stalls the pipeline
for hundreds of cycles meanwhile the scheduler balances threads across cores
and the allocator hands out pages that may or may not be resident so profiles
must be gathered under realistic conditions rather than synthetic loops the
network adds its own variance since packets queue at every hop and congestion
control reacts to loss long after the sender has moved on therefore any
benchmark that reports a single average number hides more than it reveals and
a distribution with percentiles tells a far more useful story about what users
actually experience during peak traffic on shared hardware where neighbours
compete for bandwidth cache capacity and memory controllers while the operating
system migrates work between sockets adding further noise to already noisy
samples so repeat the run warm the caches discard the first result and publish
the spread alongside the median value with units clearly labelled
`)
