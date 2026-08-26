package main

import (
	"math"
	"sort"
	"time"
)

// Record is one benchmark request. Durations from the server arrive in
// nanoseconds; everything stored here is milliseconds so the report layer never
// has to convert.
type Record struct {
	Model       string    `json:"model"`
	Suite       string    `json:"suite"`
	Label       string    `json:"label"`
	Iter        int       `json:"iter"`
	Concurrency int       `json:"concurrency"`
	TargetDepth int       `json:"target_depth"`
	StartedAt   time.Time `json:"started_at"`

	OK  bool   `json:"ok"`
	Err string `json:"error,omitempty"`

	// Client-side wall clock.
	WallMS float64 `json:"wall_ms"`
	TTFTMS float64 `json:"ttft_ms"`

	// Server-side counters, split so prefill and decode are never averaged
	// together.
	ServerTotalMS  float64 `json:"server_total_ms"`
	LoadMS         float64 `json:"load_ms"`
	PromptTokens   int     `json:"prompt_tokens"`
	PromptEvalMS   float64 `json:"prompt_eval_ms"`
	EvalTokens     int     `json:"eval_tokens"`
	EvalMS         float64 `json:"eval_ms"`
	ThinkingChars  int     `json:"thinking_chars"`
	ResponseChars  int     `json:"response_chars"`
	DoneReason     string  `json:"done_reason"`
	InterTokenP50  float64 `json:"inter_token_p50_ms"`
	InterTokenP95  float64 `json:"inter_token_p95_ms"`
	ClientOverhead float64 `json:"client_overhead_ms"`

	// Sanity suite only.
	CheckName   string `json:"check_name,omitempty"`
	CheckPassed *bool  `json:"check_passed,omitempty"`
	CheckDetail string `json:"check_detail,omitempty"`

	// responseText is kept for sanity checks only and never serialized; the
	// old script printed every response inline and buried the numbers.
	responseText string
}

// PrefillTPS is prompt-processing throughput: how fast the model ingests the
// prompt. Reported separately from decode because it runs 1-2 orders of
// magnitude faster and would otherwise swamp any combined figure.
func (r Record) PrefillTPS() float64 {
	if r.PromptEvalMS <= 0 {
		return 0
	}
	return float64(r.PromptTokens) * 1000 / r.PromptEvalMS
}

// DecodeTPS is generation throughput, the number a user actually waits on.
func (r Record) DecodeTPS() float64 {
	if r.EvalMS <= 0 {
		return 0
	}
	return float64(r.EvalTokens) * 1000 / r.EvalMS
}

// Truncated reports whether generation was cut off by num_predict rather than
// by the model stopping. Without this a truncating model looks like a fast one.
func (r Record) Truncated() bool { return r.DoneReason == "length" }

type Stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Stddev float64 `json:"stddev"`
	CV     float64 `json:"cv"` // coefficient of variation, stddev/mean
}

// Noisy flags a sample whose spread is wide enough that differences between
// models of similar speed are not meaningful.
func (s Stats) Noisy() bool { return s.N >= 3 && s.CV > 0.15 }

func Summarize(xs []float64) Stats {
	var s Stats
	if len(xs) == 0 {
		return s
	}
	v := append([]float64(nil), xs...)
	sort.Float64s(v)
	s.N = len(v)
	s.Min = v[0]
	s.Max = v[len(v)-1]
	s.Median = percentile(v, 50)
	s.P95 = percentile(v, 95)
	var sum float64
	for _, x := range v {
		sum += x
	}
	s.Mean = sum / float64(s.N)
	if s.N > 1 {
		var ss float64
		for _, x := range v {
			d := x - s.Mean
			ss += d * d
		}
		s.Stddev = math.Sqrt(ss / float64(s.N-1))
	}
	if s.Mean != 0 {
		s.CV = s.Stddev / math.Abs(s.Mean)
	}
	return s
}

// percentile uses linear interpolation between order statistics; v must be sorted.
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	if len(v) == 1 {
		return v[0]
	}
	rank := (p / 100) * float64(len(v)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return v[lo]
	}
	return v[lo] + (rank-float64(lo))*(v[hi]-v[lo])
}

// Summary is the aggregate for one (model, suite, label) cell.
type Summary struct {
	Model       string `json:"model"`
	Suite       string `json:"suite"`
	Label       string `json:"label"`
	Concurrency int    `json:"concurrency"`

	Runs   int `json:"runs"`
	Failed int `json:"failed"`

	TTFT       Stats `json:"ttft_ms"`
	DecodeTPS  Stats `json:"decode_tps"`
	PrefillTPS Stats `json:"prefill_tps"`
	WallMS     Stats `json:"wall_ms"`
	InterToken Stats `json:"inter_token_p95_ms"`

	MeanPromptTokens float64 `json:"mean_prompt_tokens"`
	MeanEvalTokens   float64 `json:"mean_eval_tokens"`
	Truncations      int     `json:"truncations"`
}

// Summarizes groups records into per-cell summaries, preserving first-seen order
// so the report reads in the order the suites ran.
func Summarizes(recs []Record) []Summary {
	type key struct {
		model, suite, label string
		conc                int
	}
	order := []key{}
	groups := map[key][]Record{}
	for _, r := range recs {
		k := key{r.Model, r.Suite, r.Label, r.Concurrency}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}

	out := make([]Summary, 0, len(order))
	for _, k := range order {
		rs := groups[k]
		s := Summary{Model: k.model, Suite: k.suite, Label: k.label, Concurrency: k.conc, Runs: len(rs)}
		var ttft, dec, pre, wall, itl []float64
		var pt, et float64
		var ok int
		for _, r := range rs {
			if !r.OK {
				s.Failed++
				continue
			}
			ok++
			ttft = append(ttft, r.TTFTMS)
			wall = append(wall, r.WallMS)
			itl = append(itl, r.InterTokenP95)
			if d := r.DecodeTPS(); d > 0 {
				dec = append(dec, d)
			}
			if p := r.PrefillTPS(); p > 0 {
				pre = append(pre, p)
			}
			pt += float64(r.PromptTokens)
			et += float64(r.EvalTokens)
			if r.Truncated() {
				s.Truncations++
			}
		}
		s.TTFT = Summarize(ttft)
		s.DecodeTPS = Summarize(dec)
		s.PrefillTPS = Summarize(pre)
		s.WallMS = Summarize(wall)
		s.InterToken = Summarize(itl)
		if ok > 0 {
			s.MeanPromptTokens = pt / float64(ok)
			s.MeanEvalTokens = et / float64(ok)
		}
		out = append(out, s)
	}
	return out
}
