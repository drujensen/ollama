package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var fenceRE = regexp.MustCompile("(?s)```[a-zA-Z0-9_+-]*\\r?\\n(.*?)```")

// verifyBash parses the returned script with `bash -n`. It is a syntax check
// only: the script is never executed.
func verifyBash(resp string) (bool, string) {
	code := strings.TrimSpace(resp)
	if m := fenceRE.FindStringSubmatch(resp); m != nil {
		code = m[1]
	} else if strings.Contains(resp, "```") {
		return false, "unterminated code fence"
	}
	if code == "" {
		return false, "empty response"
	}

	f, err := os.CreateTemp("", "ollama-bench-*.sh")
	if err != nil {
		return false, "temp file: " + err.Error()
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(code); err != nil {
		f.Close()
		return false, "write: " + err.Error()
	}
	f.Close()

	out, err := exec.Command("bash", "-n", f.Name()).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return false, firstLine(msg)
	}
	return true, fmt.Sprintf("%d bytes parse clean", len(code))
}

func verifyJSON(resp string) (bool, string) {
	s := strings.TrimSpace(resp)
	if s == "" {
		return false, "empty response"
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return false, firstLine(err.Error())
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return false, "valid JSON but not an object"
	}
	var missing []string
	for _, k := range []string{"name", "port"} {
		if _, ok := obj[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return false, "missing key(s): " + strings.Join(missing, ", ")
	}
	return true, "object with name+port"
}

var nonWord = regexp.MustCompile(`[^A-Za-z]`)

func verifyPong(resp string) (bool, string) {
	got := nonWord.ReplaceAllString(resp, "")
	if strings.EqualFold(got, "PONG") {
		return true, "exact"
	}
	return false, fmt.Sprintf("got %q", truncate(strings.TrimSpace(resp), 60))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
