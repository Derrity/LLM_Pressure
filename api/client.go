// Package api 提供对 OpenAI 兼容 chat completion 接口的客户端封装。
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 是 OpenAI 兼容接口的客户端
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New 构造一个客户端，默认 10 分钟超时（流式长输出不会被过早打断）
func New(baseURL, apiKey string) *Client {
	if normalized, err := NormalizeBaseURL(baseURL); err == nil {
		baseURL = normalized
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// NormalizeBaseURL accepts host, /v1, or full /v1/chat/completions URLs and
// returns the OpenAI-compatible base URL used before /models and /chat/completions.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("Base URL 不能为空")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("无效 Base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("无效 Base URL %q：需要包含协议和域名", raw)
	}

	u.RawQuery = ""
	u.Fragment = ""

	parts := pathParts(u.Path)
	if len(parts) >= 2 && parts[len(parts)-2] == "chat" && parts[len(parts)-1] == "completions" {
		parts = parts[:len(parts)-2]
	}

	v1Index := -1
	for i, part := range parts {
		if part == "v1" {
			v1Index = i
		}
	}
	if v1Index >= 0 {
		parts = parts[:v1Index+1]
	} else {
		parts = append(parts, "v1")
	}

	u.Path = "/" + strings.Join(parts, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func pathParts(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	raw := strings.Split(path, "/")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

// Model 表示 /models 接口里的一个模型条目
type Model struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type listModelsResp struct {
	Data []Model `json:"data"`
}

// ListModels 拉取模型列表
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: %s", httpError(resp.StatusCode, slurpErr(resp.Body)))
	}
	var out listModelsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 models 响应失败: %w", err)
	}
	return out.Data, nil
}

// Message 是 chat 请求/响应里的一条消息
// Delta 字段在流式响应里复用此结构；ReasoningContent 用于推理模型
// (如 GLM-4.7 / DeepSeek-R1)，其思考过程通过 reasoning_content 流出
type Message struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ChatRequest 是 /chat/completions 的请求体
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature"`
}

// usage 是响应里的 token 用量
type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result 是一次请求的完整测量结果
type Result struct {
	Stream           bool
	HTTPStatus       int
	Err              error
	TotalLatency     time.Duration // 端到端
	TTFT             time.Duration // 首 token 延迟（仅流式）
	GenerationTime   time.Duration // 生成耗时 = Total - TTFT
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	TokenSource      string // "usage" | "estimate" | ""（失败时为空）
}

type chatChoice struct {
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   *usage       `json:"usage"`
}

// Chat 非流式调用，返回单次结果
func (c *Client) Chat(ctx context.Context, req ChatRequest) Result {
	req.Stream = false
	start := time.Now()
	r := Result{Stream: false}

	body, err := json.Marshal(req)
	if err != nil {
		r.Err = err
		return r
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		r.Err = err
		return r
	}
	c.setAuth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(httpReq)
	r.TotalLatency = time.Since(start)
	if err != nil {
		r.Err = err
		return r
	}
	defer resp.Body.Close()
	r.HTTPStatus = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		r.Err = errors.New(httpError(resp.StatusCode, slurpErr(resp.Body)))
		return r
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		r.Err = fmt.Errorf("解析响应失败: %w", err)
		return r
	}
	r.GenerationTime = r.TotalLatency

	if cr.Usage != nil {
		r.PromptTokens = cr.Usage.PromptTokens
		r.CompletionTokens = cr.Usage.CompletionTokens
		r.TotalTokens = cr.Usage.TotalTokens
		r.TokenSource = "usage"
	} else if len(cr.Choices) > 0 {
		// 上游不返回 usage 时按空格分词估算（含推理模型的 reasoning_content）
		msg := cr.Choices[0].Message
		r.CompletionTokens = estimateTokens(msg.Content) + estimateTokens(msg.ReasoningContent)
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
		r.TokenSource = "estimate"
	}
	return r
}

// ChatStream 流式调用，逐 chunk 解析，测量 TTFT 与生成耗时
func (c *Client) ChatStream(ctx context.Context, req ChatRequest) Result {
	req.Stream = true
	start := time.Now()
	r := Result{Stream: true}

	body, err := json.Marshal(req)
	if err != nil {
		r.Err = err
		return r
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		r.Err = err
		return r
	}
	c.setAuth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		r.TotalLatency = time.Since(start)
		r.Err = err
		return r
	}
	defer resp.Body.Close()
	r.HTTPStatus = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		r.TotalLatency = time.Since(start)
		r.Err = errors.New(httpError(resp.StatusCode, slurpErr(resp.Body)))
		return r
	}

	scanner := bufio.NewScanner(resp.Body)
	// 单行可能很长（大 chunk），增大缓冲
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	firstToken := false
	var contentBuf strings.Builder
	var reasoningBuf strings.Builder
	var lastUsage *usage

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk chatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// 个别 chunk 解析失败不致命，跳过
			continue
		}
		if len(chunk.Choices) > 0 {
			d := chunk.Choices[0].Delta
			// 首 token：content 或 reasoning_content 都算（推理模型先吐思考 token）
			if !firstToken && (d.Content != "" || d.ReasoningContent != "") {
				r.TTFT = time.Since(start)
				firstToken = true
			}
			if d.Content != "" {
				contentBuf.WriteString(d.Content)
			}
			if d.ReasoningContent != "" {
				reasoningBuf.WriteString(d.ReasoningContent)
			}
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		r.TotalLatency = time.Since(start)
		r.Err = fmt.Errorf("读取 SSE 流失败: %w", err)
		return r
	}
	r.TotalLatency = time.Since(start)

	if !firstToken {
		// 没收到任何 token 内容
		r.Err = errors.New("流式响应未返回任何 token")
		return r
	}
	r.GenerationTime = r.TotalLatency - r.TTFT

	if lastUsage != nil {
		r.PromptTokens = lastUsage.PromptTokens
		r.CompletionTokens = lastUsage.CompletionTokens
		r.TotalTokens = lastUsage.TotalTokens
		r.TokenSource = "usage"
	} else {
		// 估算时把 content + reasoning 都计入（推理模型的思考 token 也是输出）
		r.CompletionTokens = estimateTokens(contentBuf.String()) + estimateTokens(reasoningBuf.String())
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
		r.TokenSource = "estimate"
	}
	return r
}

func (c *Client) setAuth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

// estimateTokens 简单按空白切分估算 token 数；中文偏差较大但够用作 fallback
func estimateTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Fields(s))
}

func slurpErr(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 16*1024))
	return strings.TrimSpace(string(b))
}

func httpError(status int, body string) string {
	return fmt.Sprintf("HTTP %d: %s", status, summarizeErrorBody(body))
}

func summarizeErrorBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "empty response body"
	}

	msg, typ, code := parseErrorJSON(body)
	if msg == "" {
		return compactWhitespace(body, 600)
	}

	if inner := extractJSONObject(msg); inner != "" {
		innerMsg, innerType, innerCode := parseErrorJSON(inner)
		if innerMsg != "" {
			msg = innerMsg
			if typ == "" {
				typ = innerType
			}
			if code == "" {
				code = innerCode
			}
		}
	}

	msg = compactWhitespace(stripTrailingRequestID(msg), 500)
	meta := make([]string, 0, 2)
	if typ != "" {
		meta = append(meta, "type="+typ)
	}
	if code != "" {
		meta = append(meta, "code="+code)
	}
	if len(meta) > 0 {
		return msg + " (" + strings.Join(meta, ", ") + ")"
	}
	return msg
}

func parseErrorJSON(raw string) (message, typ, code string) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", "", ""
	}

	if errObj, ok := root["error"].(map[string]any); ok {
		return stringField(errObj, "message"), stringField(errObj, "type"), valueString(errObj["code"])
	}

	if errorsList, ok := root["errors"].([]any); ok && len(errorsList) > 0 {
		if first, ok := errorsList[0].(map[string]any); ok {
			return stringField(first, "message"), stringField(first, "type"), valueString(first["code"])
		}
	}

	return stringField(root, "message"), stringField(root, "type"), valueString(root["code"])
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case bool:
		return fmt.Sprintf("%t", x)
	default:
		return fmt.Sprint(x)
	}
}

func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func stripTrailingRequestID(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ")") {
		if idx := strings.LastIndex(s, " ("); idx >= 0 {
			tail := s[idx+2 : len(s)-1]
			if looksLikeRequestID(tail) {
				return strings.TrimSpace(s[:idx])
			}
		}
	}
	return s
}

func looksLikeRequestID(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func compactWhitespace(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxLen > 0 && len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}
