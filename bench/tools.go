package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// The tools suite exercises the pattern a coding harness actually drives:
// multi-turn tool calling where each result is fed back and the model must
// keep its arguments well formed. Single-shot code quality can look fine while
// this path fails, which is the gap between a good eval score and an agent
// that misbehaves in practice.

type toolExpect struct {
	NoTool   bool
	Name     string
	Validate func(args map[string]any) (bool, string)
}

type toolTurn struct {
	User   string
	Expect toolExpect
	// Reply is the simulated tool output fed back for the next turn.
	Reply func(args map[string]any) string
	// InjectError makes the tool return a failure instead of a result, so the
	// next turn measures recovery rather than the happy path.
	InjectError string
}

// callSignature identifies a tool call independent of argument ordering, so
// repeated calls are detected reliably.
func callSignature(c ToolCall) string {
	keys := make([]string, 0, len(c.Function.Arguments))
	for k := range c.Function.Arguments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(c.Function.Name)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%v", k, c.Function.Arguments[k])
	}
	return b.String()
}

// isRepeatCall records the call and reports whether it was already made in this
// scenario. Reissuing an identical call after a result or an error is looping,
// not recovering.
func isRepeatCall(seen map[string]int, c ToolCall) bool {
	sig := callSignature(c)
	seen[sig]++
	return seen[sig] > 1
}

type ToolScenario struct {
	ID    string
	Tools []Tool
	Turns []toolTurn
}

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var (
	bashTool = Tool{Type: "function", Function: ToolFunction{
		Name: "bash", Description: "Run a shell command and return its output",
		Parameters: obj(map[string]any{"command": str("the shell command to run")}, "command")}}

	readTool = Tool{Type: "function", Function: ToolFunction{
		Name: "read_file", Description: "Read the full contents of a file",
		Parameters: obj(map[string]any{"path": str("path to the file")}, "path")}}

	editTool = Tool{Type: "function", Function: ToolFunction{
		Name: "edit_file", Description: "Replace an exact string in a file with another string",
		Parameters: obj(map[string]any{
			"path":       str("path to the file"),
			"old_string": str("exact text to replace"),
			"new_string": str("replacement text"),
		}, "path", "old_string", "new_string")}}

	weatherTool = Tool{Type: "function", Function: ToolFunction{
		Name: "get_weather", Description: "Get the current weather for a city",
		Parameters: obj(map[string]any{"city": str("city name")}, "city")}}

	listTool = Tool{Type: "function", Function: ToolFunction{
		Name: "list_files", Description: "List files in a directory",
		Parameters: obj(map[string]any{"path": str("directory path")}, "path")}}

	testTool = Tool{Type: "function", Function: ToolFunction{
		Name: "run_tests", Description: "Run the project test suite and return results",
		Parameters: obj(map[string]any{"path": str("test path or file")}, "path")}}
)

// distractors pad the tool list so selection is a real choice rather than a
// coin flip. A harness typically exposes a dozen or more.
func distractors() []Tool {
	names := []struct{ n, d, a string }{
		{"send_email", "Send an email message", "to"},
		{"create_ticket", "Create an issue ticket", "title"},
		{"query_database", "Run a SQL query", "sql"},
		{"http_request", "Make an HTTP request to a URL", "url"},
		{"translate_text", "Translate text to another language", "text"},
		{"resize_image", "Resize an image file", "path"},
		{"schedule_event", "Add an event to the calendar", "title"},
		{"convert_currency", "Convert between currencies", "amount"},
		{"summarize_doc", "Summarize a document", "path"},
		{"deploy_service", "Deploy a service to production", "service"},
	}
	out := make([]Tool, 0, len(names))
	for _, t := range names {
		out = append(out, Tool{Type: "function", Function: ToolFunction{
			Name: t.n, Description: t.d, Parameters: obj(map[string]any{t.a: str(t.a)}, t.a)}})
	}
	return out
}

// argStr fetches a string argument, tolerating a model that sends a number or
// bool where a string was specified.
func argStr(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	default:
		return fmt.Sprint(t), true
	}
}

func wantSubstrings(key string, subs ...string) func(map[string]any) (bool, string) {
	return func(args map[string]any) (bool, string) {
		s, ok := argStr(args, key)
		if !ok {
			return false, "missing argument " + key
		}
		low := strings.ToLower(s)
		for _, sub := range subs {
			if !strings.Contains(low, strings.ToLower(sub)) {
				return false, fmt.Sprintf("%s=%q missing %q", key, truncate(s, 50), sub)
			}
		}
		return true, ""
	}
}

const sampleGo = `package main

import "fmt"

func main() {
	fmt.Println("hello world")
}
`

var toolScenarios = []ToolScenario{
	{
		ID:    "single-call",
		Tools: []Tool{readTool, bashTool},
		Turns: []toolTurn{{
			User:   "Show me what is inside the file main.go.",
			Expect: toolExpect{Name: "read_file", Validate: wantSubstrings("path", "main.go")},
		}},
	},
	{
		ID:    "arg-fidelity",
		Tools: []Tool{bashTool},
		Turns: []toolTurn{{
			User:   "Run exactly this shell command and nothing else: ls -la /tmp",
			Expect: toolExpect{Name: "bash", Validate: wantSubstrings("command", "ls", "-la", "/tmp")},
		}},
	},
	{
		ID:    "tool-choice",
		Tools: []Tool{bashTool, readTool, weatherTool, editTool},
		Turns: []toolTurn{{
			User:   "What is the weather like in Portland right now?",
			Expect: toolExpect{Name: "get_weather", Validate: wantSubstrings("city", "portland")},
		}},
	},
	{
		// A harness that calls a tool for everything burns turns and confuses
		// its own loop, so restraint is graded too.
		ID:    "no-tool-needed",
		Tools: []Tool{bashTool, readTool, weatherTool},
		Turns: []toolTurn{{
			User:   "What is 2 + 2? Answer directly in your own words. Do not use any tool.",
			Expect: toolExpect{NoTool: true},
		}},
	},
	{
		// Quoting and escaping is where malformed arguments usually surface.
		ID:    "quoting",
		Tools: []Tool{bashTool},
		Turns: []toolTurn{{
			User:   `Run a shell command that prints exactly this text, including the quotes: He said "hello, world" today`,
			Expect: toolExpect{Name: "bash", Validate: wantSubstrings("command", `hello, world`)},
		}},
	},
	{
		// The opencode pattern: read, then edit based on what came back.
		ID:    "multi-turn-edit",
		Tools: []Tool{readTool, editTool},
		Turns: []toolTurn{
			{
				User:   "In the file main.go, change the greeting from \"hello world\" to \"goodbye world\". Read the file first.",
				Expect: toolExpect{Name: "read_file", Validate: wantSubstrings("path", "main.go")},
				Reply:  func(map[string]any) string { return sampleGo },
			},
			{
				Expect: toolExpect{Name: "edit_file", Validate: func(args map[string]any) (bool, string) {
					if ok, why := wantSubstrings("path", "main.go")(args); !ok {
						return false, why
					}
					oldS, _ := argStr(args, "old_string")
					newS, _ := argStr(args, "new_string")
					if !strings.Contains(oldS, "hello world") {
						return false, fmt.Sprintf("old_string=%q does not contain the original text", truncate(oldS, 50))
					}
					if !strings.Contains(newS, "goodbye world") {
						return false, fmt.Sprintf("new_string=%q does not contain the replacement", truncate(newS, 50))
					}
					if strings.Contains(newS, "hello world") {
						return false, "new_string still contains the old greeting"
					}
					return true, ""
				}},
			},
		},
	},
	{
		// Recovery: the tool fails. A model that reissues the identical call is
		// looping; a good one changes approach.
		ID:    "error-recovery",
		Tools: []Tool{readTool, listTool, bashTool},
		Turns: []toolTurn{
			{
				User:        "Read the configuration file config.yaml and tell me the port.",
				Expect:      toolExpect{Name: "read_file", Validate: wantSubstrings("path", "config.yaml")},
				InjectError: "Error: no such file or directory: config.yaml",
			},
			{
				// Anything other than repeating the failed read is acceptable.
				Expect: toolExpect{Validate: func(args map[string]any) (bool, string) {
					if p, ok := argStr(args, "path"); ok && p == "config.yaml" {
						return false, "reissued the identical failing call instead of recovering"
					}
					return true, ""
				}},
			},
		},
	},
	{
		// A dozen extra tools, only one of which fits.
		ID:    "distractors",
		Tools: append([]Tool{readTool, weatherTool, listTool}, distractors()...),
		Turns: []toolTurn{{
			User:   "What is the weather in Denver?",
			Expect: toolExpect{Name: "get_weather", Validate: wantSubstrings("city", "denver")},
		}},
	},
	{
		// Five turns, each depending on the last: the shape of a real agent loop.
		ID:    "long-horizon",
		Tools: []Tool{listTool, readTool, editTool, testTool},
		Turns: []toolTurn{
			{
				User:   "The tests are failing in this project. List the files, read the source, fix the bug, then run the tests.",
				Expect: toolExpect{Name: "list_files"},
				Reply:  func(map[string]any) string { return "calc.py\ntest_calc.py\nREADME.md\n" },
			},
			{
				Expect: toolExpect{Name: "read_file", Validate: wantSubstrings("path", "calc.py")},
				Reply:  func(map[string]any) string { return "def add(a, b):\n    return a - b\n" },
			},
			{
				Expect: toolExpect{Name: "edit_file", Validate: func(args map[string]any) (bool, string) {
					newS, _ := argStr(args, "new_string")
					if !strings.Contains(newS, "a + b") {
						return false, "did not correct the operator to a + b"
					}
					return true, ""
				}},
				Reply: func(map[string]any) string { return "edit applied" },
			},
			{
				Expect: toolExpect{Name: "run_tests"},
				Reply:  func(map[string]any) string { return "1 passed" },
			},
		},
	},
	{
		// The value needed in turn 2 only exists in turn 1's result.
		ID:    "state-tracking",
		Tools: []Tool{bashTool, readTool},
		Turns: []toolTurn{
			{
				User:   "Find which file in this directory is largest, then read it.",
				Expect: toolExpect{Name: "bash"},
				Reply:  func(map[string]any) string { return "  1204 notes.txt\n 98120 ledger.csv\n   441 todo.md\n" },
			},
			{
				Expect: toolExpect{Name: "read_file", Validate: wantSubstrings("path", "ledger.csv")},
			},
		},
	},
	{
		// Two dependent calls: the second must use the first result.
		ID:    "sequential",
		Tools: []Tool{bashTool, readTool},
		Turns: []toolTurn{
			{
				User:   "List the files in the current directory, then read whichever one is a Go source file.",
				Expect: toolExpect{Name: "bash", Validate: wantSubstrings("command", "ls")},
				Reply:  func(map[string]any) string { return "README.md\nserver.go\nnotes.txt\n" },
			},
			{
				Expect: toolExpect{Name: "read_file", Validate: wantSubstrings("path", "server.go")},
			},
		},
	},
}

type ToolRecord struct {
	Model       string  `json:"model"`
	Scenario    string  `json:"scenario"`
	Turn        int     `json:"turn"`
	Passed      bool    `json:"passed"`
	Outcome     string  `json:"outcome"`
	Detail      string  `json:"detail,omitempty"`
	CalledTool  string  `json:"called_tool,omitempty"`
	NumCalls    int     `json:"num_calls"`
	ContentLen  int     `json:"content_len"`
	StrayMarkup bool    `json:"stray_markup"`
	BadChars    bool    `json:"bad_chars"`
	RepeatCall  bool    `json:"repeat_call"`
	EvalTokens  int     `json:"eval_tokens"`
	WallMS      float64 `json:"wall_ms"`
	Err         string  `json:"error,omitempty"`
}

type ToolSummary struct {
	Model       string         `json:"model"`
	Turns       int            `json:"turns"`
	Passed      int            `json:"passed"`
	Percent     float64        `json:"percent"`
	Scenarios   int            `json:"scenarios"`
	ScenariosOK int            `json:"scenarios_fully_passed"`
	Outcomes    map[string]int `json:"outcomes"`
	StrayMarkup int            `json:"stray_markup"`
	BadChars    int            `json:"bad_chars"`
	RepeatCalls int            `json:"repeat_calls"`
	MeanWallMS  float64        `json:"mean_wall_ms"`
}

// strayMarkup catches reasoning or template tags leaking into assistant
// content, which is what shows up in a harness as "weird characters".
func strayMarkup(s string) bool {
	for _, tag := range []string{"<think>", "</think>", "<|im_start|>", "<|im_end|>", "<tool_call>", "</tool_call>"} {
		if strings.Contains(s, tag) {
			return true
		}
	}
	return false
}

// badChars flags control characters and replacement runes in tool arguments,
// which break a harness trying to apply an edit verbatim. It walks the raw
// values rather than marshaled JSON: marshaling escapes control characters
// into printable \uXXXX sequences and would hide exactly what we want to catch.
func badChars(v any) bool {
	switch t := v.(type) {
	case string:
		for _, r := range t {
			if r == '\uFFFD' {
				return true
			}
			if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
				return true
			}
		}
	case map[string]any:
		for _, sub := range t {
			if badChars(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if badChars(sub) {
				return true
			}
		}
	}
	return false
}

func (r *Runner) RunTools(ctx context.Context, m ModelCfg, numPredict int) []ToolRecord {
	var out []ToolRecord

	for _, sc := range toolScenarios {
		if ctx.Err() != nil {
			return out
		}
		msgs := []ChatMessage{}
		seenCalls := map[string]int{}

		for i, turn := range sc.Turns {
			if turn.User != "" {
				msgs = append(msgs, ChatMessage{Role: "user", Content: turn.User})
			}

			rec, res := r.execChat(ctx, m, chatSpec{
				suite:      "tools",
				label:      sc.ID,
				messages:   msgs,
				tools:      sc.Tools,
				numPredict: numPredict,
				iter:       i + 1,
			})
			r.add(rec)

			tr := ToolRecord{
				Model: m.Name, Scenario: sc.ID, Turn: i + 1,
				NumCalls: len(res.ToolCalls), ContentLen: len(res.Content),
				EvalTokens: rec.EvalTokens, WallMS: rec.WallMS,
				StrayMarkup: strayMarkup(res.Content),
			}
			if !rec.OK {
				tr.Outcome, tr.Err = "error", firstLine(rec.Err)
				out = append(out, tr)
				r.toolLine(tr)
				break
			}

			tr.Outcome, tr.Detail, tr.Passed = gradeToolTurn(turn.Expect, res)
			if len(res.ToolCalls) > 0 {
				tr.CalledTool = res.ToolCalls[0].Function.Name
				tr.BadChars = badChars(res.ToolCalls[0].Function.Arguments)
				tr.RepeatCall = isRepeatCall(seenCalls, res.ToolCalls[0])
			}
			out = append(out, tr)
			r.toolLine(tr)

			if !tr.Passed || i == len(sc.Turns)-1 {
				break // a broken turn invalidates the rest of the scenario
			}

			// Feed the call and its simulated result back for the next turn.
			call := res.ToolCalls[0]
			msgs = append(msgs, ChatMessage{Role: "assistant", Content: res.Content, ToolCalls: res.ToolCalls})
			result := ""
			if turn.InjectError != "" {
				result = turn.InjectError
			} else if turn.Reply != nil {
				result = turn.Reply(call.Function.Arguments)
			}
			msgs = append(msgs, ChatMessage{Role: "tool", ToolName: call.Function.Name, Content: result})
		}
	}
	return out
}

func gradeToolTurn(exp toolExpect, res ChatResult) (outcome, detail string, passed bool) {
	if exp.NoTool {
		if len(res.ToolCalls) > 0 {
			return "unexpected-tool", "called " + res.ToolCalls[0].Function.Name + " when none was needed", false
		}
		if strings.TrimSpace(res.Content) == "" {
			return "empty", "no tool call and no answer", false
		}
		return "pass", "", true
	}

	if len(res.ToolCalls) == 0 {
		return "no-tool-call", truncate(strings.TrimSpace(res.Content), 60), false
	}
	call := res.ToolCalls[0]
	if exp.Name != "" && call.Function.Name != exp.Name {
		return "wrong-tool", fmt.Sprintf("called %q, wanted %q", call.Function.Name, exp.Name), false
	}
	if exp.Validate != nil {
		if ok, why := exp.Validate(call.Function.Arguments); !ok {
			return "bad-args", why, false
		}
	}
	return "pass", "", true
}

func (r *Runner) toolLine(tr ToolRecord) {
	if r.quiet {
		return
	}
	flags := ""
	if tr.StrayMarkup {
		flags += " [stray-markup]"
	}
	if tr.BadChars {
		flags += " [bad-chars]"
	}
	status := "FAIL"
	if tr.Passed {
		status = "pass"
	}
	r.logf("    %-6s %-17s t%d %-5s %-16s %s", "tools", tr.Scenario, tr.Turn, status, tr.Outcome,
		truncate(tr.Detail, 56)+flags)
}

func SummarizeTools(recs []ToolRecord) []ToolSummary {
	var order []string
	byModel := map[string][]ToolRecord{}
	for _, r := range recs {
		if _, ok := byModel[r.Model]; !ok {
			order = append(order, r.Model)
		}
		byModel[r.Model] = append(byModel[r.Model], r)
	}

	var out []ToolSummary
	for _, model := range order {
		rs := byModel[model]
		s := ToolSummary{Model: model, Outcomes: map[string]int{}}
		scenarioFail := map[string]bool{}
		scenarioSeen := map[string]bool{}
		var wall float64
		for _, r := range rs {
			s.Turns++
			s.Outcomes[r.Outcome]++
			wall += r.WallMS
			scenarioSeen[r.Scenario] = true
			if r.Passed {
				s.Passed++
			} else {
				scenarioFail[r.Scenario] = true
			}
			if r.StrayMarkup {
				s.StrayMarkup++
			}
			if r.BadChars {
				s.BadChars++
			}
			if r.RepeatCall {
				s.RepeatCalls++
			}
		}
		s.Scenarios = len(scenarioSeen)
		for sc := range scenarioSeen {
			if !scenarioFail[sc] {
				s.ScenariosOK++
			}
		}
		if s.Turns > 0 {
			s.Percent = float64(s.Passed) * 100 / float64(s.Turns)
			s.MeanWallMS = wall / float64(s.Turns)
		}
		out = append(out, s)
	}
	return out
}
