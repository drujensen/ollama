package main

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestLoadBundledProblems(t *testing.T) {
	probs, err := LoadProblems("")
	if err != nil {
		t.Fatalf("load bundled: %v", err)
	}
	if len(probs) < 10 {
		t.Fatalf("expected a real problem set, got %d", len(probs))
	}
	seen := map[string]bool{}
	for _, p := range probs {
		if seen[p.TaskID] {
			t.Fatalf("duplicate task_id %q", p.TaskID)
		}
		seen[p.TaskID] = true
		if !strings.Contains(p.Prompt, "def "+p.EntryPoint) {
			t.Fatalf("%s: prompt does not define entry point %q", p.TaskID, p.EntryPoint)
		}
		if !strings.Contains(p.Test, "def check(") {
			t.Fatalf("%s: test has no check() function", p.TaskID)
		}
		if p.CanonicalSolution == "" {
			t.Fatalf("%s: no canonical solution to validate the harness against", p.TaskID)
		}
	}
}

// TestCanonicalSolutionsPass is the harness self-test. Every bundled problem's
// reference solution must pass its own tests inside the real sandbox. If this
// fails, a model scoring badly says nothing about the model.
func TestCanonicalSolutionsPass(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python available: %v", err)
	}
	probs, err := LoadProblems("")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		res := sb.Run(ctx, buildProgram(p.CanonicalSolution, p), 30*time.Second)
		if res.Outcome != "pass" {
			t.Errorf("%s (%s): canonical solution did not pass: %s / %s",
				p.TaskID, p.EntryPoint, res.Outcome, res.Detail)
		}
	}
}

// A wrong solution must be reported as a wrong answer, not as an error, or the
// failure breakdown is meaningless.
func TestSandboxClassifiesWrongAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python available: %v", err)
	}
	p := Problem{EntryPoint: "f", Test: "def check(candidate):\n    assert candidate() == 1\n"}

	if got := sb.Run(ctx, buildProgram("def f():\n    return 2\n", p), 15*time.Second); got.Outcome != "wrong-answer" {
		t.Fatalf("wrong result classified as %q (%s)", got.Outcome, got.Detail)
	}
	if got := sb.Run(ctx, buildProgram("def f(:\n", p), 15*time.Second); got.Outcome != "error" {
		t.Fatalf("syntax error classified as %q", got.Outcome)
	}
	if got := sb.Run(ctx, buildProgram("def f():\n    raise KeyError('x')\n", p), 15*time.Second); got.Outcome != "error" {
		t.Fatalf("runtime exception classified as %q", got.Outcome)
	}
	if got := sb.Run(ctx, buildProgram("def f():\n    while True:\n        pass\n", p), 2*time.Second); got.Outcome != "timeout" {
		t.Fatalf("infinite loop classified as %q", got.Outcome)
	}
	if got := sb.Run(ctx, buildProgram("def f():\n    return 1\n", p), 15*time.Second); got.Outcome != "pass" {
		t.Fatalf("correct solution classified as %q (%s)", got.Outcome, got.Detail)
	}
}

func TestSandboxBlocksNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python available: %v", err)
	}
	if !sb.UseNetns {
		t.Skip("host does not support unprivileged network namespaces")
	}
	code := `import socket
socket.create_connection(("1.1.1.1", 53), timeout=3)
`
	p := Problem{EntryPoint: "f", Test: "def check(candidate):\n    pass\n"}
	res := sb.Run(ctx, code+"\ndef f():\n    pass\n\n"+p.Test+"\ncheck(f)\n", 20*time.Second)
	if res.Outcome == "pass" {
		t.Fatal("sandbox reported netns but the network was reachable")
	}
}

func TestExtractCodePrefersEntryPointBlock(t *testing.T) {
	resp := "Here is a helper first:\n\n```python\nimport math\n```\n\nAnd the answer:\n\n```python\ndef solve(x):\n    return x + 1\n```\n"
	got := extractCode(resp, "solve")
	if !strings.Contains(got, "def solve") || strings.Contains(got, "import math") {
		t.Fatalf("picked the wrong block:\n%s", got)
	}
}

func TestExtractCodeFallbacks(t *testing.T) {
	// No fence at all, but the function is there.
	if got := extractCode("def solve(x):\n    return x\n", "solve"); !strings.Contains(got, "def solve") {
		t.Fatalf("bare function not extracted: %q", got)
	}
	// Fence with no language tag.
	if got := extractCode("```\ndef solve():\n    pass\n```", "solve"); !strings.Contains(got, "def solve") {
		t.Fatalf("untagged fence not extracted: %q", got)
	}
	// Truncated response with an unterminated fence.
	got := extractCode("```python\ndef solve():\n    return 1\n", "solve")
	if !strings.Contains(got, "return 1") || strings.Contains(got, "```") {
		t.Fatalf("unterminated fence not recovered: %q", got)
	}
	// Nothing usable.
	if got := extractCode("I cannot help with that.", "solve"); got != "" {
		t.Fatalf("prose should yield no code, got %q", got)
	}
	// A fence that exists but defines something else is still returned as a
	// fallback, so the failure is attributed to the model rather than to us.
	if got := extractCode("```python\ndef other():\n    pass\n```", "solve"); !strings.Contains(got, "def other") {
		t.Fatalf("fallback block not returned: %q", got)
	}
}

func TestBuildProgramCallsCheck(t *testing.T) {
	p := Problem{EntryPoint: "solve", Test: "def check(candidate):\n    assert candidate() == 1\n"}
	got := buildProgram("def solve():\n    return 1\n", p)
	if !strings.HasSuffix(strings.TrimSpace(got), "check(solve)") {
		t.Fatalf("program does not invoke check:\n%s", got)
	}
}

func TestPassAtK(t *testing.T) {
	// All samples pass, so any k passes.
	if got := passAtK(5, 5, 1); math.Abs(got-1) > 1e-9 {
		t.Fatalf("pass@1 with all passing = %v, want 1", got)
	}
	// None pass.
	if got := passAtK(5, 0, 1); got != 0 {
		t.Fatalf("pass@1 with none passing = %v, want 0", got)
	}
	// 1 of 5 passes: pass@1 is 1/5.
	if got := passAtK(5, 1, 1); math.Abs(got-0.2) > 1e-9 {
		t.Fatalf("pass@1 = %v, want 0.2", got)
	}
	// pass@k is monotonically non-decreasing in k.
	prev := 0.0
	for k := 1; k <= 5; k++ {
		got := passAtK(5, 2, k)
		if got < prev-1e-12 {
			t.Fatalf("pass@%d = %v decreased from %v", k, got, prev)
		}
		prev = got
	}
}

func TestSummarizeEval(t *testing.T) {
	recs := []EvalRecord{
		{Model: "m", TaskID: "t1", Passed: true, Outcome: "pass", EvalTokens: 100, GenMS: 1000},
		{Model: "m", TaskID: "t2", Passed: false, Outcome: "wrong-answer", EvalTokens: 200, GenMS: 3000},
		{Model: "m", TaskID: "t3", Passed: false, Outcome: "no-code", Truncated: true},
	}
	out := SummarizeEval(recs, 1)
	if len(out) != 1 {
		t.Fatalf("want one model summary, got %d", len(out))
	}
	s := out[0]
	if s.Problems != 3 || s.Attempts != 3 || s.Passed != 1 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if math.Abs(s.PassAt1-33.333333) > 1e-4 {
		t.Fatalf("pass@1 = %v, want 33.33", s.PassAt1)
	}
	if s.Outcomes["wrong-answer"] != 1 || s.Outcomes["no-code"] != 1 {
		t.Fatalf("outcome breakdown wrong: %+v", s.Outcomes)
	}
	if s.Truncated != 1 {
		t.Fatalf("truncation not counted: %+v", s)
	}
}

func TestClassifyFailure(t *testing.T) {
	if got, _ := classifyFailure("Traceback...\nAssertionError"); got != "wrong-answer" {
		t.Fatalf("AssertionError classified as %q", got)
	}
	if got, _ := classifyFailure("  File \"x\", line 1\nSyntaxError: invalid syntax"); got != "error" {
		t.Fatalf("SyntaxError classified as %q", got)
	}
	if got, detail := classifyFailure(""); got != "error" || detail == "" {
		t.Fatalf("empty stderr should still be an error with detail, got %q/%q", got, detail)
	}
}

// A problem file whose reference solutions do not pass is broken, and every
// model score computed from it is meaningless. referenceProgram must handle
// both shapes: a full solution (our bundled set) and a bare completion that
// needs the prompt prepended (HumanEval).
func TestReferenceProgramHandlesBothShapes(t *testing.T) {
	full := Problem{
		Prompt:            "def f(x):\n    \"\"\"doc\"\"\"\n",
		CanonicalSolution: "def f(x):\n    return x + 1\n",
		EntryPoint:        "f",
		Test:              "def check(candidate):\n    assert candidate(1) == 2\n",
	}
	if got := referenceProgram(full); !strings.Contains(got, "return x + 1") {
		t.Fatalf("full solution not used:\n%s", got)
	}
	// A bare completion is indented body text with no def line of its own.
	completion := Problem{
		Prompt:            "def g(x):\n",
		CanonicalSolution: "    return x * 2\n",
		EntryPoint:        "g",
		Test:              "def check(candidate):\n    assert candidate(2) == 4\n",
	}
	got := referenceProgram(completion)
	if !strings.Contains(got, "def g(x):") || !strings.Contains(got, "return x * 2") {
		t.Fatalf("completion not joined to its prompt:\n%s", got)
	}
}

func TestValidateProblemsCatchesABrokenTask(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python: %v", err)
	}
	good := Problem{TaskID: "ok", EntryPoint: "f",
		CanonicalSolution: "def f():\n    return 1\n",
		Test:              "def check(candidate):\n    assert candidate() == 1\n"}
	bad := Problem{TaskID: "broken", EntryPoint: "f",
		CanonicalSolution: "def f():\n    return 1\n",
		Test:              "def check(candidate):\n    assert candidate() == 99\n"}

	okN, failed := ValidateProblems(ctx, sb, []Problem{good, bad}, 20*time.Second)
	if okN != 1 || len(failed) != 1 || failed[0].TaskID != "broken" {
		t.Fatalf("validation wrong: ok=%d failed=%+v", okN, failed)
	}
}
