package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type WebhookClient struct {
	webhookURL string
	httpClient *http.Client
}

func NewWebhookClient(webhookURL string) *WebhookClient {
	return &WebhookClient{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type Message struct {
	Text        string       `json:"text,omitempty"`
	Blocks      []Block      `json:"blocks,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Block struct {
	Type   string      `json:"type"`
	Text   *TextObject `json:"text,omitempty"`
	Fields []TextObject `json:"fields,omitempty"`
}

type TextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Attachment struct {
	Color  string  `json:"color,omitempty"`
	Blocks []Block `json:"blocks,omitempty"`
}

func (c *WebhookClient) Send(ctx context.Context, msg *Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

func (c *WebhookClient) SendText(ctx context.Context, text string) error {
	return c.Send(ctx, &Message{Text: text})
}
