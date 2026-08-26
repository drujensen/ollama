package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- filename-scoped edit blocks --------------------------------------------

// Real harnesses edit more than one file, so a block must say which file it
// targets. Without this, multi-file tasks cannot be graded at all.
func TestParseFileScopedBlocks(t *testing.T) {
	resp := "app/models.py\n```\n<<<<<<< SEARCH\nx = 1\n=======\nx = 2\n>>>>>>> REPLACE\n```\n" +
		"app/views.py\n```\n<<<<<<< SEARCH\ny = 3\n=======\ny = 4\n>>>>>>> REPLACE\n```\n"
	edits, err := parseFileEdits(resp, []string{"app/models.py", "app/views.py"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(edits) != 2 {
		t.Fatalf("want 2 edits, got %d", len(edits))
	}
	if edits[0].File != "app/models.py" || edits[1].File != "app/views.py" {
		t.Fatalf("files wrong: %+v", edits)
	}
}

// A single-file task should not require the model to name the file.
func TestParseFileScopedDefaultsToSoleFile(t *testing.T) {
	resp := "```\n<<<<<<< SEARCH\nx = 1\n=======\nx = 2\n>>>>>>> REPLACE\n```"
	edits, err := parseFileEdits(resp, []string{"only.py"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(edits) != 1 || edits[0].File != "only.py" {
		t.Fatalf("did not default to the sole file: %+v", edits)
	}
}

// Naming a file that is not in the task is a real failure, not something to
// silently absorb.
func TestParseFileScopedRejectsUnknownFile(t *testing.T) {
	resp := "nope.py\n<<<<<<< SEARCH\nx = 1\n=======\nx = 2\n>>>>>>> REPLACE\n"
	if _, err := parseFileEdits(resp, []string{"real.py"}); err == nil {
		t.Fatal("unknown filename should be rejected")
	}
}

// With several files and no filename given, we cannot know where the edit goes.
func TestParseFileScopedAmbiguousWithoutName(t *testing.T) {
	resp := "<<<<<<< SEARCH\nx = 1\n=======\nx = 2\n>>>>>>> REPLACE\n"
	if _, err := parseFileEdits(resp, []string{"a.py", "b.py"}); err == nil {
		t.Fatal("multi-file task with an unlabelled block should be rejected")
	}
}

// --- multi-file sandbox execution -------------------------------------------

func TestSandboxRunFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("executes python in a sandbox")
	}
	ctx := context.Background()
	sb, err := DetectSandbox(ctx)
	if err != nil {
		t.Skipf("no python: %v", err)
	}
	files := map[string]string{
		"helper.py": "def double(x):\n    return x * 2\n",
		"main.py":   "from helper import double\n\nassert double(3) == 6\n",
	}
	if res := sb.RunFiles(ctx, files, "main.py", 20*time.Second); res.Outcome != "pass" {
		t.Fatalf("multi-file program failed: %s / %s", res.Outcome, res.Detail)
	}
	bad := map[string]string{
		"helper.py": "def double(x):\n    return x * 3\n",
		"main.py":   "from helper import double\n\nassert double(3) == 6\n",
	}
	if res := sb.RunFiles(ctx, bad, "main.py", 20*time.Second); res.Outcome == "pass" {
		t.Fatal("wrong helper should have failed the assertion")
	}
}

// --- the hard task set ------------------------------------------------------

func TestHardEditTasksExist(t *testing.T) {
	var big, multiFile, nearDup int
	for _, task := range editTasks {
		total := 0
		for _, f := range task.Files {
			total += len(strings.Split(f.Content, "\n"))
		}
		if total >= 80 {
			big++
		}
		if len(task.Files) > 1 {
			multiFile++
		}
		if task.Difficulty == "near-duplicate" {
			nearDup++
		}
	}
	if big < 3 {
		t.Fatalf("only %d tasks use a file of real size; small files make SEARCH trivially unique", big)
	}
	if multiFile == 0 {
		t.Fatal("no multi-file task; harnesses edit across files")
	}
	if nearDup == 0 {
		t.Fatal("no near-duplicate task; disambiguation is where SEARCH blocks actually fail")
	}
}

func TestEditTaskFilesAreNamedAndUnique(t *testing.T) {
	for _, task := range editTasks {
		if len(task.Files) == 0 {
			t.Fatalf("%s: no files", task.ID)
		}
		seen := map[string]bool{}
		for _, f := range task.Files {
			if f.Name == "" {
				t.Fatalf("%s: unnamed file", task.ID)
			}
			if seen[f.Name] {
				t.Fatalf("%s: duplicate file %q", task.ID, f.Name)
			}
			seen[f.Name] = true
		}
	}
}
