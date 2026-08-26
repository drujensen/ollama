package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

func loadResult(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

type cmpKey struct {
	model, suite, label string
	conc                int
}

// Compare diffs two runs cell by cell. Exit status is non-zero when any metric
// regressed by more than threshold percent, which makes it usable as a CI gate
// after a driver, quant, or Modelfile change.
func Compare(w io.Writer, oldPath, newPath string, threshold float64) (regressions int, err error) {
	oldR, err := loadResult(oldPath)
	if err != nil {
		return 0, err
	}
	newR, err := loadResult(newPath)
	if err != nil {
		return 0, err
	}

	index := func(r *Result) map[cmpKey]Summary {
		m := map[cmpKey]Summary{}
		for _, s := range r.Summaries {
			m[cmpKey{s.Model, s.Suite, s.Label, s.Concurrency}] = s
		}
		return m
	}
	a, b := index(oldR), index(newR)

	fmt.Fprintf(w, "\nCOMPARE\n=======\n")
	fmt.Fprintf(w, "base %s  (ollama %s, %s)\n", oldPath, oldR.Env.OllamaVersion, oldR.Env.Timestamp.Format("2006-01-02 15:04"))
	fmt.Fprintf(w, "new  %s  (ollama %s, %s)\n", newPath, newR.Env.OllamaVersion, newR.Env.Timestamp.Format("2006-01-02 15:04"))
	fmt.Fprintf(w, "regression threshold: %.0f%%\n", threshold)

	keys := make([]cmpKey, 0, len(b))
	for k := range b {
		if _, ok := a[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		if keys[i].suite != keys[j].suite {
			return keys[i].suite < keys[j].suite
		}
		return keys[i].label < keys[j].label
	})

	t := &table{
		title: "PER-CELL DELTA",
		head:  []string{"MODEL", "SUITE", "LABEL", "METRIC", "old", "new", "DELTA", "TTFT old", "TTFT new", "DELTA", "FLAG"},
		right: []bool{false, false, false, false, true, true, true, true, true, true, false},
	}
	for _, k := range keys {
		oa, nb := a[k], b[k]
		// The prefill suite generates almost nothing, so its decode figure is
		// noise; compare the metric each suite was built to measure.
		metric, os_, ns := "decode", oa.DecodeTPS, nb.DecodeTPS
		if k.suite == "prefill" {
			metric, os_, ns = "prefill", oa.PrefillTPS, nb.PrefillTPS
		}

		dThru := pct(os_.Median, ns.Median)
		// Lower TTFT is better, so its sign is inverted to keep "negative is
		// worse" true for every delta column.
		dTTFT := -pct(oa.TTFT.Median, nb.TTFT.Median)

		// A single sample cannot establish a regression.
		informational := os_.N < 2 || ns.N < 2

		flag := ""
		switch {
		case informational:
			flag = "n=1, informational"
		case dThru <= -threshold || dTTFT <= -threshold:
			flag = "REGRESSED"
			regressions++
		case dThru >= threshold && dTTFT >= -threshold:
			flag = "improved"
		}
		if !informational && (os_.Noisy() || ns.Noisy()) {
			flag += " (noisy)"
		}

		t.add(k.model, k.suite, k.label, metric,
			f1(os_.Median), f1(ns.Median), signed(dThru),
			f0(oa.TTFT.Median), f0(nb.TTFT.Median), signed(dTTFT), flag)
	}
	t.render(w, false)

	regressions += compareEval(w, oldR, newR, threshold)

	only := &table{title: "NOT IN BOTH RUNS", head: []string{"WHERE", "MODEL", "SUITE", "LABEL"}}
	for k := range a {
		if _, ok := b[k]; !ok {
			only.add("base only", k.model, k.suite, k.label)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			only.add("new only", k.model, k.suite, k.label)
		}
	}
	only.render(w, false)

	fmt.Fprintf(w, "\n%d regression(s) beyond %.0f%%\n", regressions, threshold)
	return regressions, nil
}

func pct(old, new float64) float64 {
	if old == 0 || math.IsNaN(old) {
		return 0
	}
	return (new - old) * 100 / old
}

func signed(v float64) string {
	return fmt.Sprintf("%+.1f%%", v)
}

// compareEval diffs coding-eval results: the headline pass rate per model, and
// which individual problems changed verdict. A pass rate that holds steady
// while different problems break is worth seeing.
func compareEval(w io.Writer, oldR, newR *Result, threshold float64) (regressions int) {
	if len(oldR.EvalSummaries) == 0 || len(newR.EvalSummaries) == 0 {
		return 0
	}

	oldByModel := map[string]EvalSummary{}
	for _, s := range oldR.EvalSummaries {
		oldByModel[s.Model] = s
	}

	t := &table{
		title: "CODING EVAL DELTA",
		head:  []string{"MODEL", "PASS@1 old", "PASS@1 new", "DELTA", "PASSED old", "PASSED new", "FLAG"},
		right: []bool{false, true, true, true, true, true, false},
	}
	for _, ns := range newR.EvalSummaries {
		os_, ok := oldByModel[ns.Model]
		if !ok {
			continue
		}
		// Pass rates are already percentages, so compare them in points
		// rather than as a percentage of a percentage.
		delta := ns.PassAt1 - os_.PassAt1
		flag := ""
		if delta <= -threshold {
			flag = "REGRESSED"
			regressions++
		} else if delta >= threshold {
			flag = "improved"
		}
		if os_.Problems != ns.Problems {
			flag += fmt.Sprintf(" (problem count changed %d -> %d)", os_.Problems, ns.Problems)
		}
		t.add(ns.Model, evalPct(os_.PassAt1), evalPct(ns.PassAt1),
			fmt.Sprintf("%+.1f pts", delta),
			fmt.Sprintf("%d/%d", os_.Passed, os_.Attempts),
			fmt.Sprintf("%d/%d", ns.Passed, ns.Attempts), flag)
	}
	t.render(w, false)

	type taskKey struct{ model, task string }
	verdict := func(r *Result) map[taskKey]bool {
		m := map[taskKey]bool{}
		for _, rec := range r.EvalRecords {
			k := taskKey{rec.Model, rec.TaskID}
			// With multiple samples, treat any pass as a pass.
			m[k] = m[k] || rec.Passed
		}
		return m
	}
	ov, nv := verdict(oldR), verdict(newR)

	flips := &table{title: "CODING EVAL VERDICT CHANGES", head: []string{"MODEL", "TASK", "WAS", "NOW"}}
	keys := make([]taskKey, 0, len(nv))
	for k := range nv {
		if _, ok := ov[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].task < keys[j].task
	})
	for _, k := range keys {
		if ov[k] == nv[k] {
			continue
		}
		was, now := "fail", "pass"
		if ov[k] {
			was, now = "pass", "fail"
		}
		flips.add(k.model, k.task, was, now)
	}
	flips.render(w, false)

	return regressions
}
