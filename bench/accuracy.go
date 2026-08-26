package main

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// The accuracy suite is ported from the project's benchmark.py task set, with
// one substantive change: it grades the content field returned by /api/chat
// rather than the raw /api/generate response. On /api/generate a model whose
// chat template pre-injects <think> emits its reasoning into the response
// channel, and exact-output graders then score the reasoning instead of the
// answer. That artifact cost one model every instruction task despite the
// model answering correctly.

type AccuracyTask struct {
	ID     string
	Group  string
	Prompt string
	Expect string
	Grade  func(string) bool
}

var (
	numRE    = regexp.MustCompile(`-?\d+`)
	wordRE   = regexp.MustCompile(`[a-z0-9_]+`)
	emailRE  = regexp.MustCompile(`[^\s<>()]+@[^\s<>()]+\.[A-Za-z]{2,}`)
	digitsRE = regexp.MustCompile(`\d`)
	letterRE = regexp.MustCompile(`[A-Za-z]`)
)

// lastNumber grades "reply with only the number" style answers by taking the
// final integer in the reply, which tolerates a trailing sentence.
func lastNumber(text string, expected int) bool {
	ms := numRE.FindAllString(text, -1)
	if len(ms) == 0 {
		return false
	}
	n, err := strconv.Atoi(ms[len(ms)-1])
	return err == nil && n == expected
}

func hasOption(text, expected string) bool {
	for _, w := range wordRE.FindAllString(strings.ToLower(text), -1) {
		if w == expected {
			return true
		}
	}
	return false
}

func gradeEmail(text, expected string) bool {
	m := emailRE.FindString(text)
	return strings.EqualFold(strings.Trim(m, ".,"), expected)
}

func gradePhone(text, expected string) bool {
	return strings.Join(digitsRE.FindAllString(text, -1), "") == expected
}

// exactLine accepts the answer if any single line equals the expected string
// once surrounding punctuation and formatting are stripped.
func exactLine(text, expected string) bool {
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`*_\"'."))
		if s == expected {
			return true
		}
	}
	return strings.TrimSpace(text) == expected
}

func letterChoice(text, expected string) bool {
	ls := letterRE.FindAllString(text, -1)
	if len(ls) == 0 {
		return false
	}
	return strings.EqualFold(ls[0], expected)
}

func gradeBanana(text string) bool {
	words := wordRE.FindAllString(strings.ToLower(text), -1)
	n := 0
	for _, w := range words {
		if w == "banana" {
			n++
		}
	}
	return n == 1 && len(words) == 1
}

func gradeThreeWords(text string) bool {
	return len(strings.Fields(strings.TrimSpace(text))) == 3
}

func gradeUpperParis(text string) bool {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(text), ".!\"'`*"))
	return s == "PARIS"
}

var accuracyTasks = []AccuracyTask{
	{"math-1", "math", "What is 37 * 48?\nRespond with only the final number.", "1776",
		func(t string) bool { return lastNumber(t, 1776) }},
	{"math-2", "math", "A bakery sold 145 croissants in the morning and 268 in the afternoon.\nHow many croissants were sold in total that day?\nRespond with only the final number.", "413",
		func(t string) bool { return lastNumber(t, 413) }},
	{"math-3", "math", "What is 1000 - 357 + 88?\nRespond with only the final number.", "731",
		func(t string) bool { return lastNumber(t, 731) }},

	{"classify-1", "classify", "Sentiment: 'The soup was excellent but the service was painfully slow.'\nOptions: positive, negative, mixed.\nReply with exactly one of the options.", "mixed",
		func(t string) bool { return hasOption(t, "mixed") }},
	{"classify-2", "classify", "Intent: 'I need to return a package I bought last week, it arrived broken.'\nOptions: account_help, order_tracking, return_request, billing.\nReply with exactly one of the options.", "return_request",
		func(t string) bool { return hasOption(t, "return_request") }},
	{"classify-3", "classify", "Topic: 'The central bank raised interest rates by a quarter point to cool inflation.'\nOptions: sports, economy, health, technology.\nReply with exactly one of the options.", "economy",
		func(t string) bool { return hasOption(t, "economy") }},

	{"extract-1", "extract", "Extract the email address from: 'Please send the invoice to dana.reyes@northwind.com before Friday.'\nReply with exactly the email address, nothing else.", "dana.reyes@northwind.com",
		func(t string) bool { return gradeEmail(t, "dana.reyes@northwind.com") }},
	{"extract-2", "extract", "Extract the city name from: 'We will ship to Portland, Oregon next Tuesday.'\nReply with exactly the city name, nothing else.", "Portland",
		func(t string) bool { return hasOption(t, "portland") }},
	{"extract-3", "extract", "Extract the phone number from: 'Call 415-555-0132 to confirm.'\nReply with exactly the phone number, nothing else.", "415-555-0132",
		func(t string) bool { return gradePhone(t, "4155550132") }},

	{"choice-1", "choice", "Which of these numbers is prime? A) 51 B) 29 C) 91 D) 15\nReply with only the letter.", "B",
		func(t string) bool { return letterChoice(t, "b") }},
	{"choice-2", "choice", "Which fraction is the largest? A) 9/16 B) 7/11 C) 5/8 D) 2/3\nReply with only the letter.", "D",
		func(t string) bool { return letterChoice(t, "d") }},
	{"choice-3", "choice", "In HTTP, which status code means the resource was not found? A) 500 B) 200 C) 404 D) 401\nReply with only the letter.", "C",
		func(t string) bool { return letterChoice(t, "c") }},

	// "Title case" is ambiguous: Chicago and AP lowercase articles mid-title,
	// so a model answering "...Over the Lazy Dog" is arguably more correct than
	// the naive every-word rule. State the rule explicitly instead of grading
	// a style judgement.
	{"format-1", "format", "Capitalize the first letter of every word in this sentence and output only the result:\nthe quick brown fox jumps over the lazy dog", "The Quick Brown Fox Jumps Over The Lazy Dog",
		func(t string) bool { return exactLine(t, "The Quick Brown Fox Jumps Over The Lazy Dog") }},
	{"format-2", "format", "Reverse the letters of the word 'ollama'\nReply with only the reversed word.", "amallo",
		func(t string) bool { return exactLine(t, "amallo") }},
	{"format-3", "format", "Write the numbers 1 through 10 separated by commas, no spaces, no other characters.", "1,2,3,4,5,6,7,8,9,10",
		func(t string) bool { return exactLine(t, "1,2,3,4,5,6,7,8,9,10") }},

	{"instr-1", "instructions", "Say the word 'banana' exactly once.\nDo not say anything else.", "banana", gradeBanana},
	{"instr-2", "instructions", "What color is the sky on a clear day?\nAnswer using exactly 3 words.", "<3 words>", gradeThreeWords},
	{"instr-3", "instructions", "What is the capital of France?\nAnswer in exactly one word, using only uppercase letters.", "PARIS", gradeUpperParis},
}

type AccuracyRecord struct {
	Model      string  `json:"model"`
	TaskID     string  `json:"task_id"`
	Group      string  `json:"group"`
	Passed     bool    `json:"passed"`
	Expect     string  `json:"expect"`
	Got        string  `json:"got"`
	Empty      bool    `json:"empty"`
	Repetitive bool    `json:"repetitive"`
	Truncated  bool    `json:"truncated"`
	EvalTokens int     `json:"eval_tokens"`
	ThinkChars int     `json:"thinking_chars"`
	WallMS     float64 `json:"wall_ms"`
	Err        string  `json:"error,omitempty"`
}

type AccuracySummary struct {
	Model      string         `json:"model"`
	Passed     int            `json:"passed"`
	Total      int            `json:"total"`
	Percent    float64        `json:"percent"`
	ByGroup    map[string]int `json:"by_group_passed"`
	GroupTotal map[string]int `json:"by_group_total"`
	Empty      int            `json:"empty"`
	Repetitive int            `json:"repetitive"`
	Truncated  int            `json:"truncated"`
	Errors     int            `json:"errors"`
}

func (r *Runner) RunAccuracy(ctx context.Context, m ModelCfg, numPredict int) []AccuracyRecord {
	var out []AccuracyRecord
	for _, t := range accuracyTasks {
		if ctx.Err() != nil {
			return out
		}
		rec, res := r.execChat(ctx, m, chatSpec{
			suite:      "accuracy",
			label:      t.ID,
			messages:   []ChatMessage{{Role: "user", Content: t.Prompt}},
			numPredict: numPredict,
			iter:       1,
		})
		r.add(rec)

		ar := AccuracyRecord{
			Model: m.Name, TaskID: t.ID, Group: t.Group, Expect: t.Expect,
			EvalTokens: rec.EvalTokens, ThinkChars: rec.ThinkingChars,
			WallMS: rec.WallMS, Truncated: rec.Truncated(),
		}
		if !rec.OK {
			ar.Err = firstLine(rec.Err)
			out = append(out, ar)
			r.accuracyLine(ar)
			continue
		}
		got := strings.TrimSpace(res.Content)
		ar.Got = truncate(strings.ReplaceAll(got, "\n", "\\n"), 60)
		// An empty answer is a distinct failure from a wrong one: for a
		// thinking model it usually means the budget went to reasoning.
		ar.Empty = got == ""
		ar.Passed = t.Grade(got)
		ar.Repetitive = detectRepetition(got)
		out = append(out, ar)
		r.accuracyLine(ar)
	}
	return out
}

func (r *Runner) accuracyLine(ar AccuracyRecord) {
	if r.quiet {
		return
	}
	status := "FAIL"
	detail := "got \"" + ar.Got + "\""
	if ar.Empty {
		detail = "empty response"
	}
	if ar.Passed {
		status, detail = "pass", ""
	}
	if ar.Err != "" {
		status, detail = "ERROR", ar.Err
	}
	if ar.Truncated {
		detail += " [truncated]"
	}
	r.logf("    %-9s %-13s %-5s %5d tok  %s", "accuracy", ar.TaskID, status, ar.EvalTokens, truncate(detail, 62))
}

// detectRepetition flags degenerate loops: a 5-word window repeated enough to
// dominate the output.
func detectRepetition(text string) bool {
	words := strings.Fields(text)
	const n = 5
	if len(words) < n {
		return false
	}
	counts := map[string]int{}
	best := 0
	for i := 0; i+n <= len(words); i++ {
		k := strings.Join(words[i:i+n], " ")
		counts[k]++
		if counts[k] > best {
			best = counts[k]
		}
	}
	coverage := float64(best*n) / float64(len(words))
	return best >= 3 && coverage > 0.5
}

func SummarizeAccuracy(recs []AccuracyRecord) []AccuracySummary {
	var order []string
	groups := map[string][]AccuracyRecord{}
	for _, r := range recs {
		if _, ok := groups[r.Model]; !ok {
			order = append(order, r.Model)
		}
		groups[r.Model] = append(groups[r.Model], r)
	}
	var out []AccuracySummary
	for _, model := range order {
		s := AccuracySummary{Model: model, ByGroup: map[string]int{}, GroupTotal: map[string]int{}}
		for _, r := range groups[model] {
			s.Total++
			s.GroupTotal[r.Group]++
			if r.Passed {
				s.Passed++
				s.ByGroup[r.Group]++
			}
			if r.Empty {
				s.Empty++
			}
			if r.Repetitive {
				s.Repetitive++
			}
			if r.Truncated {
				s.Truncated++
			}
			if r.Err != "" {
				s.Errors++
			}
		}
		if s.Total > 0 {
			s.Percent = float64(s.Passed) * 100 / float64(s.Total)
		}
		out = append(out, s)
	}
	return out
}

func accuracyGroups() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range accuracyTasks {
		if !seen[t.Group] {
			seen[t.Group] = true
			out = append(out, t.Group)
		}
	}
	return out
}
