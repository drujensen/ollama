package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

type Runner struct {
	cfg *Config
	cl  *Client

	mu   sync.Mutex
	recs []Record

	skipped map[string]bool
	quiet   bool

	// Set by suites that execute generated code.
	sandbox     Sandbox
	editTimeout time.Duration
}

func NewRunner(cfg *Config, cl *Client, quiet bool) *Runner {
	return &Runner{cfg: cfg, cl: cl, quiet: quiet}
}

func (r *Runner) add(rec Record) {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
}

func (r *Runner) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.recs...)
}

type runSpec struct {
	suite       string
	label       string
	prompt      string
	numPredict  int
	iter        int
	concurrency int
	targetDepth int
	format      string
}

// exec issues one streaming request and turns it into a Record. Everything the
// report needs is derived here; nothing is recomputed later.
func (r *Runner) exec(ctx context.Context, m ModelCfg, spec runSpec) Record {
	opts := mergeOptions(r.cfg.Options, m.Options)
	if spec.numPredict > 0 {
		if opts == nil {
			opts = map[string]any{}
		}
		opts["num_predict"] = spec.numPredict
	}

	req := GenerateRequest{
		Model:   m.Name,
		Prompt:  spec.prompt,
		Stream:  true,
		Think:   m.Think,
		Format:  spec.format,
		Options: opts,
	}

	rec := Record{
		Model:       m.Name,
		Suite:       spec.suite,
		Label:       spec.label,
		Iter:        spec.iter,
		Concurrency: spec.concurrency,
		TargetDepth: spec.targetDepth,
		StartedAt:   time.Now(),
	}

	var (
		start     = time.Now()
		firstTok  time.Time
		lastTok   time.Time
		gaps      []float64
		text      []byte
		thinkChar int
	)

	final, err := r.cl.Generate(ctx, req, func(ch Chunk, at time.Time) {
		if ch.Thinking != "" {
			thinkChar += len(ch.Thinking)
		}
		// A frame counts as a token for timing only if it carried text.
		if ch.Response == "" && ch.Thinking == "" {
			return
		}
		if firstTok.IsZero() {
			firstTok = at
		} else {
			gaps = append(gaps, float64(at.Sub(lastTok).Microseconds())/1000)
		}
		lastTok = at
		text = append(text, ch.Response...)
	})
	wall := time.Since(start)

	rec.WallMS = float64(wall.Microseconds()) / 1000
	if err != nil {
		rec.Err = err.Error()
		return rec
	}

	rec.OK = true
	if !firstTok.IsZero() {
		rec.TTFTMS = float64(firstTok.Sub(start).Microseconds()) / 1000
	}
	rec.ServerTotalMS = nsToMS(final.TotalDuration)
	rec.LoadMS = nsToMS(final.LoadDuration)
	rec.PromptTokens = final.PromptEvalCount
	rec.PromptEvalMS = nsToMS(final.PromptEvalDuration)
	rec.EvalTokens = final.EvalCount
	rec.EvalMS = nsToMS(final.EvalDuration)
	rec.DoneReason = final.DoneReason
	rec.ResponseChars = len(text)
	rec.ThinkingChars = thinkChar
	rec.ClientOverhead = rec.WallMS - rec.ServerTotalMS

	if len(gaps) > 0 {
		s := Summarize(gaps)
		rec.InterTokenP50 = s.Median
		rec.InterTokenP95 = s.P95
	}
	rec.responseText = string(text)
	return rec
}

func nsToMS(ns int64) float64 { return float64(ns) / 1e6 }

func (r *Runner) logf(format string, a ...any) {
	if r.quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

// line prints one compact result line while the run is in progress.
func (r *Runner) line(rec Record) {
	if r.quiet {
		return
	}
	if !rec.OK {
		fmt.Fprintf(os.Stderr, "    %-10s %-14s FAILED: %s\n", rec.Suite, rec.Label, rec.Err)
		return
	}
	trunc := ""
	if rec.Truncated() {
		trunc = " [truncated]"
	}
	fmt.Fprintf(os.Stderr,
		"    %-10s %-14s ttft %7.0fms  prefill %8.1f tok/s (%d tok)  decode %6.1f tok/s (%d tok)%s\n",
		rec.Suite, rec.Label, rec.TTFTMS, rec.PrefillTPS(), rec.PromptTokens, rec.DecodeTPS(), rec.EvalTokens, trunc)
}

// chatSpec describes one /api/chat turn.
type chatSpec struct {
	suite      string
	label      string
	messages   []ChatMessage
	tools      []Tool
	format     string
	numPredict int
	iter       int
}

// execChat is the /api/chat counterpart of exec. Suites that grade output
// content use this, because /api/generate leaks reasoning into the response
// for models whose template pre-injects a thinking tag, which would score the
// model on its own reasoning text.
func (r *Runner) execChat(ctx context.Context, m ModelCfg, spec chatSpec) (Record, ChatResult) {
	opts := mergeOptions(r.cfg.Options, m.Options)
	if spec.numPredict > 0 {
		if opts == nil {
			opts = map[string]any{}
		}
		opts["num_predict"] = spec.numPredict
	}

	req := ChatRequest{
		Model:    m.Name,
		Messages: spec.messages,
		Think:    m.Think,
		Format:   spec.format,
		Tools:    spec.tools,
		Options:  opts,
	}

	rec := Record{
		Model:     m.Name,
		Suite:     spec.suite,
		Label:     spec.label,
		Iter:      spec.iter,
		StartedAt: time.Now(),
	}

	start := time.Now()
	res, err := r.cl.Chat(ctx, req, nil)
	wall := time.Since(start)
	rec.WallMS = float64(wall.Microseconds()) / 1000

	if err != nil {
		rec.Err = err.Error()
		return rec, res
	}

	rec.OK = true
	if !res.FirstToken.IsZero() {
		rec.TTFTMS = float64(res.FirstToken.Sub(start).Microseconds()) / 1000
	}
	f := res.Final
	rec.ServerTotalMS = nsToMS(f.TotalDuration)
	rec.LoadMS = nsToMS(f.LoadDuration)
	rec.PromptTokens = f.PromptEvalCount
	rec.PromptEvalMS = nsToMS(f.PromptEvalDuration)
	rec.EvalTokens = f.EvalCount
	rec.EvalMS = nsToMS(f.EvalDuration)
	rec.DoneReason = f.DoneReason
	rec.ResponseChars = len(res.Content)
	rec.ThinkingChars = len(res.Thinking)
	rec.ClientOverhead = rec.WallMS - rec.ServerTotalMS
	if len(res.Gaps) > 0 {
		s := Summarize(res.Gaps)
		rec.InterTokenP50 = s.Median
		rec.InterTokenP95 = s.P95
	}
	rec.responseText = res.Content
	return rec, res
}
