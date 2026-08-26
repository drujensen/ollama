package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{1, 2, 3, 4, 5})
	if s.N != 5 || s.Min != 1 || s.Max != 5 || s.Median != 3 {
		t.Fatalf("unexpected stats: %+v", s)
	}
	if math.Abs(s.Mean-3) > 1e-9 {
		t.Fatalf("mean = %v, want 3", s.Mean)
	}
	// Sample stddev of 1..5 is sqrt(2.5).
	if math.Abs(s.Stddev-math.Sqrt(2.5)) > 1e-9 {
		t.Fatalf("stddev = %v, want %v", s.Stddev, math.Sqrt(2.5))
	}
	if empty := Summarize(nil); empty.N != 0 {
		t.Fatalf("empty sample should have N=0, got %+v", empty)
	}
	if one := Summarize([]float64{7}); one.Median != 7 || one.P95 != 7 || one.Stddev != 0 {
		t.Fatalf("single sample: %+v", one)
	}
}

func TestPercentileInterpolates(t *testing.T) {
	v := []float64{0, 10}
	if got := percentile(v, 50); got != 5 {
		t.Fatalf("p50 = %v, want 5", got)
	}
	if got := percentile(v, 95); math.Abs(got-9.5) > 1e-9 {
		t.Fatalf("p95 = %v, want 9.5", got)
	}
}

func TestNoisyThreshold(t *testing.T) {
	tight := Summarize([]float64{100, 101, 99, 100})
	if tight.Noisy() {
		t.Fatalf("tight sample flagged noisy: cv=%v", tight.CV)
	}
	wide := Summarize([]float64{10, 100, 55, 20})
	if !wide.Noisy() {
		t.Fatalf("wide sample not flagged: cv=%v", wide.CV)
	}
}

func TestRecordThroughputSplit(t *testing.T) {
	r := Record{PromptTokens: 1000, PromptEvalMS: 500, EvalTokens: 100, EvalMS: 1000}
	if got := r.PrefillTPS(); got != 2000 {
		t.Fatalf("prefill = %v, want 2000", got)
	}
	if got := r.DecodeTPS(); got != 100 {
		t.Fatalf("decode = %v, want 100", got)
	}
	// Zero durations must not divide by zero.
	if (Record{}).PrefillTPS() != 0 || (Record{}).DecodeTPS() != 0 {
		t.Fatal("zero record should report zero throughput")
	}
}

func TestTruncatedDetectsLengthStop(t *testing.T) {
	if !(Record{DoneReason: "length"}).Truncated() {
		t.Fatal("length stop should count as truncated")
	}
	if (Record{DoneReason: "stop"}).Truncated() {
		t.Fatal("natural stop is not truncation")
	}
}

func TestSummarizesExcludesFailures(t *testing.T) {
	recs := []Record{
		{Model: "m", Suite: "latency", Label: "a", OK: true, TTFTMS: 100, EvalTokens: 10, EvalMS: 100},
		{Model: "m", Suite: "latency", Label: "a", OK: false, Err: "boom"},
	}
	out := Summarizes(recs)
	if len(out) != 1 {
		t.Fatalf("want 1 cell, got %d", len(out))
	}
	s := out[0]
	if s.Runs != 2 || s.Failed != 1 || s.DecodeTPS.N != 1 {
		t.Fatalf("failure not excluded from stats: %+v", s)
	}
}

func TestCommentStripperKeepsStrings(t *testing.T) {
	in := []byte(`{
  // a comment
  "url": "http://x/y", // trailing
  "esc": "a\"// not a comment"
}`)
	var v map[string]string
	if err := json.NewDecoder(newCommentStripper(in)).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v["url"] != "http://x/y" {
		t.Fatalf("URL slashes eaten: %q", v["url"])
	}
	if v["esc"] != `a"// not a comment` {
		t.Fatalf("comment stripped inside string: %q", v["esc"])
	}
}

func TestVerifyBash(t *testing.T) {
	ok, detail := verifyBash("```bash\nls -S | head -5\n```")
	if !ok {
		t.Fatalf("valid script rejected: %s", detail)
	}
	if ok, _ := verifyBash("```bash\nif [ -z ; then\n```"); ok {
		t.Fatal("syntactically broken script accepted")
	}
	if ok, _ := verifyBash(""); ok {
		t.Fatal("empty response accepted")
	}
}

func TestVerifyJSON(t *testing.T) {
	if ok, d := verifyJSON(`{"name":"srv","port":8080}`); !ok {
		t.Fatalf("valid object rejected: %s", d)
	}
	if ok, _ := verifyJSON(`{"name":"srv"}`); ok {
		t.Fatal("object missing a required key accepted")
	}
	if ok, _ := verifyJSON(`[1,2]`); ok {
		t.Fatal("array accepted as object")
	}
	if ok, _ := verifyJSON("not json"); ok {
		t.Fatal("garbage accepted")
	}
}

func TestVerifyPong(t *testing.T) {
	for _, s := range []string{"PONG", " pong ", "PONG.", "**PONG**"} {
		if ok, d := verifyPong(s); !ok {
			t.Fatalf("verifyPong(%q) failed: %s", s, d)
		}
	}
	if ok, _ := verifyPong("Sure! PONG"); ok {
		t.Fatal("extra prose should fail the instruction check")
	}
}

func TestParseNumCtx(t *testing.T) {
	params := "temperature                    0.6\nnum_ctx                        262144\n"
	if got := parseNumCtx(params); got != 262144 {
		t.Fatalf("num_ctx = %d, want 262144", got)
	}
	if got := parseNumCtx("temperature 0.6"); got != 0 {
		t.Fatalf("missing num_ctx should be 0, got %d", got)
	}
}

func TestTokLabel(t *testing.T) {
	cases := map[int]string{0: "0", 1000: "1k", 16000: "16k", 1500: "1500"}
	for in, want := range cases {
		if got := tokLabel(in); got != want {
			t.Fatalf("tokLabel(%d) = %q, want %q", in, got, want)
		}
	}
}

// Filler must be deterministic per seed, and different per seed, or the prompt
// cache would serve one iteration from another's prefix and prefill throughput
// would be fiction.
func TestFillerDeterministicAndUnique(t *testing.T) {
	a := docPrompt("m", "prefill", "1k", 1, 1000, "q")
	b := docPrompt("m", "prefill", "1k", 1, 1000, "q")
	c := docPrompt("m", "prefill", "1k", 2, 1000, "q")
	if a != b {
		t.Fatal("same inputs produced different prompts")
	}
	if a == c {
		t.Fatal("different iterations produced identical prompts")
	}
	if got := docPrompt("m", "depth", "@0", 1, 0, "q"); got != "q" {
		t.Fatalf("zero depth should be the bare question, got %q", got)
	}
}

func TestFillerLengthTracksTarget(t *testing.T) {
	// Roughly 1.1 tokens per word; assert the word count scales linearly so a
	// 16k target cannot silently produce a 1k prompt.
	small := len(strings.Fields(filler(1, 1000)))
	large := len(strings.Fields(filler(1, 16000)))
	ratio := float64(large) / float64(small)
	if ratio < 15 || ratio > 17 {
		t.Fatalf("filler scaling off: %d vs %d words (ratio %.2f)", small, large, ratio)
	}
}

func TestMergeOptionsLayers(t *testing.T) {
	base := map[string]any{"temperature": 0, "seed": 42}
	over := map[string]any{"temperature": 1}
	got := mergeOptions(base, over)
	if got["temperature"] != 1 || got["seed"] != 42 {
		t.Fatalf("merge = %v", got)
	}
	if base["temperature"] != 0 {
		t.Fatal("mergeOptions mutated its input")
	}
	if mergeOptions() != nil {
		t.Fatal("empty merge should be nil so the field is omitted")
	}
}

func TestPctAndSigned(t *testing.T) {
	if got := pct(100, 110); math.Abs(got-10) > 1e-9 {
		t.Fatalf("pct = %v, want 10", got)
	}
	if got := pct(0, 10); got != 0 {
		t.Fatalf("pct from zero base = %v, want 0", got)
	}
	if got := signed(-3.14); got != "-3.1%" {
		t.Fatalf("signed = %q", got)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err == nil {
		t.Fatal("config with no models should not validate")
	}
	cfg.Models = []ModelCfg{{Name: "x"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cfg.Suites = []string{"bogus"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown suite should not validate")
	}
}
