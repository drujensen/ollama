package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const usage = `ollama-bench - measure Ollama models with metrics that mean something

usage:
  ollama-bench [flags]                    run the benchmark
  ollama-bench eval [flags]               run the coding eval (generates code, runs its tests)
  ollama-bench list                       list models the server has loaded/available
  ollama-bench compare OLD.json NEW.json  diff two runs

flags:
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "eval":
			os.Exit(cmdEval(os.Args[2:]))
		case "list":
			os.Exit(cmdList(os.Args[2:]))
		case "compare":
			os.Exit(cmdCompare(os.Args[2:]))
		case "-h", "--help", "help":
			flag.CommandLine.Init("ollama-bench", flag.ExitOnError)
			defineFlags(flag.CommandLine, &flags{})
			fmt.Fprint(os.Stderr, usage)
			flag.CommandLine.PrintDefaults()
			os.Exit(0)
		}
	}
	os.Exit(cmdRun(os.Args[1:]))
}

type flags struct {
	config   string
	url      string
	models   string
	suites   string
	repeat   int
	think    string
	numCtx   int
	timeout  int
	outDir   string
	markdown bool
	quiet    bool
	noWarmup bool
	keep     bool
	cooldown int
}

func defineFlags(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.config, "config", "", "path to JSON config (// comments allowed)")
	fs.StringVar(&f.url, "url", "", "Ollama base URL (default http://localhost:11434)")
	fs.StringVar(&f.models, "models", "", "comma-separated models to benchmark (overrides config)")
	fs.StringVar(&f.suites, "suites", "", "comma-separated suites: latency,prefill,depth,concurrency,sanity,accuracy,tools,edit")
	fs.IntVar(&f.repeat, "repeat", 0, "iterations per prompt (default 3)")
	fs.StringVar(&f.think, "think", "", `thinking level for thinking models: low|medium|high|off`)
	fs.IntVar(&f.numCtx, "num-ctx", 0, "override num_ctx for every model (0 = use the model's own)")
	fs.IntVar(&f.timeout, "timeout", 0, "per-request timeout in seconds (default 1800)")
	fs.StringVar(&f.outDir, "out", "results", "directory for JSON/CSV output (empty = no files)")
	fs.BoolVar(&f.markdown, "markdown", false, "also write a markdown report")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress per-request progress lines")
	fs.BoolVar(&f.noWarmup, "no-warmup", false, "skip the discarded warmup request")
	fs.BoolVar(&f.keep, "keep-loaded", false, "do not unload models between runs (skips cold-load timing)")
	fs.IntVar(&f.cooldown, "cooldown", -1, "seconds to idle between models (default 3)")
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("ollama-bench", flag.ExitOnError)
	var f flags
	defineFlags(fs, &f)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := LoadConfig(f.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	applyFlags(cfg, &f)
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}

	cl := NewClient(cfg.BaseURL, time.Duration(cfg.RequestTimeoutSec)*time.Second)

	// Ctrl-C cancels in-flight work but still writes whatever was measured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := cl.Version(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach Ollama at %s: %v\n", cfg.BaseURL, err)
		return 1
	}

	env := CaptureEnv(ctx, cl, cfg)
	tags, _ := cl.Tags(ctx)

	runner := NewRunner(cfg, cl, f.quiet)
	runner.editTimeout = time.Duration(cfg.Edit.ExecTimeout) * time.Second
	res := &Result{Env: env, Suites: cfg.Suites}
	for _, s := range cfg.Suites {
		if s != "edit" {
			continue
		}
		sb, err := DetectSandbox(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sandbox:", err)
			return 1
		}
		sb.CPUSeconds = cfg.Edit.ExecTimeout
		runner.sandbox = sb
		res.Sandbox = sb.Level
		break
	}

	fmt.Fprintf(os.Stderr, "ollama %s at %s | %d model(s) | suites: %s | repeat %d\n",
		env.OllamaVersion, cfg.BaseURL, len(cfg.Models), strings.Join(cfg.Suites, ","), cfg.Repeat)

	for _, m := range cfg.Models {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "\n>>> %s\n", m.Name)

		if cfg.UnloadBetween {
			if err := cl.Unload(ctx, m.Name); err != nil {
				fmt.Fprintf(os.Stderr, "    warn: unload failed: %v\n", err)
			}
		}

		// Describe first: capabilities decide whether think can be sent at
		// all, and the warmup request would fail if it could not.
		desc := DescribeModel(ctx, cl, m, tags)
		if m.Think != nil && m.Think != false && !desc.Thinks() {
			fmt.Fprintf(os.Stderr, "    note: %s has no thinking capability; ignoring think=%v\n", m.Name, m.Think)
			m.Think = nil
			desc.Think = nil
		}

		if cfg.Warmup {
			// The warmup result is discarded from the metrics but its
			// load_duration is the cold-load figure, which the old script
			// silently folded into every request's total.
			w := runner.exec(ctx, m, runSpec{suite: "warmup", label: "warmup", prompt: "Reply with the word ready.", numPredict: 8, iter: 0})
			runner.add(w)
			if !w.OK {
				fmt.Fprintf(os.Stderr, "    warmup FAILED: %s (skipping model)\n", w.Err)
				desc.Error = w.Err
				res.Models = append(res.Models, desc)
				continue
			}
			desc.ColdLoadMS = w.LoadMS
			fmt.Fprintf(os.Stderr, "    warmup ok, cold load %.1fs\n", w.LoadMS/1000)
		}

		// VRAM residency and the real loaded context length are only knowable
		// once the model is in memory.
		AttachRuntime(ctx, cl, &desc)
		if desc.GPUPercent > 0 && desc.GPUPercent < 99 {
			fmt.Fprintf(os.Stderr, "    warn: only %.0f%% of this model is in VRAM; throughput below is CPU-bound\n", desc.GPUPercent)
		}
		res.Models = append(res.Models, desc)

		for _, suite := range cfg.Suites {
			if ctx.Err() != nil {
				break
			}
			switch suite {
			case "latency":
				runner.RunLatency(ctx, m)
			case "prefill":
				runner.RunPrefill(ctx, m, desc)
			case "depth":
				runner.RunDepth(ctx, m, desc)
			case "concurrency":
				res.ConcBatches = append(res.ConcBatches, runner.RunConcurrency(ctx, m)...)
			case "sanity":
				runner.RunSanity(ctx, m)
			case "accuracy":
				res.AccuracyRecords = append(res.AccuracyRecords, runner.RunAccuracy(ctx, m, cfg.Accuracy.NumPredict)...)
			case "tools":
				res.ToolRecords = append(res.ToolRecords, runner.RunTools(ctx, m, cfg.Tools.NumPredict)...)
			case "edit":
				res.EditRecords = append(res.EditRecords, runner.RunEdit(ctx, m, cfg.Edit.NumPredict, cfg.Edit.MaxAttempts)...)
			}
		}

		if cfg.CooldownSec > 0 && ctx.Err() == nil {
			time.Sleep(time.Duration(cfg.CooldownSec) * time.Second)
		}
	}

	// Warmup records are kept in the JSON for traceability but excluded from
	// every aggregate.
	all := runner.Records()
	res.Records = all
	var scored []Record
	for _, r := range all {
		if r.Suite != "warmup" {
			scored = append(scored, r)
		}
	}
	res.Summaries = Summarizes(scored)
	res.AccuracySummaries = SummarizeAccuracy(res.AccuracyRecords)
	res.ToolSummaries = SummarizeTools(res.ToolRecords)
	res.EditSummaries = SummarizeEdit(res.EditRecords)

	res.Render(os.Stdout, false)

	if f.outDir != "" {
		stamp := env.Timestamp.Format("20060102-150405")
		base := filepath.Join(f.outDir, stamp)
		if err := res.WriteJSON(base + ".json"); err != nil {
			fmt.Fprintln(os.Stderr, "write json:", err)
		} else {
			fmt.Fprintf(os.Stdout, "\nwrote %s.json\n", base)
		}
		if err := res.WriteCSV(base + ".csv"); err != nil {
			fmt.Fprintln(os.Stderr, "write csv:", err)
		} else {
			fmt.Fprintf(os.Stdout, "wrote %s.csv\n", base)
		}
		if f.markdown {
			if err := res.WriteMarkdown(base + ".md"); err != nil {
				fmt.Fprintln(os.Stderr, "write markdown:", err)
			} else {
				fmt.Fprintf(os.Stdout, "wrote %s.md\n", base)
			}
		}
		fmt.Fprintf(os.Stdout, "compare with: ollama-bench compare OLD.json %s.json\n", base)
	}

	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\ninterrupted; partial results written")
		return 130
	}
	for _, r := range scored {
		if !r.OK {
			return 1
		}
	}
	return 0
}

func applyFlags(cfg *Config, f *flags) {
	if f.url != "" {
		cfg.BaseURL = f.url
	}
	if f.repeat > 0 {
		cfg.Repeat = f.repeat
	}
	if f.timeout > 0 {
		cfg.RequestTimeoutSec = f.timeout
	}
	if f.cooldown >= 0 {
		cfg.CooldownSec = f.cooldown
	}
	if f.noWarmup {
		cfg.Warmup = false
	}
	if f.keep {
		cfg.UnloadBetween = false
	}
	if f.suites != "" {
		cfg.Suites = splitCSV(f.suites)
	}
	if f.models != "" {
		cfg.Models = nil
		for _, name := range splitCSV(f.models) {
			cfg.Models = append(cfg.Models, ModelCfg{Name: name})
		}
	}
	if f.think != "" {
		think := any(f.think)
		if f.think == "off" || f.think == "false" {
			think = false
		}
		for i := range cfg.Models {
			cfg.Models[i].Think = think
		}
	}
	if f.numCtx > 0 {
		for i := range cfg.Models {
			cfg.Models[i].Options = mergeOptions(cfg.Models[i].Options, map[string]any{"num_ctx": f.numCtx})
		}
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

const evalUsage = `ollama-bench eval - generate code with each model and execute it against unit tests

Solutions run in a sandbox: a network namespace where the host allows it, CPU
and memory rlimits, an isolated interpreter, and a hard timeout. The isolation
level actually achieved is printed and recorded in the results.

flags:
`

func cmdEval(args []string) int {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	var f flags
	defineFlags(fs, &f)
	problems := fs.String("problems", "", "path to a HumanEval-format JSONL file (default: the bundled problem set)")
	samples := fs.Int("samples", 1, "samples per problem; >1 enables pass@k and turns on sampling")
	temperature := fs.Float64("temperature", 0.2, "sampling temperature, used only when -samples > 1")
	numPredict := fs.Int("num-predict", 4096, "max tokens per solution (thinking models need room; truncation is reported)")
	execTimeout := fs.Int("exec-timeout", 15, "seconds allowed for each candidate to run its tests")
	limit := fs.Int("limit", 0, "run only the first N problems (0 = all)")
	minPass := fs.Float64("min-pass", 0, "exit non-zero if any model scores below this pass@1 percentage")
	validate := fs.Bool("validate", false, "run every reference solution against its own tests and exit; proves the problem file is sound before trusting any model score")
	fs.Usage = func() { fmt.Fprint(os.Stderr, evalUsage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := LoadConfig(f.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	applyFlags(cfg, &f)
	if *samples < 1 {
		fmt.Fprintln(os.Stderr, "-samples must be >= 1")
		return 1
	}

	probs, err := LoadProblems(*problems)
	if err != nil {
		fmt.Fprintln(os.Stderr, "problems:", err)
		return 1
	}
	if *limit > 0 && *limit < len(probs) {
		probs = probs[:*limit]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sb, err := DetectSandbox(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 1
	}
	sb.CPUSeconds = *execTimeout

	if *validate {
		fmt.Fprintf(os.Stdout, "validating %d problems from %s (sandbox: %s)\n",
			len(probs), map[bool]string{true: "bundled", false: *problems}[*problems == ""], sb.Level)
		okN, failed := ValidateProblems(ctx, sb, probs, time.Duration(*execTimeout)*time.Second)
		fmt.Fprintf(os.Stdout, "  %d/%d reference solutions pass\n", okN, len(probs))
		for _, p := range failed {
			fmt.Fprintf(os.Stdout, "  FAILED %s (%s)\n", p.TaskID, p.EntryPoint)
		}
		if len(failed) > 0 {
			return 1
		}
		return 0
	}

	// Validation needs no models; a real run does.
	if len(cfg.Models) == 0 {
		fmt.Fprintln(os.Stderr, "no models configured (use -models or a config file)")
		return 1
	}
	if sb.Level == "none" {
		fmt.Fprintln(os.Stderr, "WARNING: no sandboxing available on this host; generated code will run with your\n"+
			"         normal privileges and network access. Continue only if you trust the models.")
	}

	cl := NewClient(cfg.BaseURL, time.Duration(cfg.RequestTimeoutSec)*time.Second)
	if _, err := cl.Version(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach Ollama at %s: %v\n", cfg.BaseURL, err)
		return 1
	}

	env := CaptureEnv(ctx, cl, cfg)
	tags, _ := cl.Tags(ctx)
	runner := NewRunner(cfg, cl, f.quiet)
	res := &Result{Env: env, Suites: []string{"eval"}, Sandbox: sb.Level, EvalSamples: *samples}

	ec := EvalCfg{
		Samples:      *samples,
		Temperature:  *temperature,
		NumPredict:   *numPredict,
		ExecTimeout:  time.Duration(*execTimeout) * time.Second,
		ProblemsPath: *problems,
	}

	source := "bundled"
	if *problems != "" {
		source = *problems
	}
	fmt.Fprintf(os.Stderr, "ollama %s at %s | %d model(s) | %d problems (%s) | %d sample(s) | sandbox: %s\n",
		env.OllamaVersion, cfg.BaseURL, len(cfg.Models), len(probs), source, *samples, sb.Level)

	for _, m := range cfg.Models {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "\n>>> %s\n", m.Name)

		if cfg.UnloadBetween {
			if err := cl.Unload(ctx, m.Name); err != nil {
				fmt.Fprintf(os.Stderr, "    warn: unload failed: %v\n", err)
			}
		}

		desc := DescribeModel(ctx, cl, m, tags)
		if m.Think != nil && m.Think != false && !desc.Thinks() {
			fmt.Fprintf(os.Stderr, "    note: %s has no thinking capability; ignoring think=%v\n", m.Name, m.Think)
			m.Think = nil
			desc.Think = nil
		}
		if cfg.Warmup {
			w := runner.exec(ctx, m, runSpec{suite: "warmup", label: "warmup", prompt: "Reply with the word ready.", numPredict: 8})
			runner.add(w)
			if !w.OK {
				fmt.Fprintf(os.Stderr, "    warmup FAILED: %s (skipping model)\n", w.Err)
				desc.Error = w.Err
				res.Models = append(res.Models, desc)
				continue
			}
			desc.ColdLoadMS = w.LoadMS
		}
		AttachRuntime(ctx, cl, &desc)
		if desc.GPUPercent > 0 && desc.GPUPercent < 99 {
			fmt.Fprintf(os.Stderr, "    warn: only %.0f%% of this model is in VRAM\n", desc.GPUPercent)
		}
		res.Models = append(res.Models, desc)

		res.EvalRecords = append(res.EvalRecords, runner.RunEval(ctx, m, probs, sb, ec)...)

		if cfg.CooldownSec > 0 && ctx.Err() == nil {
			time.Sleep(time.Duration(cfg.CooldownSec) * time.Second)
		}
	}

	res.Records = runner.Records()
	res.EvalSummaries = SummarizeEval(res.EvalRecords, *samples)
	res.Render(os.Stdout, false)

	if f.outDir != "" {
		base := filepath.Join(f.outDir, env.Timestamp.Format("20060102-150405")+"-eval")
		if err := res.WriteJSON(base + ".json"); err != nil {
			fmt.Fprintln(os.Stderr, "write json:", err)
		} else {
			fmt.Fprintf(os.Stdout, "\nwrote %s.json\n", base)
		}
		if err := res.WriteEvalCSV(base + ".csv"); err != nil {
			fmt.Fprintln(os.Stderr, "write csv:", err)
		} else {
			fmt.Fprintf(os.Stdout, "wrote %s.csv\n", base)
		}
		if f.markdown {
			if err := res.WriteMarkdown(base + ".md"); err != nil {
				fmt.Fprintln(os.Stderr, "write markdown:", err)
			} else {
				fmt.Fprintf(os.Stdout, "wrote %s.md\n", base)
			}
		}
	}

	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\ninterrupted; partial results written")
		return 130
	}
	for _, s := range res.EvalSummaries {
		if *minPass > 0 && s.PassAt1 < *minPass {
			fmt.Fprintf(os.Stderr, "\n%s scored %.1f%%, below the -min-pass threshold of %.1f%%\n",
				s.Model, s.PassAt1, *minPass)
			return 1
		}
	}
	return 0
}

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	url := fs.String("url", "http://localhost:11434", "Ollama base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cl := NewClient(*url, 30*time.Second)
	ctx := context.Background()

	tags, err := cl.Tags(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		return 1
	}
	loaded := map[string]PSModel{}
	if ps, err := cl.PS(ctx); err == nil {
		for _, p := range ps {
			loaded[p.Name] = p
		}
	}

	slices.SortFunc(tags, func(a, b TagModel) int { return strings.Compare(a.Name, b.Name) })
	t := &table{title: "AVAILABLE MODELS", head: []string{"MODEL", "PARAMS", "QUANT", "DISK", "LOADED", "GPU%"}}
	for _, m := range tags {
		state, gpu := "-", "-"
		if p, ok := loaded[m.Name]; ok {
			state = "yes"
			if p.Size > 0 {
				gpu = f0(float64(p.SizeVRAM) * 100 / float64(p.Size))
			}
		}
		t.add(m.Name, dash(m.Details.ParameterSize), dash(m.Details.QuantizationLevel), bytesGB(m.Size), state, gpu)
	}
	t.render(os.Stdout, false)
	return 0
}

func cmdCompare(args []string) int {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	threshold := fs.Float64("threshold", 10, "percent change treated as a regression")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: ollama-bench compare [-threshold N] OLD.json NEW.json")
		return 2
	}
	n, err := Compare(os.Stdout, fs.Arg(0), fs.Arg(1), *threshold)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		return 1
	}
	if n > 0 {
		return 1
	}
	return 0
}
