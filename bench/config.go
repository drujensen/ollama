package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is JSON rather than YAML deliberately: it keeps the tool on the
// standard library so the binary builds with no module downloads.
type Config struct {
	BaseURL           string         `json:"base_url"`
	RequestTimeoutSec int            `json:"request_timeout_sec"`
	Repeat            int            `json:"repeat"`
	Warmup            bool           `json:"warmup"`
	UnloadBetween     bool           `json:"unload_between_models"`
	CooldownSec       int            `json:"cooldown_sec"`
	Options           map[string]any `json:"options"`
	Models            []ModelCfg     `json:"models"`
	Suites            []string       `json:"suites"`

	Latency     LatencyCfg     `json:"latency"`
	Prefill     PrefillCfg     `json:"prefill"`
	Depth       DepthCfg       `json:"depth"`
	Concurrency ConcurrencyCfg `json:"concurrency"`
	Sanity      SanityCfg      `json:"sanity"`
	Accuracy    AccuracyCfg    `json:"accuracy"`
	Tools       ToolsCfg       `json:"tools"`
	Edit        EditCfg        `json:"edit"`
}

type EditCfg struct {
	NumPredict int `json:"num_predict"`
	// Retries on a malformed or non-applying edit. Attempts-per-success is the
	// headline metric, so this must be >1 to measure self-correction at all.
	MaxAttempts int `json:"max_attempts"`
	ExecTimeout int `json:"exec_timeout_sec"`
}

type AccuracyCfg struct {
	NumPredict int `json:"num_predict"`
}

type ToolsCfg struct {
	NumPredict int `json:"num_predict"`
}

type ModelCfg struct {
	Name string `json:"name"`
	// Think is the reasoning level for thinking models: "low", "medium",
	// "high", true/false. Ignored by models without the capability.
	Think   any            `json:"think,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

type Prompt struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

type LatencyCfg struct {
	NumPredict int      `json:"num_predict"`
	Prompts    []Prompt `json:"prompts"`
}

type PrefillCfg struct {
	Depths     []int `json:"depths"`
	NumPredict int   `json:"num_predict"`
}

type DepthCfg struct {
	Depths     []int `json:"depths"`
	NumPredict int   `json:"num_predict"`
}

type ConcurrencyCfg struct {
	Levels     []int `json:"levels"`
	NumPredict int   `json:"num_predict"`
	Repeat     int   `json:"repeat"`
}

type SanityCfg struct {
	NumPredict int `json:"num_predict"`
}

func DefaultConfig() *Config {
	return &Config{
		BaseURL:           "http://localhost:11434",
		RequestTimeoutSec: 1800,
		Repeat:            3,
		Warmup:            true,
		UnloadBetween:     true,
		CooldownSec:       3,
		// temperature 0 + a fixed seed makes runs comparable; without them
		// output length varies run to run and so does every derived metric.
		Options: map[string]any{"temperature": 0, "seed": 42},
		Suites:  []string{"latency", "prefill", "depth", "concurrency", "sanity", "accuracy", "tools", "edit"},
		Latency: LatencyCfg{
			NumPredict: 256,
			Prompts: []Prompt{
				{Label: "story", Text: "tell me a story about a frog and a princess"},
				{Label: "cpu-cache", Text: "explain how a CPU cache works"},
				{Label: "bash", Text: "write a bash script that lists the 5 largest files in the current directory, sorted by size"},
				{Label: "tcp-udp", Text: "what are the differences between TCP and UDP?"},
			},
		},
		Prefill:     PrefillCfg{Depths: []int{1000, 4000, 16000}, NumPredict: 8},
		Depth:       DepthCfg{Depths: []int{0, 8000, 32000}, NumPredict: 256},
		Concurrency: ConcurrencyCfg{Levels: []int{1, 2, 4}, NumPredict: 128, Repeat: 1},
		Sanity:      SanityCfg{NumPredict: 2048},
		// Thinking models spend heavily before answering; too small a budget
		// truncates them and scores the truncation instead of the model.
		Accuracy: AccuracyCfg{NumPredict: 2048},
		Tools:    ToolsCfg{NumPredict: 2048},
		Edit:     EditCfg{NumPredict: 4096, MaxAttempts: 3, ExecTimeout: 20},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(newCommentStripper(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("no models configured (use --models or a config file)")
	}
	if c.Repeat < 1 {
		return fmt.Errorf("repeat must be >= 1")
	}
	known := map[string]bool{"latency": true, "prefill": true, "depth": true, "concurrency": true,
		"sanity": true, "accuracy": true, "tools": true, "edit": true}
	for _, s := range c.Suites {
		if !known[s] {
			return fmt.Errorf("unknown suite %q", s)
		}
	}
	if len(c.Latency.Prompts) == 0 {
		return fmt.Errorf("latency suite needs at least one prompt")
	}
	return nil
}

// mergeOptions layers per-run options over per-model over global, without
// mutating any of them.
func mergeOptions(maps ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
