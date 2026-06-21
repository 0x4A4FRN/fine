package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Provider interface {
	Completion(
		ctx context.Context,
		prompt string,
		schema any,
	) (*LLMResponse, error)
	CompletionWithMessages(
		ctx context.Context,
		messages []Message,
		schema any,
	) (*LLMResponse, error)
}

type OpenAIClient struct {
	baseURL    string
	apiKeys    []string
	keyMu      sync.Mutex
	keyIndex   int
	keyReqCnt  int
	keyRotate  int
	model      string
	maxRetries int
	httpClient *http.Client
	logger     *zap.Logger
}

type Option func(*OpenAIClient)

func WithBaseURL(url string) Option {
	return func(c *OpenAIClient) {
		c.baseURL = url
	}
}

func WithAPIKey(key string) Option {
	return func(c *OpenAIClient) {
		if key != "" {
			c.apiKeys = []string{key}
		}
	}
}

func WithAPIKeys(keys []string, rotateEvery int) Option {
	return func(c *OpenAIClient) {
		c.apiKeys = keys
		c.keyRotate = rotateEvery
	}
}

func WithModel(model string) Option {
	return func(c *OpenAIClient) {
		c.model = model
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(c *OpenAIClient) {
		c.httpClient = client
	}
}

func WithLogger(l *zap.Logger) Option {
	return func(c *OpenAIClient) {
		c.logger = l
	}
}

func WithTimeout(d time.Duration) Option {
	return func(c *OpenAIClient) {
		if d > 0 {
			c.httpClient.Timeout = d
		}
	}
}

func WithMaxRetries(n int) Option {
	return func(c *OpenAIClient) {
		c.maxRetries = n
	}
}

func NewOpenAIClient(opts ...Option) *OpenAIClient {
	const (
		defaultBaseURL = "https://api.openai.com/v1"
		defaultModel   = "gpt-4o-mini"
		defaultTimeout = 15 * time.Second
	)

	c := &OpenAIClient{
		baseURL: defaultBaseURL,
		model:   "",
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		logger: zap.NewNop(),
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.model == "" {
		c.model = defaultModel
	}

	if c.maxRetries == 0 {
		c.maxRetries = 2
	}

	return c
}

var _ Provider = (*OpenAIClient)(nil)

// currentAPIKey returns the active API key, rotating to the next key every
// keyRotate requests when multiple keys are configured. For a single key
// (or no keys) the mutex is not acquired — the fast path is lock-free.
func (c *OpenAIClient) currentAPIKey() string {
	if len(c.apiKeys) == 0 {
		return ""
	}
	if len(c.apiKeys) == 1 {
		return c.apiKeys[0]
	}
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	key := c.apiKeys[c.keyIndex]
	c.keyReqCnt++
	if c.keyReqCnt >= c.keyRotate {
		c.keyReqCnt = 0
		c.keyIndex = (c.keyIndex + 1) % len(c.apiKeys)
		c.logger.Info("llm: rotating API key",
			zap.Int("new_key_index", c.keyIndex),
			zap.Int("total_keys", len(c.apiKeys)),
		)
	}
	return key
}

// rotateKeyOn429 advances to the next key immediately after a 429 response.
func (c *OpenAIClient) rotateKeyOn429() {
	if len(c.apiKeys) <= 1 {
		return
	}
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	c.keyIndex = (c.keyIndex + 1) % len(c.apiKeys)
	c.keyReqCnt = 0
	c.logger.Warn("llm: 429 rate limited; rotating API key",
		zap.Int("new_key_index", c.keyIndex),
	)
}

// doWithRetry executes a POST to the chat completions endpoint, recreating the
// request each attempt (the request body is consumed by each Do call). It
// retries on transport errors and 5xx responses with exponential backoff
// (1s, 2s, 4s, …). On 429, the API key is rotated before retrying. 4xx
// responses (other than 429) are returned immediately without retry.
func (c *OpenAIClient) doWithRetry(
	ctx context.Context,
	bodyBytes []byte,
) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.baseURL+"/chat/completions",
			bytes.NewReader(bodyBytes),
		)
		if reqErr != nil {
			return nil, fmt.Errorf("llm: creating request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if key := c.currentAPIKey(); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err = c.httpClient.Do(req)
		if err == nil && resp.StatusCode < 500 {
			break
		}
		if attempt < c.maxRetries {
			if resp != nil {
				if resp.StatusCode == 429 {
					c.rotateKeyOn429()
				}
				resp.Body.Close()
			}
			backoff := time.Duration(1<<attempt) * time.Second
			c.logger.Warn("llm: retrying after error",
				zap.Int("attempt", attempt+1),
				zap.Duration("backoff", backoff),
				zap.Error(err),
			)
			// Context-aware sleep: if the caller's context is
			// canceled (e.g. handler timeout expired), abort
			// immediately instead of sleeping through the
			// backoff and then failing on the next attempt.
			// This prevents the retry loop from outliving the
			// handler and sending stale replies to messages
			// that have already been answered by a newer
			// handler invocation.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}
		// Final attempt failed. If we're returning with both a non-nil
		// response AND a non-nil error (the rare transport-error-with-
		// response case, e.g. redirect failures), close the body now —
		// the caller's `if err != nil { return }` path will skip the
		// `defer resp.Body.Close()` and leak the file descriptor.
		// See Fine Code Review Finding 1.5 (corrected fix).
		if err != nil && resp != nil {
			resp.Body.Close()
			resp = nil
		}
	}
	return resp, err
}

type chatRequest struct {
	Model          string          `json:"model"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Messages       []chatMessage   `json:"messages"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

func (c *OpenAIClient) Completion(
	ctx context.Context,
	prompt string,
	schema any,
) (*LLMResponse, error) {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling schema: %w", err)
	}

	systemContent := "You are Fine, a Discord moderation bot. " +
		"Reply only with valid JSON matching the schema provided. " +
		"Output a valid json object.\n\n" +
		string(schemaBytes)

	reqBody := chatRequest{
		Model: c.model,
		ResponseFormat: &responseFormat{
			Type: "json_object",
		},
		Messages: []chatMessage{
			{Role: "system", Content: systemContent},
			{Role: "user", Content: prompt},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling request body: %w", err)
	}

	c.logger.Info("llm: completion request",
		zap.String("model", c.model),
		zap.Int("prompt_len", len(prompt)),
		zap.Int("messages_len", len(reqBody.Messages)),
	)

	resp, err := c.doWithRetry(ctx, bodyBytes)
	if err != nil {
		c.logger.Error("llm: http transport error",
			zap.String("model", c.model),
			zap.Error(err),
		)
		return nil, fmt.Errorf("llm: sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: reading response body: %w", err)
	}

	c.logger.Info("llm: completion response received",
		zap.String("model", c.model),
		zap.Int("status", resp.StatusCode),
		zap.Int("body_len", len(respBody)),
	)

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("llm: non-200 response",
			zap.String("model", c.model),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body_excerpt", truncateForLog(respBody, 512)),
		)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       respBody,
		}
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("llm: unmarshaling chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		c.logger.Warn("llm: response had no choices", zap.String("model", c.model))
		return nil, fmt.Errorf("llm: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	var llmResp LLMResponse
	if err := json.Unmarshal([]byte(content), &llmResp); err != nil {
		return nil, fmt.Errorf("llm: unmarshaling LLM response: %w", err)
	}

	return &llmResp, nil
}

func (c *OpenAIClient) CompletionWithMessages(
	ctx context.Context,
	messages []Message,
	schema any,
) (*LLMResponse, error) {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling schema: %w", err)
	}

	chatMsgs := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		chatMsgs = append(chatMsgs, chatMessage(m))
	}

	if len(chatMsgs) > 0 && chatMsgs[0].Role == "system" {
		chatMsgs[0].Content += "\n\n" + string(schemaBytes) +
			"\n\nYour reply must be a valid JSON object. Output ONLY valid json."
	} else {
		systemContent := "You are Fine, a Discord moderation bot. " +
			"Reply only with valid JSON matching the schema provided. " +
			"Output a valid json object.\n\n" +
			string(schemaBytes)
		chatMsgs = append([]chatMessage{
			{Role: "system", Content: systemContent},
		}, chatMsgs...)
	}

	reqBody := chatRequest{
		Model: c.model,
		ResponseFormat: &responseFormat{
			Type: "json_object",
		},
		Messages: chatMsgs,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling request body: %w", err)
	}

	c.logger.Info("llm: completion-with-messages request",
		zap.String("model", c.model),
		zap.Int("messages_len", len(reqBody.Messages)),
	)

	resp, err := c.doWithRetry(ctx, bodyBytes)
	if err != nil {
		c.logger.Error("llm: http transport error",
			zap.String("model", c.model),
			zap.Error(err),
		)
		return nil, fmt.Errorf("llm: sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llm: reading response body: %w", err)
	}

	c.logger.Info("llm: completion response received",
		zap.String("model", c.model),
		zap.Int("status", resp.StatusCode),
		zap.Int("body_len", len(respBody)),
	)

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("llm: non-200 response",
			zap.String("model", c.model),
			zap.Int("status", resp.StatusCode),
			zap.ByteString("body_excerpt", truncateForLog(respBody, 512)),
		)
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       respBody,
		}
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("llm: unmarshaling chat response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		c.logger.Warn("llm: response had no choices", zap.String("model", c.model))
		return nil, fmt.Errorf("llm: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	var llmResp LLMResponse
	if err := json.Unmarshal([]byte(content), &llmResp); err != nil {
		return nil, fmt.Errorf("llm: unmarshaling LLM response: %w", err)
	}

	return &llmResp, nil
}

type APIError struct {
	StatusCode int
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf(
		"LLM API returned status %d: %s",
		e.StatusCode,
		string(e.Body),
	)
}

func truncateForLog(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	return b[:max]
}
