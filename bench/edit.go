package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// The edit suite measures whether a model can change an existing file, which
// is what a coding agent actually does. Generating a function from scratch and
// surgically editing one that already exists are different skills, and only the
// second one explains a harness that "struggles editing files".
//
// The headline metric is attempts-per-success: how many tries before the edit
// applies cleanly. A model that always lands it first time feels very different
// in an agent loop from one that needs two corrections, even when both
// eventually produce the right answer.

type srBlock struct {
	Search  string
	Replace string
}

const (
	srStart = "<<<<<<< SEARCH"
	srMid   = "======="
	srEnd   = ">>>>>>> REPLACE"
)

// parseSearchReplace extracts aider/opencode-style edit blocks. Malformed
// output is an error rather than a silent skip: emitting an unparseable block
// is exactly the failure we are measuring.
func parseSearchReplace(resp string) ([]srBlock, error) {
	var out []srBlock
	rest := resp
	for {
		i := strings.Index(rest, srStart)
		if i < 0 {
			break
		}
		rest = rest[i+len(srStart):]
		rest = strings.TrimPrefix(rest, "\r")
		rest = strings.TrimPrefix(rest, "\n")

		m := strings.Index(rest, srMid)
		e := strings.Index(rest, srEnd)
		if m < 0 || e < 0 || m > e {
			return nil, fmt.Errorf("malformed block: missing %q or %q in the right order", srMid, srEnd)
		}
		search := rest[:m]
		afterMid := rest[m+len(srMid):]
		afterMid = strings.TrimPrefix(afterMid, "\r")
		afterMid = strings.TrimPrefix(afterMid, "\n")

		e2 := strings.Index(afterMid, srEnd)
		if e2 < 0 {
			return nil, fmt.Errorf("malformed block: missing %q", srEnd)
		}
		replace := afterMid[:e2]
		rest = afterMid[e2+len(srEnd):]

		search = strings.Trim(search, "\r\n")
		replace = strings.Trim(replace, "\r\n")
		if strings.TrimSpace(search) == "" {
			return nil, fmt.Errorf("SEARCH block is empty, which would match anywhere")
		}
		out = append(out, srBlock{Search: search, Replace: replace})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no SEARCH/REPLACE block found in the response")
	}
	return out, nil
}

// applySearchReplace requires each SEARCH to match exactly once. Zero matches
// means the model invented text; several means it gave too little context to
// disambiguate. Both are real failures a harness would hit.
func applySearchReplace(original string, blocks []srBlock) (string, error) {
	out := original
	for i, b := range blocks {
		n := strings.Count(out, b.Search)
		switch {
		case n == 0:
			return "", fmt.Errorf("block %d: SEARCH text not found in the file", i+1)
		case n > 1:
			return "", fmt.Errorf("block %d: SEARCH text matches %d locations, not unique", i+1, n)
		}
		out = strings.Replace(out, b.Search, b.Replace, 1)
	}
	return out, nil
}

var wholeFileRE = regexp.MustCompile("(?s)```(?:python|py)?[ \\t]*\\r?\\n(.*?)```")

func extractWholeFile(resp string) (string, error) {
	m := wholeFileRE.FindStringSubmatch(resp)
	if m == nil {
		return "", fmt.Errorf("no fenced code block found")
	}
	s := strings.Trim(m[1], "\r\n")
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("code block is empty")
	}
	return s, nil
}

// untouchedDrift reports how many lines of a region that should not have
// changed did change. Rewriting code it was not asked to touch is dangerous in
// a real repository even when the requested change is correct.
func untouchedDrift(original, edited, region string) int {
	want := strings.Split(strings.TrimRight(region, "\n"), "\n")
	// Match whole lines. Substring matching would consider "return 2" present
	// inside "return 22" and miss the very drift we are looking for.
	if !containsLineSeq(strings.Split(original, "\n"), want) {
		return 0 // region spec is stale; nothing to assert
	}
	if containsLineSeq(strings.Split(edited, "\n"), want) {
		return 0
	}
	return len(want)
}

func containsLineSeq(hay, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(hay) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

type EditFile struct {
	Name    string
	Content string
}

type EditTask struct {
	ID         string
	Difficulty string
	Files      []EditFile
	Request    string
	Test       string // python: def check() with asserts, run after importing the files
	Keep       string // a region that must survive untouched
	Reference  []EditFile
}

func (t EditTask) fileNames() []string {
	out := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		out = append(out, f.Name)
	}
	return out
}

func filesMap(fs []EditFile) map[string]string {
	m := make(map[string]string, len(fs))
	for _, f := range fs {
		m[f.Name] = f.Content
	}
	return m
}

// fileEdit is a SEARCH/REPLACE block bound to the file it targets.
type fileEdit struct {
	File string
	srBlock
}

// parseFileEdits reads blocks that may be preceded by a filename, which is how
// aider and opencode scope an edit to one of several open files. With a single
// file the name may be omitted; with several it is required, because an
// unlabelled block is genuinely ambiguous.
func parseFileEdits(resp string, files []string) ([]fileEdit, error) {
	known := map[string]bool{}
	for _, f := range files {
		known[f] = true
	}

	var out []fileEdit
	rest := resp
	for {
		i := strings.Index(rest, srStart)
		if i < 0 {
			break
		}

		// The filename is the last non-empty, non-fence line before the block.
		name := ""
		for _, l := range reverse(strings.Split(rest[:i], "\n")) {
			l = strings.TrimSpace(l)
			for _, p := range []string{"```python", "```py", "```"} {
				l = strings.TrimPrefix(l, p)
			}
			l = strings.TrimSpace(strings.Trim(l, "`*: "))
			if l != "" {
				name = l
				break
			}
		}

		blocks, err := parseSearchReplace(rest[i:])
		if err != nil {
			return nil, err
		}

		switch {
		case known[name]:
			// labelled correctly
		case name != "":
			// Naming a file that is not in the task is an error even when
			// there is only one candidate: the model invented a target.
			return nil, fmt.Errorf("edit names file %q, which is not part of this task", truncate(name, 40))
		case len(files) == 1:
			name = files[0]
		default:
			return nil, fmt.Errorf("edit block does not say which file it applies to")
		}
		out = append(out, fileEdit{File: name, srBlock: blocks[0]})

		after := rest[i:]
		rest = after[strings.Index(after, srEnd)+len(srEnd):]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no SEARCH/REPLACE block found in the response")
	}
	return out, nil
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// applyFileEdits groups edits by file and applies each in order.
func applyFileEdits(files []EditFile, edits []fileEdit) (map[string]string, error) {
	out := filesMap(files)
	for _, e := range edits {
		cur, ok := out[e.File]
		if !ok {
			return nil, fmt.Errorf("edit targets unknown file %q", e.File)
		}
		next, err := applySearchReplace(cur, []srBlock{e.srBlock})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.File, err)
		}
		out[e.File] = next
	}
	return out, nil
}

type EditRecord struct {
	Model      string  `json:"model"`
	TaskID     string  `json:"task_id"`
	Difficulty string  `json:"difficulty"`
	Format     string  `json:"format"`
	Attempts   int     `json:"attempts"`
	Parsed     bool    `json:"parsed"`
	Applied    bool    `json:"applied"`
	TestsPass  bool    `json:"tests_pass"`
	Drift      int     `json:"drift_lines"`
	Outcome    string  `json:"outcome"`
	Detail     string  `json:"detail,omitempty"`
	EvalTokens int     `json:"eval_tokens"`
	WallMS     float64 `json:"wall_ms"`
}

type EditSummary struct {
	Model         string         `json:"model"`
	Tasks         int            `json:"tasks"`
	Passed        int            `json:"passed"`
	Percent       float64        `json:"percent"`
	FirstTry      int            `json:"first_try"`
	MeanAttempts  float64        `json:"mean_attempts"`
	ParseFailures int            `json:"parse_failures"`
	ApplyFailures int            `json:"apply_failures"`
	TestFailures  int            `json:"test_failures"`
	DriftTasks    int            `json:"drift_tasks"`
	MeanWallMS    float64        `json:"mean_wall_ms"`
	Outcomes      map[string]int `json:"outcomes"`
}

const srInstruction = "You are editing an existing file. Reply with ONE OR MORE edit blocks in exactly this format and nothing else:\n\n" +
	srStart + "\n<exact lines copied from the file>\n" + srMid + "\n<replacement lines>\n" + srEnd + "\n\n" +
	"The SEARCH text must match the file byte for byte, including indentation, and must be unique. " +
	"Change only what the request asks for. Do not explain.\n\n"

func (r *Runner) RunEdit(ctx context.Context, m ModelCfg, numPredict, maxAttempts int) []EditRecord {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var out []EditRecord

	for _, task := range editTasks {
		if ctx.Err() != nil {
			return out
		}
		rec := EditRecord{Model: m.Name, TaskID: task.ID, Difficulty: task.Difficulty, Format: "search-replace"}

		var sb strings.Builder
		sb.WriteString(srInstruction)
		if len(task.Files) > 1 {
			sb.WriteString("Several files are shown. Put the file name on its own line immediately before each edit block.\n\n")
		}
		for _, f := range task.Files {
			fmt.Fprintf(&sb, "%s\n```python\n%s```\n\n", f.Name, f.Content)
		}
		fmt.Fprintf(&sb, "Request: %s", task.Request)
		msgs := []ChatMessage{{Role: "user", Content: sb.String()}}

		var edited map[string]string
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			rec.Attempts = attempt
			gen, chat := r.execChat(ctx, m, chatSpec{
				suite: "edit", label: task.ID, messages: msgs,
				numPredict: numPredict, iter: attempt,
			})
			r.add(gen)
			rec.EvalTokens += gen.EvalTokens
			rec.WallMS += gen.WallMS

			if !gen.OK {
				rec.Outcome, rec.Detail = "error", firstLine(gen.Err)
				break
			}

			edits, err := parseFileEdits(chat.Content, task.fileNames())
			if err != nil {
				rec.Outcome, rec.Detail = "parse-failure", err.Error()
				msgs = append(msgs,
					ChatMessage{Role: "assistant", Content: chat.Content},
					ChatMessage{Role: "user", Content: "That did not parse: " + err.Error() + ". Reply with only correctly formatted SEARCH/REPLACE blocks."})
				continue
			}
			rec.Parsed = true

			result, err := applyFileEdits(task.Files, edits)
			if err != nil {
				rec.Outcome, rec.Detail = "apply-failure", err.Error()
				msgs = append(msgs,
					ChatMessage{Role: "assistant", Content: chat.Content},
					ChatMessage{Role: "user", Content: "That edit did not apply: " + err.Error() + ". The SEARCH text must be copied exactly from the file and match exactly one location."})
				continue
			}
			rec.Applied = true
			edited = result
			break
		}

		if rec.Applied {
			if task.Keep != "" {
				for _, f := range task.Files {
					if d := untouchedDrift(f.Content, edited[f.Name], task.Keep); d > 0 {
						rec.Drift += d
					}
				}
			}
			files, entry := buildEditProgram(edited, task)
			res := r.sandbox.RunFiles(ctx, files, entry, r.editTimeout)
			rec.TestsPass = res.Outcome == "pass"
			if rec.TestsPass {
				rec.Outcome, rec.Detail = "pass", ""
			} else {
				rec.Outcome, rec.Detail = "test-failure", res.Detail
			}
		}
		out = append(out, rec)
		r.editLine(rec)
	}
	return out
}

// buildEditProgram writes the edited files alongside a generated entry point
// that imports them and runs the task's checks.
func buildEditProgram(edited map[string]string, task EditTask) (map[string]string, string) {
	files := make(map[string]string, len(edited)+1)
	for k, v := range edited {
		files[k] = v
	}
	var b strings.Builder
	for _, f := range task.Files {
		mod := strings.TrimSuffix(f.Name, ".py")
		mod = strings.ReplaceAll(mod, "/", ".")
		fmt.Fprintf(&b, "from %s import *\n", mod)
	}
	b.WriteString("\n")
	b.WriteString(task.Test)
	b.WriteString("\n\ncheck()\n")
	const entry = "_check_entry.py"
	files[entry] = b.String()
	return files, entry
}

func (r *Runner) editLine(rec EditRecord) {
	if r.quiet {
		return
	}
	status := "FAIL"
	if rec.Outcome == "pass" {
		status = "pass"
	}
	flags := ""
	if rec.Drift > 0 {
		flags = fmt.Sprintf(" [drift %d lines]", rec.Drift)
	}
	r.logf("    %-5s %-16s %-9s %-5s try=%d %-14s %s", "edit", rec.TaskID, rec.Difficulty,
		status, rec.Attempts, rec.Outcome, truncate(rec.Detail, 50)+flags)
}

func SummarizeEdit(recs []EditRecord) []EditSummary {
	var order []string
	byModel := map[string][]EditRecord{}
	for _, r := range recs {
		if _, ok := byModel[r.Model]; !ok {
			order = append(order, r.Model)
		}
		byModel[r.Model] = append(byModel[r.Model], r)
	}
	var out []EditSummary
	for _, model := range order {
		rs := byModel[model]
		s := EditSummary{Model: model, Outcomes: map[string]int{}}
		var attempts, wall float64
		for _, r := range rs {
			s.Tasks++
			s.Outcomes[r.Outcome]++
			attempts += float64(r.Attempts)
			wall += r.WallMS
			if r.Outcome == "pass" {
				s.Passed++
				if r.Attempts == 1 {
					s.FirstTry++
				}
			}
			switch r.Outcome {
			case "parse-failure":
				s.ParseFailures++
			case "apply-failure":
				s.ApplyFailures++
			case "test-failure":
				s.TestFailures++
			}
			if r.Drift > 0 {
				s.DriftTasks++
			}
		}
		if s.Tasks > 0 {
			s.Percent = float64(s.Passed) * 100 / float64(s.Tasks)
			s.MeanAttempts = attempts / float64(s.Tasks)
			s.MeanWallMS = wall / float64(s.Tasks)
		}
		out = append(out, s)
	}
	return out
}
