package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal Ollama HTTP client. Stdlib only, no external deps, so the
// benchmark binary builds anywhere Go does.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(base string, timeout time.Duration) *Client {
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

type GenerateRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Stream    bool           `json:"stream"`
	Think     any            `json:"think,omitempty"`
	Format    string         `json:"format,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
}

// Chunk is one NDJSON frame from /api/generate. Intermediate frames carry text;
// the final frame (done=true) carries the server-side timing counters.
type Chunk struct {
	Model              string    `json:"model"`
	CreatedAt          time.Time `json:"created_at"`
	Response           string    `json:"response"`
	Thinking           string    `json:"thinking"`
	Done               bool      `json:"done"`
	DoneReason         string    `json:"done_reason"`
	TotalDuration      int64     `json:"total_duration"`
	LoadDuration       int64     `json:"load_duration"`
	PromptEvalCount    int       `json:"prompt_eval_count"`
	PromptEvalDuration int64     `json:"prompt_eval_duration"`
	EvalCount          int       `json:"eval_count"`
	EvalDuration       int64     `json:"eval_duration"`
	Error              string    `json:"error"`
}

func (c *Client) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

// Generate streams a completion. onChunk is called for every frame with the
// wall-clock instant the frame was decoded, which is what makes TTFT and
// inter-token latency measurable at all. The final frame is returned.
func (c *Client) Generate(ctx context.Context, req GenerateRequest, onChunk func(Chunk, time.Time)) (Chunk, error) {
	var final Chunk
	resp, err := c.post(ctx, "/api/generate", req)
	if err != nil {
		return final, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return final, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		now := time.Now()
		var ch Chunk
		if err := json.Unmarshal(line, &ch); err != nil {
			return final, fmt.Errorf("decode chunk: %w", err)
		}
		if ch.Error != "" {
			return final, fmt.Errorf("server: %s", ch.Error)
		}
		if onChunk != nil {
			onChunk(ch, now)
		}
		if ch.Done {
			final = ch
			return final, nil
		}
	}
	if err := sc.Err(); err != nil {
		return final, fmt.Errorf("read stream: %w", err)
	}
	return final, fmt.Errorf("stream ended without a done frame")
}

// Unload drops a model from memory so the next run measures cold load honestly.
func (c *Client) Unload(ctx context.Context, model string) error {
	resp, err := c.post(ctx, "/api/generate", GenerateRequest{Model: model, KeepAlive: 0})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unload %s: http %d", model, resp.StatusCode)
	}
	return nil
}

type PSModel struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ContextLength int    `json:"context_length"`
}

func (c *Client) PS(ctx context.Context) ([]PSModel, error) {
	var out struct {
		Models []PSModel `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/ps", &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

type TagModel struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Details struct {
		Family            string `json:"family"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
}

func (c *Client) Tags(ctx context.Context) ([]TagModel, error) {
	var out struct {
		Models []TagModel `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

type ShowResponse struct {
	Parameters string `json:"parameters"`
	Details    struct {
		Family            string `json:"family"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
	Capabilities []string `json:"capabilities"`
}

func (c *Client) Show(ctx context.Context, model string) (ShowResponse, error) {
	var out ShowResponse
	resp, err := c.post(ctx, "/api/show", map[string]string{"model": model})
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return out, fmt.Errorf("show %s: http %d: %s", model, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/api/version", &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: http %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// --- /api/chat -------------------------------------------------------------
//
// The chat endpoint is what real harnesses use, and unlike /api/generate it
// separates a model's reasoning into its own field for every model we tested.
// Accuracy and tool suites go through here so a model whose template
// pre-injects <think> is not scored on its own leaked reasoning.

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolCallFunction struct {
	Index     int            `json:"index,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Function ToolCallFunction `json:"function"`
}

type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

type ChatRequest struct {
	Model     string         `json:"model"`
	Messages  []ChatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
	Think     any            `json:"think,omitempty"`
	Format    string         `json:"format,omitempty"`
	Tools     []Tool         `json:"tools,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
}

type ChatChunk struct {
	Model              string      `json:"model"`
	CreatedAt          time.Time   `json:"created_at"`
	Message            ChatMessage `json:"message"`
	Done               bool        `json:"done"`
	DoneReason         string      `json:"done_reason"`
	TotalDuration      int64       `json:"total_duration"`
	LoadDuration       int64       `json:"load_duration"`
	PromptEvalCount    int         `json:"prompt_eval_count"`
	PromptEvalDuration int64       `json:"prompt_eval_duration"`
	EvalCount          int         `json:"eval_count"`
	EvalDuration       int64       `json:"eval_duration"`
	Error              string      `json:"error"`
}

// ChatResult is the accumulated outcome of one streamed chat turn.
type ChatResult struct {
	Content    string
	Thinking   string
	ToolCalls  []ToolCall
	Final      ChatChunk
	FirstToken time.Time
	Gaps       []float64
}

// Chat streams one chat turn, accumulating content, reasoning and tool calls
// separately. onToken fires for each frame carrying text so TTFT and
// inter-token timing stay measurable.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onToken func(at time.Time)) (ChatResult, error) {
	var res ChatResult
	req.Stream = true

	resp, err := c.post(ctx, "/api/chat", req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return res, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var content, thinking strings.Builder
	var last time.Time

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		now := time.Now()
		var ch ChatChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			return res, fmt.Errorf("decode chunk: %w", err)
		}
		if ch.Error != "" {
			return res, fmt.Errorf("server: %s", ch.Error)
		}

		content.WriteString(ch.Message.Content)
		thinking.WriteString(ch.Message.Thinking)
		res.ToolCalls = append(res.ToolCalls, ch.Message.ToolCalls...)

		if ch.Message.Content != "" || ch.Message.Thinking != "" {
			if res.FirstToken.IsZero() {
				res.FirstToken = now
			} else {
				res.Gaps = append(res.Gaps, float64(now.Sub(last).Microseconds())/1000)
			}
			last = now
			if onToken != nil {
				onToken(now)
			}
		}

		if ch.Done {
			res.Final = ch
			res.Content = content.String()
			res.Thinking = thinking.String()
			return res, nil
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("read stream: %w", err)
	}
	return res, fmt.Errorf("stream ended without a done frame")
}
