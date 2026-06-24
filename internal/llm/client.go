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

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

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

func (c *OpenAIClient) buildSingleTurnMessages(prompt string, schema any) ([]chatMessage, error) {
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling schema: %w", err)
	}
	systemContent := "You are Fine, a Discord moderation bot. " +
		"Reply only with valid JSON matching the schema provided. " +
		"Output a valid json object.\n\n" +
		string(schemaBytes)
	return []chatMessage{
		{Role: "system", Content: systemContent},
		{Role: "user", Content: prompt},
	}, nil
}

func (c *OpenAIClient) buildMultiTurnMessages(messages []Message, schema any) ([]chatMessage, error) {
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
	return chatMsgs, nil
}

func (c *OpenAIClient) send(ctx context.Context, msgs []chatMessage) (*LLMResponse, error) {
	reqBody := chatRequest{
		Model: c.model,
		ResponseFormat: &responseFormat{
			Type: "json_object",
		},
		Messages: msgs,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("llm: marshaling request body: %w", err)
	}

	c.logger.Info("llm: completion request",
		zap.String("model", c.model),
		zap.Int("messages_len", len(msgs)),
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

func (c *OpenAIClient) Completion(
	ctx context.Context,
	prompt string,
	schema any,
) (*LLMResponse, error) {
	msgs, err := c.buildSingleTurnMessages(prompt, schema)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, msgs)
}

func (c *OpenAIClient) CompletionWithMessages(
	ctx context.Context,
	messages []Message,
	schema any,
) (*LLMResponse, error) {
	msgs, err := c.buildMultiTurnMessages(messages, schema)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, msgs)
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
