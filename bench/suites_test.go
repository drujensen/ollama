package main

import (
	"strings"
	"testing"
)

func TestAccuracyGraders(t *testing.T) {
	// Graders must tolerate a leading sentence but still require the answer.
	if !lastNumber("The answer is 1776", 1776) {
		t.Fatal("lastNumber missed a trailing answer")
	}
	if lastNumber("1776 is wrong, it is 42", 1776) {
		t.Fatal("lastNumber should take the LAST number")
	}
	if !hasOption("I would say mixed.", "mixed") || hasOption("positive", "mixed") {
		t.Fatal("hasOption wrong")
	}
	if !gradeEmail("Send to dana.reyes@northwind.com.", "dana.reyes@northwind.com") {
		t.Fatal("gradeEmail failed to strip trailing punctuation")
	}
	if !gradePhone("Call 415-555-0132 now", "4155550132") {
		t.Fatal("gradePhone should compare digits only")
	}
	if !letterChoice("B) 29", "b") || letterChoice("A", "b") {
		t.Fatal("letterChoice wrong")
	}
	for _, s := range []string{"PARIS", " PARIS ", "**PARIS**", "PARIS."} {
		if !gradeUpperParis(s) {
			t.Fatalf("gradeUpperParis(%q) failed", s)
		}
	}
	if gradeUpperParis("Paris") {
		t.Fatal("gradeUpperParis must require uppercase")
	}
	if !gradeThreeWords("deep vivid blue") || gradeThreeWords("blue") {
		t.Fatal("gradeThreeWords wrong")
	}
	if !gradeBanana("banana") || gradeBanana("The word is banana") || gradeBanana("banana banana") {
		t.Fatal("gradeBanana must require exactly the single word")
	}
}

func TestExactLineToleratesFormatting(t *testing.T) {
	for _, s := range []string{"amallo", "`amallo`", "  amallo  ", "Here:\namallo\n", "**amallo**"} {
		if !exactLine(s, "amallo") {
			t.Fatalf("exactLine(%q) failed", s)
		}
	}
	if exactLine("amallo extra", "amallo") {
		t.Fatal("exactLine must not accept extra words on the line")
	}
}

// The ported benchmark expected "ammalo" for reversing "ollama", which is not
// the reversal and marked every model wrong. Lock the correct answer in.
func TestReverseOllamaExpectation(t *testing.T) {
	src := "ollama"
	r := []rune(src)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	if got := string(r); got != "amallo" {
		t.Fatalf("reverse(ollama) = %q, want amallo", got)
	}
	for _, task := range accuracyTasks {
		if task.ID == "format-2" && task.Expect != "amallo" {
			t.Fatalf("format-2 expects %q, want amallo", task.Expect)
		}
	}
}

func TestAccuracyTaskSetWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, task := range accuracyTasks {
		if seen[task.ID] {
			t.Fatalf("duplicate task id %q", task.ID)
		}
		seen[task.ID] = true
		if task.Prompt == "" || task.Group == "" || task.Grade == nil {
			t.Fatalf("%s: incomplete task", task.ID)
		}
		// Every task must actually accept its own stated expected answer.
		if task.Expect != "<3 words>" && !task.Grade(task.Expect) {
			t.Fatalf("%s: grader rejects its own expected answer %q", task.ID, task.Expect)
		}
	}
}

// A task must not grade a style judgement. The title-case wording punished a
// model for correctly lowercasing an article mid-title, so the prompt now
// states the capitalization rule explicitly.
func TestFormatTaskIsUnambiguous(t *testing.T) {
	for _, task := range accuracyTasks {
		if task.ID != "format-1" {
			continue
		}
		if strings.Contains(strings.ToLower(task.Prompt), "title case") {
			t.Fatal("format-1 still asks for ambiguous \"title case\"")
		}
		if !task.Grade(task.Expect) {
			t.Fatal("format-1 grader rejects its own expected answer")
		}
		return
	}
	t.Fatal("format-1 task not found")
}

func TestDetectRepetition(t *testing.T) {
	loop := strings.Repeat("the cat sat on the mat ", 20)
	if !detectRepetition(loop) {
		t.Fatal("degenerate loop not detected")
	}
	if detectRepetition("a perfectly ordinary sentence that does not repeat itself at all here") {
		t.Fatal("normal prose flagged as repetitive")
	}
	if detectRepetition("short") {
		t.Fatal("too-short input should not be flagged")
	}
}

func mkCall(name string, args map[string]any) ChatResult {
	return ChatResult{ToolCalls: []ToolCall{{Function: ToolCallFunction{Name: name, Arguments: args}}}}
}

func TestGradeToolTurn(t *testing.T) {
	exp := toolExpect{Name: "read_file", Validate: wantSubstrings("path", "main.go")}

	if out, _, ok := gradeToolTurn(exp, mkCall("read_file", map[string]any{"path": "main.go"})); !ok || out != "pass" {
		t.Fatalf("correct call graded %q", out)
	}
	if out, _, ok := gradeToolTurn(exp, mkCall("bash", map[string]any{"path": "main.go"})); ok || out != "wrong-tool" {
		t.Fatalf("wrong tool graded %q", out)
	}
	if out, _, ok := gradeToolTurn(exp, mkCall("read_file", map[string]any{"path": "other.txt"})); ok || out != "bad-args" {
		t.Fatalf("bad args graded %q", out)
	}
	if out, _, ok := gradeToolTurn(exp, mkCall("read_file", map[string]any{})); ok || out != "bad-args" {
		t.Fatalf("missing arg graded %q", out)
	}
	if out, _, ok := gradeToolTurn(exp, ChatResult{Content: "I would read main.go"}); ok || out != "no-tool-call" {
		t.Fatalf("prose instead of a call graded %q", out)
	}

	// Restraint: calling a tool when told not to is a failure.
	noTool := toolExpect{NoTool: true}
	if out, _, ok := gradeToolTurn(noTool, ChatResult{Content: "4"}); !ok || out != "pass" {
		t.Fatalf("direct answer graded %q", out)
	}
	if out, _, ok := gradeToolTurn(noTool, mkCall("bash", map[string]any{"command": "echo 4"})); ok || out != "unexpected-tool" {
		t.Fatalf("needless tool call graded %q", out)
	}
	if out, _, ok := gradeToolTurn(noTool, ChatResult{Content: "  "}); ok || out != "empty" {
		t.Fatalf("empty answer graded %q", out)
	}
}

func TestArgStrCoercesNonStrings(t *testing.T) {
	// Models sometimes send a number where the schema said string.
	if v, ok := argStr(map[string]any{"port": 8080.0}, "port"); !ok || v != "8080" {
		t.Fatalf("argStr coercion = %q,%v", v, ok)
	}
	if _, ok := argStr(map[string]any{}, "missing"); ok {
		t.Fatal("missing key should report not-ok")
	}
}

func TestStrayMarkupAndBadChars(t *testing.T) {
	if !strayMarkup("done</think> ok") || !strayMarkup("<|im_end|>") {
		t.Fatal("stray markup not detected")
	}
	if strayMarkup("a normal reply about <html> tags") {
		t.Fatal("ordinary angle brackets flagged")
	}
	if !badChars(map[string]any{"s": "bad\x07bell"}) {
		t.Fatal("control character not flagged")
	}
	if badChars(map[string]any{"s": "fine\nwith\tnewlines"}) {
		t.Fatal("newline/tab must be allowed")
	}
}

func TestSummarizeTools(t *testing.T) {
	recs := []ToolRecord{
		{Model: "m", Scenario: "a", Turn: 1, Passed: true, Outcome: "pass"},
		{Model: "m", Scenario: "b", Turn: 1, Passed: false, Outcome: "bad-args", StrayMarkup: true},
		{Model: "m", Scenario: "b", Turn: 2, Passed: true, Outcome: "pass"},
	}
	s := SummarizeTools(recs)[0]
	if s.Turns != 3 || s.Passed != 2 {
		t.Fatalf("turn counts wrong: %+v", s)
	}
	if s.Scenarios != 2 || s.ScenariosOK != 1 {
		t.Fatalf("a scenario with any failed turn must not count as OK: %+v", s)
	}
	if s.StrayMarkup != 1 || s.Outcomes["bad-args"] != 1 {
		t.Fatalf("flags wrong: %+v", s)
	}
}

func TestSummarizeAccuracyGroups(t *testing.T) {
	recs := []AccuracyRecord{
		{Model: "m", TaskID: "math-1", Group: "math", Passed: true},
		{Model: "m", TaskID: "math-2", Group: "math", Passed: false, Empty: true},
		{Model: "m", TaskID: "instr-1", Group: "instructions", Passed: true, Repetitive: true},
	}
	s := SummarizeAccuracy(recs)[0]
	if s.Passed != 2 || s.Total != 3 {
		t.Fatalf("totals wrong: %+v", s)
	}
	if s.ByGroup["math"] != 1 || s.GroupTotal["math"] != 2 {
		t.Fatalf("group tally wrong: %+v", s)
	}
	if s.Repetitive != 1 {
		t.Fatalf("repetition not counted: %+v", s)
	}
	// An empty answer must be distinguishable from a merely wrong one.
	if s.Empty != 1 {
		t.Fatalf("empty output not counted: %+v", s)
	}
}

// A model that reissues an identical tool call is looping rather than
// recovering - the "kept making mistakes it had to correct" symptom, counted.
func TestRepeatedCallDetection(t *testing.T) {
	seen := map[string]int{}
	c1 := ToolCall{Function: ToolCallFunction{Name: "read_file", Arguments: map[string]any{"path": "main.go"}}}
	c2 := ToolCall{Function: ToolCallFunction{Name: "read_file", Arguments: map[string]any{"path": "other.go"}}}

	if isRepeatCall(seen, c1) {
		t.Fatal("first call must not count as a repeat")
	}
	if !isRepeatCall(seen, c1) {
		t.Fatal("identical second call must count as a repeat")
	}
	if isRepeatCall(seen, c2) {
		t.Fatal("different arguments must not count as a repeat")
	}
}

// Argument order must not affect the signature, or repeats go uncounted.
func TestRepeatSignatureStableAcrossKeyOrder(t *testing.T) {
	a := ToolCall{Function: ToolCallFunction{Name: "edit", Arguments: map[string]any{"x": 1, "y": 2}}}
	b := ToolCall{Function: ToolCallFunction{Name: "edit", Arguments: map[string]any{"y": 2, "x": 1}}}
	if callSignature(a) != callSignature(b) {
		t.Fatalf("signatures differ by key order:\n  %s\n  %s", callSignature(a), callSignature(b))
	}
}

func TestToolScenarioSetHasHardCases(t *testing.T) {
	var maxTurns, withErrors, withDistractors int
	for _, sc := range toolScenarios {
		if len(sc.Turns) > maxTurns {
			maxTurns = len(sc.Turns)
		}
		for _, turn := range sc.Turns {
			if turn.InjectError != "" {
				withErrors++
			}
		}
		if len(sc.Tools) >= 10 {
			withDistractors++
		}
	}
	if maxTurns < 4 {
		t.Fatalf("longest scenario is %d turns; agent loops are longer than that", maxTurns)
	}
	if withErrors == 0 {
		t.Fatal("no scenario injects a tool error, so error recovery is untested")
	}
	if withDistractors == 0 {
		t.Fatal("no scenario has a large distractor tool set")
	}
}
