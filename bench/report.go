package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

// Result is the complete output of a run: what ran, on what, and what it
// measured. This whole struct is what gets written to JSON so a later run can
// be diffed against it.
type Result struct {
	Env         Env         `json:"env"`
	Models      []ModelInfo `json:"models"`
	Suites      []string    `json:"suites"`
	Summaries   []Summary   `json:"summaries"`
	ConcBatches []ConcBatch `json:"concurrency_batches,omitempty"`
	Records     []Record    `json:"records"`

	// Coding eval, populated by the `eval` subcommand.
	Sandbox       string        `json:"sandbox,omitempty"`
	EvalSamples   int           `json:"eval_samples,omitempty"`
	EvalSummaries []EvalSummary `json:"eval_summaries,omitempty"`
	EvalRecords   []EvalRecord  `json:"eval_records,omitempty"`

	AccuracySummaries []AccuracySummary `json:"accuracy_summaries,omitempty"`
	AccuracyRecords   []AccuracyRecord  `json:"accuracy_records,omitempty"`
	ToolSummaries     []ToolSummary     `json:"tool_summaries,omitempty"`
	ToolRecords       []ToolRecord      `json:"tool_records,omitempty"`
	EditSummaries     []EditSummary     `json:"edit_summaries,omitempty"`
	EditRecords       []EditRecord      `json:"edit_records,omitempty"`
}

type table struct {
	title string
	head  []string
	rows  [][]string
	right []bool
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) render(w io.Writer, md bool) {
	if len(t.rows) == 0 {
		return
	}
	if md {
		fmt.Fprintf(w, "\n### %s\n\n", t.title)
		fmt.Fprintf(w, "| %s |\n", strings.Join(t.head, " | "))
		seps := make([]string, len(t.head))
		for i := range seps {
			seps[i] = "---"
			if i < len(t.right) && t.right[i] {
				seps[i] = "---:"
			}
		}
		fmt.Fprintf(w, "|%s|\n", strings.Join(seps, "|"))
		for _, r := range t.rows {
			fmt.Fprintf(w, "| %s |\n", strings.Join(r, " | "))
		}
		return
	}

	fmt.Fprintf(w, "\n%s\n%s\n", t.title, strings.Repeat("=", len(t.title)))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(t.head, "\t"))
	for _, r := range t.rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
func f0(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }

// mark appends "!" to a value whose sample spread is too wide to compare.
func mark(v string, s Stats) string {
	if s.Noisy() {
		return v + "!"
	}
	return v
}

func bytesGB(b int64) string {
	if b == 0 {
		return "-"
	}
	return f1(float64(b)/(1<<30)) + "G"
}

func (r *Result) pick(model, suite string) []Summary {
	var out []Summary
	for _, s := range r.Summaries {
		if s.Model == model && s.Suite == suite {
			out = append(out, s)
		}
	}
	return out
}

// combine merges several cells into one, so the headline row can show a single
// figure across all latency prompts.
func (r *Result) combine(model, suite string) Summary {
	var ttft, dec, pre []float64
	agg := Summary{Model: model, Suite: suite, Label: "all"}
	for _, rec := range r.Records {
		if rec.Model != model || rec.Suite != suite {
			continue
		}
		agg.Runs++
		if !rec.OK {
			agg.Failed++
			continue
		}
		ttft = append(ttft, rec.TTFTMS)
		if d := rec.DecodeTPS(); d > 0 {
			dec = append(dec, d)
		}
		if p := rec.PrefillTPS(); p > 0 {
			pre = append(pre, p)
		}
		if rec.Truncated() {
			agg.Truncations++
		}
	}
	agg.TTFT = Summarize(ttft)
	agg.DecodeTPS = Summarize(dec)
	agg.PrefillTPS = Summarize(pre)
	return agg
}

func (r *Result) Render(w io.Writer, md bool) {
	e := r.Env
	if md {
		fmt.Fprintf(w, "# Ollama benchmark - %s\n\n", e.Timestamp.Format("2006-01-02 15:04:05"))
	} else {
		fmt.Fprintf(w, "\n%s\n", strings.Repeat("=", 78))
		fmt.Fprintf(w, "OLLAMA BENCHMARK  %s\n", e.Timestamp.Format("2006-01-02 15:04:05"))
		fmt.Fprintf(w, "%s\n", strings.Repeat("=", 78))
	}
	fmt.Fprintf(w, "host %s (%s/%s, %d cpu, %.0f GB)   ollama %s   repeat %d\n",
		e.Hostname, e.OS, e.Arch, e.NumCPU, e.MemTotalGB, e.OllamaVersion, e.Repeat)
	dirty := ""
	if e.GitDirty {
		dirty = "-dirty"
	}
	if e.GitSHA != "" {
		fmt.Fprintf(w, "repo %s%s   suites %s\n", e.GitSHA, dirty, strings.Join(r.Suites, ","))
	}

	if r.Sandbox != "" {
		fmt.Fprintf(w, "sandbox %s\n", r.Sandbox)
	}

	r.headline().render(w, md)
	r.accuracyTable().render(w, md)
	r.toolsTable().render(w, md)
	r.toolFailureTable().render(w, md)
	r.editTable().render(w, md)
	r.editFailureTable().render(w, md)
	r.evalTable().render(w, md)
	r.evalDetailTable().render(w, md)
	r.latencyTable().render(w, md)
	r.prefillTable().render(w, md)
	r.depthTable().render(w, md)
	r.concurrencyTable().render(w, md)
	r.sanityTable().render(w, md)
	r.failureTable().render(w, md)

	if !md {
		if len(r.EvalSummaries) > 0 {
			fmt.Fprintf(w, "\nwrong-answer = code ran, tests failed.  error = code could not run.\n")
			fmt.Fprintf(w, "no-code = no function found in the response.\n")
		}
		fmt.Fprintf(w, "\n! = sample spread over 15%% CV; treat close comparisons as a tie.\n")
		fmt.Fprintf(w, "prefill = prompt processing (prompt_eval_count/prompt_eval_duration)\n")
		fmt.Fprintf(w, "decode  = generation (eval_count/eval_duration)\n")
	}
}

func (r *Result) headline() *table {
	t := &table{
		title: "HEADLINE",
		head:  []string{"MODEL", "PARAMS", "QUANT", "CTX", "GPU%", "LOAD s", "TTFT p50 ms", "TTFT p95 ms", "PREFILL tok/s", "DECODE p50", "DECODE p95", "SANITY"},
		right: []bool{false, false, false, true, true, true, true, true, true, true, true, false},
	}
	for _, m := range r.Models {
		// Prefer the latency suite for the headline latency figures, but fall
		// back so the row is still populated when only some suites ran.
		var lat Summary
		for _, suite := range []string{"latency", "depth", "concurrency", "sanity", "prefill"} {
			if lat = r.combine(m.Name, suite); lat.Runs > 0 {
				break
			}
		}
		// Prefill belongs to the suite built to measure it: a 35-token latency
		// prompt gives a prefill number dominated by fixed overhead.
		prefill := r.combine(m.Name, "prefill")
		if prefill.PrefillTPS.N == 0 {
			prefill = lat
		}
		ctx := "-"
		if m.NumCtx > 0 {
			ctx = tokLabel(m.NumCtx)
		}
		gpu := "-"
		if m.ResidentBytes > 0 {
			gpu = f0(m.GPUPercent)
		}
		t.add(
			m.Name,
			dash(m.Params),
			dash(m.Quant),
			ctx,
			gpu,
			f1(m.ColdLoadMS/1000),
			mark(f0(lat.TTFT.Median), lat.TTFT),
			f0(lat.TTFT.P95),
			mark(f1(prefill.PrefillTPS.Median), prefill.PrefillTPS),
			mark(f1(lat.DecodeTPS.Median), lat.DecodeTPS),
			f1(lat.DecodeTPS.P95),
			r.sanityScore(m.Name),
		)
	}
	return t
}

func (r *Result) sanityScore(model string) string {
	pass, total := 0, 0
	for _, rec := range r.Records {
		if rec.Model != model || rec.Suite != "sanity" || rec.CheckPassed == nil {
			continue
		}
		total++
		if *rec.CheckPassed {
			pass++
		}
	}
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d", pass, total)
}

func (r *Result) latencyTable() *table {
	t := &table{
		title: "LATENCY  (short prompts)",
		head:  []string{"MODEL", "PROMPT", "N", "TTFT p50", "TTFT p95", "DECODE p50", "DECODE p95", "ITL p95", "EVAL tok", "TRUNC"},
		right: []bool{false, false, true, true, true, true, true, true, true, true},
	}
	for _, m := range r.Models {
		for _, s := range r.pick(m.Name, "latency") {
			t.add(m.Name, s.Label, strconv.Itoa(s.Runs),
				mark(f0(s.TTFT.Median), s.TTFT), f0(s.TTFT.P95),
				mark(f1(s.DecodeTPS.Median), s.DecodeTPS), f1(s.DecodeTPS.P95),
				f1(s.InterToken.Median),
				f0(s.MeanEvalTokens),
				truncCell(s.Truncations))
		}
	}
	return t
}

func (r *Result) prefillTable() *table {
	t := &table{
		title: "PREFILL  (prompt processing vs prompt length)",
		head:  []string{"MODEL", "TARGET", "ACTUAL tok", "PREFILL p50 tok/s", "PREFILL p95", "TTFT p50 ms"},
		right: []bool{false, true, true, true, true, true},
	}
	for _, m := range r.Models {
		for _, s := range r.pick(m.Name, "prefill") {
			t.add(m.Name, s.Label, f0(s.MeanPromptTokens),
				mark(f1(s.PrefillTPS.Median), s.PrefillTPS), f1(s.PrefillTPS.P95),
				f0(s.TTFT.Median))
		}
	}
	return t
}

func (r *Result) depthTable() *table {
	t := &table{
		title: "DEPTH  (decode speed vs KV cache depth)",
		head:  []string{"MODEL", "DEPTH", "ACTUAL tok", "DECODE p50 tok/s", "vs BASE", "TTFT p50 ms"},
		right: []bool{false, true, true, true, true, true},
	}
	for _, m := range r.Models {
		rows := r.pick(m.Name, "depth")
		var base float64
		if len(rows) > 0 {
			base = rows[0].DecodeTPS.Median
		}
		for _, s := range rows {
			rel := "-"
			if base > 0 {
				rel = fmt.Sprintf("%+.0f%%", (s.DecodeTPS.Median-base)*100/base)
			}
			t.add(m.Name, s.Label, f0(s.MeanPromptTokens),
				mark(f1(s.DecodeTPS.Median), s.DecodeTPS), rel, f0(s.TTFT.Median))
		}
	}
	return t
}

func (r *Result) concurrencyTable() *table {
	t := &table{
		title: "CONCURRENCY  (parallel streams)",
		head:  []string{"MODEL", "STREAMS", "AGGREGATE tok/s", "PER-STREAM tok/s", "SCALING", "TTFT p50 ms", "FAILED"},
		right: []bool{false, true, true, true, true, true, true},
	}
	for _, m := range r.Models {
		var base float64
		for _, b := range r.ConcBatches {
			if b.Model != m.Name {
				continue
			}
			if b.Level == 1 && base == 0 {
				base = b.AggregateTPS
			}
			scale := "-"
			if base > 0 {
				scale = fmt.Sprintf("%.2fx", b.AggregateTPS/base)
			}
			var ttft Stats
			for _, s := range r.pick(m.Name, "concurrency") {
				if s.Concurrency == b.Level {
					ttft = s.TTFT
					break
				}
			}
			t.add(m.Name, strconv.Itoa(b.Level), f1(b.AggregateTPS), f1(b.PerStreamTPS),
				scale, f0(ttft.Median), strconv.Itoa(b.Failed))
		}
	}
	return t
}

func (r *Result) sanityTable() *table {
	t := &table{
		title: "SANITY  (is the output usable at all)",
		head:  []string{"MODEL", "CHECK", "RESULT", "DETAIL"},
	}
	for _, m := range r.Models {
		for _, rec := range r.Records {
			if rec.Model != m.Name || rec.Suite != "sanity" {
				continue
			}
			res, detail := "ERROR", firstLine(rec.Err)
			if rec.CheckPassed != nil {
				res = "FAIL"
				if *rec.CheckPassed {
					res = "PASS"
				}
				detail = rec.CheckDetail
			}
			t.add(m.Name, rec.CheckName, res, truncate(detail, 60))
		}
	}
	return t
}

func (r *Result) failureTable() *table {
	t := &table{title: "FAILURES", head: []string{"MODEL", "SUITE", "LABEL", "ERROR"}}
	for _, rec := range r.Records {
		if rec.OK {
			continue
		}
		t.add(rec.Model, rec.Suite, rec.Label, truncate(firstLine(rec.Err), 80))
	}
	return t
}

func truncCell(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (r *Result) accuracyTable() *table {
	if len(r.AccuracySummaries) == 0 {
		return &table{}
	}
	groups := accuracyGroups()
	head := []string{"MODEL", "OVERALL", "PASSED"}
	right := []bool{false, true, true}
	for _, g := range groups {
		head = append(head, strings.ToUpper(g))
		right = append(right, true)
	}
	head = append(head, "EMPTY", "REPEAT", "TRUNC", "ERR")
	right = append(right, true, true, true, true)

	t := &table{title: "ACCURACY  (graded tasks, chat endpoint)", head: head, right: right}
	for _, s := range r.AccuracySummaries {
		row := []string{s.Model, evalPct(s.Percent), fmt.Sprintf("%d/%d", s.Passed, s.Total)}
		for _, g := range groups {
			row = append(row, fmt.Sprintf("%d/%d", s.ByGroup[g], s.GroupTotal[g]))
		}
		row = append(row, truncCell(s.Empty), truncCell(s.Repetitive), truncCell(s.Truncated), truncCell(s.Errors))
		t.add(row...)
	}
	return t
}

func (r *Result) toolsTable() *table {
	if len(r.ToolSummaries) == 0 {
		return &table{}
	}
	t := &table{
		title: "TOOL CALLING  (multi-turn, the pattern a coding agent drives)",
		head:  []string{"MODEL", "TURNS OK", "SCENARIOS OK", "NO-CALL", "WRONG-TOOL", "BAD-ARGS", "UNWANTED", "REPEATED", "STRAY MARKUP", "BAD CHARS", "MEAN s"},
		right: []bool{false, true, true, true, true, true, true, true, true, true, true},
	}
	for _, s := range r.ToolSummaries {
		t.add(s.Model,
			fmt.Sprintf("%d/%d (%s)", s.Passed, s.Turns, evalPct(s.Percent)),
			fmt.Sprintf("%d/%d", s.ScenariosOK, s.Scenarios),
			truncCell(s.Outcomes["no-tool-call"]),
			truncCell(s.Outcomes["wrong-tool"]),
			truncCell(s.Outcomes["bad-args"]),
			truncCell(s.Outcomes["unexpected-tool"]),
			truncCell(s.RepeatCalls),
			truncCell(s.StrayMarkup), truncCell(s.BadChars),
			f1(s.MeanWallMS/1000))
	}
	return t
}

func (r *Result) toolFailureTable() *table {
	t := &table{title: "TOOL CALLING FAILURES", head: []string{"MODEL", "SCENARIO", "TURN", "OUTCOME", "CALLED", "DETAIL"}}
	for _, rec := range r.ToolRecords {
		if rec.Passed {
			continue
		}
		t.add(rec.Model, rec.Scenario, strconv.Itoa(rec.Turn), rec.Outcome, dash(rec.CalledTool), truncate(rec.Detail, 60))
	}
	return t
}

// editTable leads with attempts-per-success, because a model that lands the
// edit first time behaves very differently inside an agent loop from one that
// needs corrections, even when both eventually succeed.
func (r *Result) editTable() *table {
	if len(r.EditSummaries) == 0 {
		return &table{}
	}
	t := &table{
		title: "FILE EDITING  (search/replace against an existing file)",
		head:  []string{"MODEL", "PASSED", "FIRST TRY", "MEAN TRIES", "PARSE FAIL", "APPLY FAIL", "TEST FAIL", "DRIFT", "MEAN s"},
		right: []bool{false, true, true, true, true, true, true, true, true},
	}
	for _, s := range r.EditSummaries {
		t.add(s.Model,
			fmt.Sprintf("%d/%d (%s)", s.Passed, s.Tasks, evalPct(s.Percent)),
			fmt.Sprintf("%d/%d", s.FirstTry, s.Tasks),
			fmt.Sprintf("%.2f", s.MeanAttempts),
			truncCell(s.ParseFailures), truncCell(s.ApplyFailures),
			truncCell(s.TestFailures), truncCell(s.DriftTasks),
			f1(s.MeanWallMS/1000))
	}
	return t
}

func (r *Result) editFailureTable() *table {
	t := &table{title: "FILE EDITING FAILURES", head: []string{"MODEL", "TASK", "DIFFICULTY", "TRIES", "OUTCOME", "DETAIL"}}
	for _, rec := range r.EditRecords {
		if rec.Outcome == "pass" {
			continue
		}
		t.add(rec.Model, rec.TaskID, rec.Difficulty, strconv.Itoa(rec.Attempts), rec.Outcome, truncate(rec.Detail, 60))
	}
	return t
}

func (r *Result) evalTable() *table {
	t := &table{
		title: "CODING EVAL  (generated code executed against unit tests)",
		head:  []string{"MODEL", "PROBLEMS", "SAMPLES", "PASS@1", "PASSED", "WRONG", "ERROR", "NO-CODE", "TIMEOUT", "GEN-ERR", "MEAN GEN s", "MEAN tok", "TRUNC"},
		right: []bool{false, true, true, true, true, true, true, true, true, true, true, true, true},
	}
	for _, s := range r.EvalSummaries {
		passCol := evalPct(s.PassAt1)
		if s.K > 1 {
			passCol = fmt.Sprintf("%s (pass@%d %s)", passCol, s.K, evalPct(s.PassAtK))
		}
		t.add(s.Model,
			strconv.Itoa(s.Problems), strconv.Itoa(s.Samples), passCol,
			fmt.Sprintf("%d/%d", s.Passed, s.Attempts),
			strconv.Itoa(s.Outcomes["wrong-answer"]),
			strconv.Itoa(s.Outcomes["error"]),
			strconv.Itoa(s.Outcomes["no-code"]),
			strconv.Itoa(s.Outcomes["timeout"]),
			strconv.Itoa(s.Outcomes["generation-error"]),
			f1(s.MeanGenMS/1000), f0(s.MeanTok), truncCell(s.Truncated))
	}
	return t
}

// evalDetailTable lists only the failures: which problem, and why. An eval that
// reports a score without saying what broke cannot be acted on.
func (r *Result) evalDetailTable() *table {
	t := &table{
		title: "CODING EVAL FAILURES",
		head:  []string{"MODEL", "TASK", "FUNCTION", "SAMPLE", "OUTCOME", "DETAIL"},
	}
	for _, rec := range r.EvalRecords {
		if rec.Passed {
			continue
		}
		t.add(rec.Model, rec.TaskID, rec.EntryPoint, strconv.Itoa(rec.Sample), rec.Outcome, truncate(rec.Detail, 70))
	}
	return t
}

// WriteEvalCSV emits one row per generated solution.
func (r *Result) WriteEvalCSV(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"model", "task_id", "entry_point", "sample", "passed",
		"outcome", "detail", "gen_ms", "exec_ms", "eval_tokens", "decode_tps", "code_chars", "truncated"}); err != nil {
		return err
	}
	for _, rec := range r.EvalRecords {
		if err := w.Write([]string{rec.Model, rec.TaskID, rec.EntryPoint,
			strconv.Itoa(rec.Sample), strconv.FormatBool(rec.Passed), rec.Outcome, firstLine(rec.Detail),
			f1(rec.GenMS), f1(rec.ExecMS), strconv.Itoa(rec.EvalTokens), f1(rec.DecodeTPS),
			strconv.Itoa(rec.CodeChars), strconv.FormatBool(rec.Truncated)}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Result) WriteJSON(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteCSV emits one row per request so the raw data can go into a spreadsheet
// or a plot without re-running anything.
func (r *Result) WriteCSV(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	head := []string{"model", "suite", "label", "iter", "concurrency", "ok",
		"ttft_ms", "wall_ms", "server_total_ms", "load_ms",
		"prompt_tokens", "prompt_eval_ms", "prefill_tps",
		"eval_tokens", "eval_ms", "decode_tps",
		"itl_p50_ms", "itl_p95_ms", "client_overhead_ms",
		"done_reason", "check", "check_passed", "error"}
	if err := w.Write(head); err != nil {
		return err
	}
	for _, rec := range r.Records {
		passed := ""
		if rec.CheckPassed != nil {
			passed = strconv.FormatBool(*rec.CheckPassed)
		}
		row := []string{rec.Model, rec.Suite, rec.Label,
			strconv.Itoa(rec.Iter), strconv.Itoa(rec.Concurrency), strconv.FormatBool(rec.OK),
			f1(rec.TTFTMS), f1(rec.WallMS), f1(rec.ServerTotalMS), f1(rec.LoadMS),
			strconv.Itoa(rec.PromptTokens), f1(rec.PromptEvalMS), f1(rec.PrefillTPS()),
			strconv.Itoa(rec.EvalTokens), f1(rec.EvalMS), f1(rec.DecodeTPS()),
			f1(rec.InterTokenP50), f1(rec.InterTokenP95), f1(rec.ClientOverhead),
			rec.DoneReason, rec.CheckName, passed, firstLine(rec.Err)}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func (r *Result) WriteMarkdown(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r.Render(f, true)
	return nil
}
