package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultSlackEndpoint = "https://slack.com/api/chat.postMessage"

// slackClient posts messages via the Slack Web API using a bot token.
type slackClient struct {
	token      string
	endpoint   string
	httpClient *http.Client
}

// NewSlackClient returns a send-only Slack client authenticated with a bot token.
func NewSlackClient(token string) *slackClient {
	return &slackClient{
		token:      token,
		endpoint:   defaultSlackEndpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// PostMessage posts text to a channel via chat.postMessage. Slack returns HTTP
// 200 even on logical errors, so the JSON "ok" field is checked.
func (c *slackClient) PostMessage(ctx context.Context, channel, text string) error {
	body, err := json.Marshal(map[string]string{"channel": channel, "text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: http %d", resp.StatusCode)
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("slack: decode response: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("slack: %s", out.Error)
	}
	return nil
}
