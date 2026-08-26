package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- SEARCH/REPLACE parsing -------------------------------------------------

func TestParseSearchReplaceBasic(t *testing.T) {
	resp := "Here you go:\n\n```\n<<<<<<< SEARCH\nreturn a + b\n=======\nreturn a - b\n>>>>>>> REPLACE\n```\n"
	blocks, err := parseSearchReplace(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	if blocks[0].Search != "return a + b" || blocks[0].Replace != "return a - b" {
		t.Fatalf("bad block: %+v", blocks[0])
	}
}

func TestParseSearchReplaceMultiple(t *testing.T) {
	resp := "<<<<<<< SEARCH\nx = 1\n=======\nx = 2\n>>>>>>> REPLACE\n" +
		"<<<<<<< SEARCH\ny = 3\n=======\ny = 4\n>>>>>>> REPLACE\n"
	blocks, err := parseSearchReplace(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
}

func TestParseSearchReplaceMalformed(t *testing.T) {
	cases := map[string]string{
		"no divider":     "<<<<<<< SEARCH\nfoo\n>>>>>>> REPLACE\n",
		"no terminator":  "<<<<<<< SEARCH\nfoo\n=======\nbar\n",
		"nothing at all": "I would change the function to subtract instead.",
	}
	for name, resp := range cases {
		if _, err := parseSearchReplace(resp); err == nil {
			t.Fatalf("%s: expected an error, got none", name)
		}
	}
}

// A model that emits an empty SEARCH is asking to match anything; reject it.
func TestParseSearchReplaceRejectsEmptySearch(t *testing.T) {
	if _, err := parseSearchReplace("<<<<<<< SEARCH\n\n=======\nx = 1\n>>>>>>> REPLACE\n"); err == nil {
		t.Fatal("empty SEARCH should be rejected")
	}
}

// --- applying ---------------------------------------------------------------

func TestApplySearchReplace(t *testing.T) {
	orig := "def add(a, b):\n    return a + b\n"
	out, err := applySearchReplace(orig, []srBlock{{Search: "return a + b", Replace: "return a - b"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out != "def add(a, b):\n    return a - b\n" {
		t.Fatalf("bad result:\n%s", out)
	}
}

func TestApplyFailsWhenSearchAbsent(t *testing.T) {
	if _, err := applySearchReplace("x = 1\n", []srBlock{{Search: "y = 2", Replace: "y = 3"}}); err == nil {
		t.Fatal("expected failure when SEARCH text is not present")
	}
}

// Ambiguity is a real failure mode: a SEARCH matching several places means the
// model did not include enough context to disambiguate.
func TestApplyFailsWhenSearchAmbiguous(t *testing.T) {
	orig := "a = 1\nb = 1\n"
	if _, err := applySearchReplace(orig, []srBlock{{Search: "= 1", Replace: "= 2"}}); err == nil {
		t.Fatal("expected failure when SEARCH matches more than once")
	}
}

func TestApplyPreservesIndentation(t *testing.T) {
	orig := "class C:\n    def f(self):\n        return 1\n"
	out, err := applySearchReplace(orig, []srBlock{{Search: "        return 1", Replace: "        return 2"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out != "class C:\n    def f(self):\n        return 2\n" {
		t.Fatalf("indentation lost:\n%q", out)
	}
}

func TestApplyMultipleBlocksInOrder(t *testing.T) {
	orig := "x = 1\ny = 2\n"
	out, err := applySearchReplace(orig, []srBlock{
		{Search: "x = 1", Replace: "x = 10"},
		{Search: "y = 2", Replace: "y = 20"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out != "x = 10\ny = 20\n" {
		t.Fatalf("bad result: %q", out)
	}
}

// --- whole-file format ------------------------------------------------------

func TestExtractWholeFile(t *testing.T) {
	resp := "Sure:\n\n```python\ndef f():\n    return 1\n```\n"
	out, err := extractWholeFile(resp)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if out != "def f():\n    return 1" {
		t.Fatalf("bad extract: %q", out)
	}
}

func TestExtractWholeFileRejectsProse(t *testing.T) {
	if _, err := extractWholeFile("I would rewrite the function."); err == nil {
		t.Fatal("prose with no code block should error")
	}
}

// --- drift detection --------------------------------------------------------

// A model that rewrites code it was not asked to touch is a hazard in a real
// repo even when its own change is correct.
func TestUntouchedDrift(t *testing.T) {
	orig := "def a():\n    return 1\n\ndef b():\n    return 2\n"
	good := "def a():\n    return 9\n\ndef b():\n    return 2\n"
	bad := "def a():\n    return 9\n\ndef b():\n    return 22\n"

	if n := untouchedDrift(orig, good, "def b():\n    return 2"); n != 0 {
		t.Fatalf("clean edit reported %d drift", n)
	}
	if n := untouchedDrift(orig, bad, "def b():\n    return 2"); n == 0 {
		t.Fatal("drift in an untouched region was not detected")
	}
}

// --- task set ---------------------------------------------------------------

func TestEditTasksWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, task := range editTasks {
		if seen[task.ID] {
			t.Fatalf("duplicate edit task %q", task.ID)
		}
		seen[task.ID] = true
		if len(task.Files) == 0 || task.Request == "" || task.Test == "" {
			t.Fatalf("%s: incomplete task", task.ID)
		}
		// The reference edit must itself apply and pass, or the task is broken.
		if len(task.Reference) == 0 {
			t.Fatalf("%s: no reference solution to validate the task", task.ID)
		}
	}
	if len(editTasks) < 6 {
		t.Fatalf("want a real task set, got %d", len(editTasks))
	}
}

// Every task's reference edit must pass its own tests in the real sandbox. If
// this fails, the task is broken and a model's score on it means nothing.
func TestEditReferenceSolutionsPass(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python available: %v", err)
	}
	for _, task := range editTasks {
		files, entry := buildEditProgram(filesMap(task.Reference), task)
		res := sb.RunFiles(ctx, files, entry, 30*time.Second)
		if res.Outcome != "pass" {
			t.Errorf("%s: reference edit failed: %s / %s", task.ID, res.Outcome, res.Detail)
		}
	}
}

// The Keep region must actually exist in the original, or drift detection is
// silently disabled for that task.
func TestEditKeepRegionsExist(t *testing.T) {
	for _, task := range editTasks {
		if task.Keep == "" {
			continue
		}
		want := strings.Split(strings.TrimRight(task.Keep, "\n"), "\n")
		found := false
		for _, f := range task.Files {
			if containsLineSeq(strings.Split(f.Content, "\n"), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: Keep region not found verbatim in any file", task.ID)
		}
	}
}
