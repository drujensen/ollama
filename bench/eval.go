package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
)

// bundledProblems ships with the binary so the eval works with no download.
// The format is HumanEval's, so -problems can point at the real 164-problem
// JSONL and produce directly comparable pass@1 numbers.
//
//go:embed problems/bundled.jsonl
var bundledProblems []byte

type Problem struct {
	TaskID            string `json:"task_id"`
	Prompt            string `json:"prompt"`
	EntryPoint        string `json:"entry_point"`
	Test              string `json:"test"`
	CanonicalSolution string `json:"canonical_solution,omitempty"`
}

type EvalCfg struct {
	Samples      int
	Temperature  float64
	NumPredict   int
	ExecTimeout  time.Duration
	ProblemsPath string
	Limit        int
}

type EvalRecord struct {
	Model      string  `json:"model"`
	TaskID     string  `json:"task_id"`
	EntryPoint string  `json:"entry_point"`
	Sample     int     `json:"sample"`
	Passed     bool    `json:"passed"`
	Outcome    string  `json:"outcome"`
	Detail     string  `json:"detail,omitempty"`
	GenMS      float64 `json:"gen_ms"`
	ExecMS     float64 `json:"exec_ms"`
	EvalTokens int     `json:"eval_tokens"`
	DecodeTPS  float64 `json:"decode_tps"`
	CodeChars  int     `json:"code_chars"`
	Truncated  bool    `json:"truncated"`
}

type EvalSummary struct {
	Model     string         `json:"model"`
	Problems  int            `json:"problems"`
	Samples   int            `json:"samples"`
	Attempts  int            `json:"attempts"`
	Passed    int            `json:"passed"`
	PassAt1   float64        `json:"pass_at_1"`
	PassAtK   float64        `json:"pass_at_k,omitempty"`
	K         int            `json:"k,omitempty"`
	Outcomes  map[string]int `json:"outcomes"`
	MeanGenMS float64        `json:"mean_gen_ms"`
	MeanTok   float64        `json:"mean_eval_tokens"`
	Truncated int            `json:"truncated"`
}

func LoadProblems(path string) ([]Problem, error) {
	data := bundledProblems
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = b
	}

	var out []Problem
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var p Problem
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if p.EntryPoint == "" || p.Test == "" || p.Prompt == "" {
			return nil, fmt.Errorf("line %d (%s): needs prompt, entry_point and test", line, p.TaskID)
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no problems found")
	}
	return out, nil
}

const evalInstruction = `Complete the following Python function.

Reply with the complete function inside a single ` + "```python" + ` code block. Include any imports the function needs. Do not write tests, example usage, or explanation.

`

// RunEval generates a solution per problem, executes it against the problem's
// tests in the sandbox, and records why each failure failed.
func (r *Runner) RunEval(ctx context.Context, m ModelCfg, probs []Problem, sb Sandbox, ec EvalCfg) []EvalRecord {
	var out []EvalRecord

	for _, p := range probs {
		for sample := 1; sample <= ec.Samples; sample++ {
			if ctx.Err() != nil {
				return out
			}
			rec := EvalRecord{Model: m.Name, TaskID: p.TaskID, EntryPoint: p.EntryPoint, Sample: sample}

			opts := map[string]any{}
			if ec.Samples > 1 {
				// pass@k needs diverse samples; a fixed seed at temperature 0
				// would return the same completion k times.
				opts["temperature"] = ec.Temperature
				opts["seed"] = sample
			}

			// Chat endpoint: on /api/generate a model whose template
			// pre-injects <think> mixes reasoning into the response, which
			// pollutes code extraction.
			gen, chat := r.execChat(ctx, ModelCfg{Name: m.Name, Think: m.Think, Options: mergeOptions(m.Options, opts)}, chatSpec{
				suite:      "eval",
				label:      p.TaskID,
				messages:   []ChatMessage{{Role: "user", Content: evalInstruction + p.Prompt}},
				numPredict: ec.NumPredict,
				iter:       sample,
			})
			_ = chat

			rec.GenMS = gen.WallMS
			rec.EvalTokens = gen.EvalTokens
			rec.DecodeTPS = gen.DecodeTPS()
			rec.Truncated = gen.Truncated()

			if !gen.OK {
				rec.Outcome, rec.Detail = "generation-error", firstLine(gen.Err)
				out = append(out, rec)
				r.evalLine(rec)
				continue
			}

			code := extractCode(gen.responseText, p.EntryPoint)
			rec.CodeChars = len(code)
			if code == "" {
				rec.Outcome = "no-code"
				rec.Detail = "no function found in the response"
				if rec.Truncated {
					rec.Detail = "response truncated before any code block closed"
				}
				out = append(out, rec)
				r.evalLine(rec)
				continue
			}

			res := sb.Run(ctx, buildProgram(code, p), ec.ExecTimeout)
			rec.Outcome, rec.Detail, rec.ExecMS = res.Outcome, res.Detail, res.DurationMS
			rec.Passed = res.Outcome == "pass"
			out = append(out, rec)
			r.evalLine(rec)
		}
	}
	return out
}

func (r *Runner) evalLine(rec EvalRecord) {
	if r.quiet {
		return
	}
	detail := rec.Detail
	if rec.Passed {
		detail = ""
	}
	r.logf("    %-6s %-34s %-16s %5d tok  %6.0f ms  %s",
		"eval", truncate(rec.TaskID+"/"+rec.EntryPoint, 34), rec.Outcome,
		rec.EvalTokens, rec.GenMS, truncate(detail, 60))
}

// buildProgram assembles the candidate code, the problem's tests, and the call
// that runs them.
func buildProgram(code string, p Problem) string {
	var b strings.Builder
	b.WriteString(code)
	b.WriteString("\n\n")
	b.WriteString(p.Test)
	b.WriteString("\n\ncheck(")
	b.WriteString(p.EntryPoint)
	b.WriteString(")\n")
	return b.String()
}

var pyFenceRE = regexp.MustCompile("(?s)```(?:python|py)?[ \\t]*\\r?\\n(.*?)```")

// extractCode pulls the candidate function out of a chat response. It prefers
// the fenced block that actually defines the entry point, since models often
// emit several blocks (imports, usage examples, output samples).
func extractCode(resp, entryPoint string) string {
	want := "def " + entryPoint
	matches := pyFenceRE.FindAllStringSubmatch(resp, -1)

	var fallback string
	for _, m := range matches {
		block := strings.TrimSpace(m[1])
		if block == "" {
			continue
		}
		if strings.Contains(block, want) {
			return block
		}
		if fallback == "" {
			fallback = block
		}
	}
	if fallback != "" {
		return fallback
	}

	// No usable fence. Accept a bare response only if it defines the function,
	// which covers models that ignore the formatting instruction.
	if strings.Contains(resp, want) {
		return strings.TrimSpace(stripStrayFence(resp))
	}
	return ""
}

// stripStrayFence drops an unterminated opening fence from a truncated
// response so the code before it can still be compiled.
func stripStrayFence(s string) string {
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexByte(rest, '\n'); j >= 0 {
			rest = rest[j+1:]
		}
		if k := strings.Index(rest, "```"); k >= 0 {
			rest = rest[:k]
		}
		return rest
	}
	return s
}

func SummarizeEval(recs []EvalRecord, samples int) []EvalSummary {
	type key string
	var order []string
	groups := map[string][]EvalRecord{}
	for _, r := range recs {
		if _, ok := groups[r.Model]; !ok {
			order = append(order, r.Model)
		}
		groups[r.Model] = append(groups[r.Model], r)
	}

	var out []EvalSummary
	for _, model := range order {
		rs := groups[model]
		s := EvalSummary{Model: model, Samples: samples, Outcomes: map[string]int{}}

		byTask := map[string][]EvalRecord{}
		var genSum, tokSum float64
		for _, r := range rs {
			s.Attempts++
			s.Outcomes[r.Outcome]++
			if r.Passed {
				s.Passed++
			}
			if r.Truncated {
				s.Truncated++
			}
			genSum += r.GenMS
			tokSum += float64(r.EvalTokens)
			byTask[r.TaskID] = append(byTask[r.TaskID], r)
		}
		s.Problems = len(byTask)
		if s.Attempts > 0 {
			s.MeanGenMS = genSum / float64(s.Attempts)
			s.MeanTok = tokSum / float64(s.Attempts)
			s.PassAt1 = float64(s.Passed) * 100 / float64(s.Attempts)
		}

		if samples > 1 {
			s.K = samples
			var acc float64
			for _, task := range byTask {
				var c int
				for _, r := range task {
					if r.Passed {
						c++
					}
				}
				acc += passAtK(len(task), c, samples)
			}
			if s.Problems > 0 {
				s.PassAtK = acc * 100 / float64(s.Problems)
			}
		}
		out = append(out, s)
	}
	return out
}

// passAtK is the unbiased estimator from the Codex paper: the probability that
// at least one of k samples drawn from n passes, given c passed.
func passAtK(n, c, k int) float64 {
	if n-c < k {
		return 1
	}
	prob := 1.0
	for i := n - c + 1; i <= n; i++ {
		prob *= 1 - float64(k)/float64(i)
	}
	return 1 - prob
}

func evalPct(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// referenceProgram builds a runnable program from a problem's reference
// solution. Problem files come in two shapes: ours carries a complete solution,
// while HumanEval carries a bare completion that only makes sense appended to
// its prompt. Detect which by whether the solution defines the entry point.
func referenceProgram(p Problem) string {
	sol := p.CanonicalSolution
	if !strings.Contains(sol, "def "+p.EntryPoint) {
		sol = p.Prompt + sol
	}
	return buildProgram(sol, p)
}

// ValidateProblems runs every reference solution against its own tests and
// returns how many passed plus the ones that did not. A problem file that fails
// this cannot support any claim about a model.
func ValidateProblems(ctx context.Context, sb Sandbox, probs []Problem, timeout time.Duration) (int, []Problem) {
	ok := 0
	var failed []Problem
	for _, p := range probs {
		if ctx.Err() != nil {
			break
		}
		if sb.Run(ctx, referenceProgram(p), timeout).Outcome == "pass" {
			ok++
		} else {
			failed = append(failed, p)
		}
	}
	return ok, failed
}
