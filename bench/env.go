package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Env captures everything needed to reproduce or discount a result six months
// from now. The old script recorded none of this, so numbers from two runs were
// not comparable.
type Env struct {
	Timestamp     time.Time      `json:"timestamp"`
	BaseURL       string         `json:"base_url"`
	OllamaVersion string         `json:"ollama_version"`
	Hostname      string         `json:"hostname"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
	NumCPU        int            `json:"num_cpu"`
	MemTotalGB    float64        `json:"mem_total_gb"`
	GitSHA        string         `json:"git_sha,omitempty"`
	GitDirty      bool           `json:"git_dirty,omitempty"`
	BenchOptions  map[string]any `json:"bench_options,omitempty"`
	Repeat        int            `json:"repeat"`
}

// ModelInfo records what was actually loaded, including how much of it landed
// in VRAM. A model that spills to system memory reports terrible throughput,
// and this is the field that explains why.
type ModelInfo struct {
	Name         string   `json:"name"`
	Family       string   `json:"family"`
	Params       string   `json:"parameter_size"`
	Quant        string   `json:"quantization_level"`
	DiskBytes    int64    `json:"disk_bytes"`
	NumCtx       int      `json:"num_ctx"`
	Capabilities []string `json:"capabilities,omitempty"`
	Think        any      `json:"think,omitempty"`

	ResidentBytes int64   `json:"resident_bytes"`
	VRAMBytes     int64   `json:"vram_bytes"`
	GPUPercent    float64 `json:"gpu_percent"`
	ColdLoadMS    float64 `json:"cold_load_ms"`
	Error         string  `json:"error,omitempty"`
}

func CaptureEnv(ctx context.Context, cl *Client, cfg *Config) Env {
	e := Env{
		Timestamp:    time.Now(),
		BaseURL:      cfg.BaseURL,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		MemTotalGB:   memTotalGB(),
		BenchOptions: cfg.Options,
		Repeat:       cfg.Repeat,
	}
	e.Hostname, _ = os.Hostname()
	if v, err := cl.Version(ctx); err == nil {
		e.OllamaVersion = v
	}
	e.GitSHA, e.GitDirty = gitState()
	return e
}

func memTotalGB() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return 0
			}
			return kb / 1024 / 1024
		}
	}
	return 0
}

func gitState() (string, bool) {
	sha, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", false
	}
	status, _ := exec.Command("git", "status", "--porcelain").Output()
	return strings.TrimSpace(string(sha)), len(strings.TrimSpace(string(status))) > 0
}

// DescribeModel merges /api/show, /api/tags and /api/ps into one row. Call it
// after the model is loaded so the /api/ps numbers are populated.
func DescribeModel(ctx context.Context, cl *Client, m ModelCfg, tags []TagModel) ModelInfo {
	info := ModelInfo{Name: m.Name, Think: m.Think}

	if sh, err := cl.Show(ctx, m.Name); err == nil {
		info.Family = sh.Details.Family
		info.Params = sh.Details.ParameterSize
		info.Quant = sh.Details.QuantizationLevel
		info.Capabilities = sh.Capabilities
		info.NumCtx = parseNumCtx(sh.Parameters)
	} else {
		info.Error = err.Error()
	}

	for _, t := range tags {
		if t.Name == m.Name {
			info.DiskBytes = t.Size
			if info.Params == "" {
				info.Params = t.Details.ParameterSize
			}
			if info.Quant == "" {
				info.Quant = t.Details.QuantizationLevel
			}
			break
		}
	}

	AttachRuntime(ctx, cl, &info)
	return info
}

// AttachRuntime fills in the fields that only exist once the model is resident:
// how much of it is in VRAM, and the context window it was actually loaded
// with (which can differ from the Modelfile value).
func AttachRuntime(ctx context.Context, cl *Client, info *ModelInfo) {
	ps, err := cl.PS(ctx)
	if err != nil {
		return
	}
	for _, p := range ps {
		if p.Name == info.Name || p.Model == info.Name {
			info.ResidentBytes = p.Size
			info.VRAMBytes = p.SizeVRAM
			if p.Size > 0 {
				info.GPUPercent = float64(p.SizeVRAM) * 100 / float64(p.Size)
			}
			if p.ContextLength > 0 {
				info.NumCtx = p.ContextLength
			}
			return
		}
	}
}

// Thinks reports whether the model advertises the thinking capability. Sending
// think to a model without it makes the request fail outright.
func (m ModelInfo) Thinks() bool {
	for _, c := range m.Capabilities {
		if c == "thinking" {
			return true
		}
	}
	return false
}

// parseNumCtx reads the num_ctx line out of the Modelfile parameter block that
// /api/show returns as free text.
func parseNumCtx(params string) int {
	for _, line := range strings.Split(params, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "num_ctx" {
			if n, err := strconv.Atoi(strings.Trim(f[1], `"`)); err == nil {
				return n
			}
		}
	}
	return 0
}
