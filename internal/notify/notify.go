// Package notify delivers alerts to stdout and webhooks.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
)

var (
	_ alert.Notifier = (*Writer)(nil)
	_ alert.Notifier = (*Webhook)(nil)
)

// Writer prints alerts as single lines
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter creates a notifier writing to w (e.g. os.Stderr)
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Name returns the notifier name
func (n *Writer) Name() string { return "stdout" }

// Notify writes the alert
func (n *Writer) Notify(_ context.Context, a alert.Alert) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err := fmt.Fprintf(n.w, "🚨 %s\n", Text(a))
	return err
}

// Webhook formats
const (
	FormatJSON  = "json"
	FormatSlack = "slack"
)

// Webhook POSTs alerts to a URL, retrying transient failures
type Webhook struct {
	URL     string
	Format  string // json (alert object) or slack ({"text": ...})
	Retries int
	Backoff time.Duration // first retry delay, doubled each attempt
	Client  *http.Client
}

// NewWebhook creates a webhook notifier with sensible defaults
func NewWebhook(url, format string) *Webhook {
	if format == "" {
		format = FormatJSON
	}
	return &Webhook{
		URL:     url,
		Format:  format,
		Retries: 3,
		Backoff: time.Second,
		Client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the notifier name
func (n *Webhook) Name() string { return "webhook" }

// Notify sends the alert, retrying on network errors, 429 and 5xx
func (n *Webhook) Notify(ctx context.Context, a alert.Alert) error {
	var payload interface{} = a
	if n.Format == FormatSlack {
		payload = map[string]string{"text": "🚨 " + slackEscape(Text(a))}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	delay := n.Backoff
	var lastErr error
	for attempt := 0; attempt <= n.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
			case <-time.After(delay):
			}
			delay *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "LogFlux")

		resp, err := n.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("webhook returned %s", resp.Status)
		default:
			return fmt.Errorf("webhook returned %s", resp.Status) // not retryable
		}
	}
	return lastErr
}

// slackEscape escapes the characters Slack treats as control sequences
// ("<NUM>" would otherwise be parsed as a link or mention)
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Text renders an alert as one human-readable line
func Text(a alert.Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s", strings.ToUpper(a.Kind), a.Title)
	if a.Source != "" {
		fmt.Fprintf(&b, " (source: %s)", a.Source)
	}
	if a.Message != "" {
		fmt.Fprintf(&b, " — %s", a.Message)
	}
	if a.Sample != "" {
		sample := a.Sample
		if len(sample) > 200 {
			sample = sample[:200] + "…"
		}
		fmt.Fprintf(&b, " | e.g. %q", sample)
	}
	return b.String()
}
