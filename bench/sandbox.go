package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Sandbox runs model-generated code. Executing untrusted output is the one
// genuinely dangerous part of a coding eval, so isolation is layered and the
// level actually achieved is recorded in the report rather than assumed.
type Sandbox struct {
	Python     string // absolute interpreter path, resolved past any shim
	UseNetns   bool   // unshare -rn: no network namespace
	UsePrlimit bool   // CPU, address space and file size caps
	Level      string
	CPUSeconds int
	MemoryMB   int
	FileSizeMB int
}

// DetectSandbox probes what this host supports and degrades explicitly.
func DetectSandbox(ctx context.Context) (Sandbox, error) {
	sb := Sandbox{CPUSeconds: 15, MemoryMB: 2048, FileSizeMB: 16}

	// Resolve the real interpreter once. A pyenv/asdf shim is a shell script
	// that needs its own PATH, which the minimal child environment would not
	// provide.
	out, err := exec.CommandContext(ctx, "python3", "-c", "import sys; print(sys.executable)").Output()
	if err != nil {
		return sb, fmt.Errorf("python3 not usable: %w", err)
	}
	sb.Python = strings.TrimSpace(string(out))
	if sb.Python == "" {
		return sb, fmt.Errorf("could not resolve the python3 interpreter path")
	}

	if _, err := exec.LookPath("prlimit"); err == nil {
		sb.UsePrlimit = true
	}
	// Unprivileged network namespaces need user namespaces enabled; probe
	// rather than assume.
	if _, err := exec.LookPath("unshare"); err == nil {
		if err := exec.CommandContext(ctx, "unshare", "-rn", "true").Run(); err == nil {
			sb.UseNetns = true
		}
	}

	switch {
	case sb.UseNetns && sb.UsePrlimit:
		sb.Level = "netns+rlimits"
	case sb.UsePrlimit:
		sb.Level = "rlimits"
	case sb.UseNetns:
		sb.Level = "netns"
	default:
		sb.Level = "none"
	}
	return sb, nil
}

type ExecResult struct {
	Outcome    string // pass, wrong-answer, error, timeout
	Detail     string
	DurationMS float64
}

// Run writes a single program to a scratch directory and executes it.
func (s Sandbox) Run(ctx context.Context, code string, timeout time.Duration) ExecResult {
	return s.RunFiles(ctx, map[string]string{"candidate.py": code}, "candidate.py", timeout)
}

// RunFiles writes several files into one scratch directory and runs entry from
// there, so a program can import its siblings. Multi-file editing is what a
// coding harness actually does, and a single-file runner cannot grade it.
func (s Sandbox) RunFiles(ctx context.Context, files map[string]string, entry string, timeout time.Duration) ExecResult {
	dir, err := os.MkdirTemp("", "ollama-bench-eval-*")
	if err != nil {
		return ExecResult{Outcome: "error", Detail: "sandbox: " + err.Error()}
	}
	defer os.RemoveAll(dir)

	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return ExecResult{Outcome: "error", Detail: "sandbox: " + err.Error()}
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return ExecResult{Outcome: "error", Detail: "sandbox: " + err.Error()}
		}
	}
	file := filepath.Join(dir, filepath.FromSlash(entry))

	argv := []string{}
	if s.UseNetns {
		argv = append(argv, "unshare", "-rn", "--")
	}
	if s.UsePrlimit {
		argv = append(argv, "prlimit",
			fmt.Sprintf("--cpu=%d", s.CPUSeconds),
			fmt.Sprintf("--as=%d", s.MemoryMB*1024*1024),
			fmt.Sprintf("--fsize=%d", s.FileSizeMB*1024*1024),
			"--nofile=256")
	}
	// -s drops user site-packages. We deliberately do NOT use -I here: it
	// implies -E, which would both ignore PYTHONPATH and remove the script
	// directory from sys.path, breaking the sibling imports that multi-file
	// tasks depend on. The environment is already minimal and explicit below.
	argv = append(argv, s.Python, "-s", file)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "TMPDIR=" + dir,
		"PYTHONDONTWRITEBYTECODE=1", "PYTHONPATH=" + dir, "PYTHONNOUSERSITE=1"}
	// Run in its own process group so a timeout kills any children too;
	// CommandContext alone would only signal the direct child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = nil
	runErr := cmd.Run()
	elapsed := float64(time.Since(start).Microseconds()) / 1000

	res := ExecResult{DurationMS: elapsed}
	if runCtx.Err() == context.DeadlineExceeded {
		res.Outcome = "timeout"
		res.Detail = fmt.Sprintf("exceeded %s", timeout)
		return res
	}
	if runErr == nil {
		res.Outcome = "pass"
		return res
	}

	res.Outcome, res.Detail = classifyFailure(stderr.String())
	return res
}

// classifyFailure separates a wrong answer from code that could not run at all.
// Both are failures, but only one says anything about the model's reasoning.
func classifyFailure(stderr string) (string, string) {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return "error", "no output, non-zero exit"
	}
	lines := strings.Split(trimmed, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])

	switch {
	case strings.Contains(last, "AssertionError"):
		return "wrong-answer", truncate(last, 100)
	case strings.HasPrefix(last, "SyntaxError"), strings.HasPrefix(last, "IndentationError"):
		return "error", truncate(last, 100)
	case strings.Contains(trimmed, "MemoryError"):
		return "error", "MemoryError (hit the sandbox memory cap)"
	}
	return "error", truncate(last, 100)
}
