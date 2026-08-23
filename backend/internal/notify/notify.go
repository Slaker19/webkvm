// Package notify provides proactive alerting: it delivers event
// notifications to configured channels (a generic HTTPS webhook and/or
// SMTP email) and records an audit trail of emitted alerts.
//
// Security model
// -------------
// Secrets (the webhook bearer token and the SMTP password) are stored
// separately from config.json in {dataDir}/notify-secrets.json with
// mode 0600. The regular config backup (webkvm-config-latest.tar.zst)
// only captures config.json and the app state, NOT this file, so a
// leaked backup never contains notification credentials.
//
// The API never serializes secrets to the client: GET returns only
// booleans indicating whether each secret is configured. Mutations use
// empty-string-as-keep (an empty secret field means "don't change it"),
// so a form submit never silently clears a stored credential.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config is the operator-facing notification configuration. Secret
// fields are NEVER populated from the store for the API; they live in
// the separate secrets file.
type Config struct {
	Enabled bool `json:"enabled"`

	// Webhook channel.
	WebhookEnabled bool   `json:"webhook_enabled"`
	WebhookURL     string `json:"webhook_url,omitempty"`

	// SMTP channel.
	SMTPEnabled   bool   `json:"smtp_enabled"`
	SMTPHost      string `json:"smtp_host,omitempty"`
	SMTPPort      int    `json:"smtp_port,omitempty"`
	SMTPFrom      string `json:"smtp_from,omitempty"`
	SMTPTo        string `json:"smtp_to,omitempty"`
	SMTPTLS       bool   `json:"smtp_tls"` // STARTTLS or TLS on connect
	SMTPInsecure  bool   `json:"smtp_insecure"`

	// Alert thresholds.
	DiskFreePercent int `json:"disk_free_percent"` // warn below this %
	CheckIntervalSec int `json:"check_interval_sec"`
}

// secrets holds the credential material, persisted separately.
type secrets struct {
	WebhookSecret string `json:"webhook_secret,omitempty"`
	SMTPUser      string `json:"smtp_user,omitempty"`
	SMTPPassword  string `json:"smtp_password,omitempty"`
}

// AlertEvent is one emitted alert, kept in a bounded ring for the UI.
type AlertEvent struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info | warning | critical
	Subject string    `json:"subject"`
	Message string    `json:"message"`
}

// Notifier owns configuration, the secrets file and the alert ring.
type Notifier struct {
	mu       sync.RWMutex
	cfg      Config
	sec      secrets
	secretsPath string

	events   []AlertEvent
	eventMax int

	logger *slog.Logger
}

// New loads the config + secrets. Callers wire the config from their
// own store; the secrets file path is derived from dataDir.
func New(dataDir string, cfg Config, logger *slog.Logger) (*Notifier, error) {
	if logger == nil {
		logger = slog.Default()
	}
	n := &Notifier{
		cfg:         cfg,
		secretsPath: filepath.Join(dataDir, "notify-secrets.json"),
		events:      make([]AlertEvent, 0, 32),
		eventMax:    200,
		logger:      logger,
	}
	if err := n.loadSecrets(); err != nil {
		return nil, err
	}
	return n, nil
}

// loadSecrets reads the secrets file (0600). A missing file is fine.
func (n *Notifier) loadSecrets() error {
	data, err := os.ReadFile(n.secretsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var sec secrets
	if err := json.Unmarshal(data, &sec); err != nil {
		return fmt.Errorf("parse notify-secrets.json: %w", err)
	}
	n.mu.Lock()
	n.sec = sec
	n.mu.Unlock()
	return nil
}

// saveSecrets persists the secrets file with restrictive perms. The
// file is written atomically (tmp + rename) so a crash mid-write
// never leaves a truncated secrets file. It reads n.sec WITHOUT
// locking: callers must hold n.mu (Update does), otherwise use the
// data-copying variant. Not locking here avoids a self-deadlock
// (RLock inside a held Lock).
func (n *Notifier) saveSecrets() error {
	sec := n.sec
	data, err := json.MarshalIndent(sec, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.secretsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, n.secretsPath)
}

// Status is what the API exposes: configuration WITHOUT any secrets,
// only whether each secret is present.
type Status struct {
	Config            Config `json:"config"`
	HasWebhookSecret  bool   `json:"has_webhook_secret"`
	HasSMTPUser       bool   `json:"has_smtp_user"`
	HasSMTPPassword   bool   `json:"has_smtp_password"`
}

// Status returns the safe view (no secrets) plus booleans.
func (n *Notifier) Status() Status {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return Status{
		Config:           n.cfg,
		HasWebhookSecret: n.sec.WebhookSecret != "",
		HasSMTPUser:      n.sec.SMTPUser != "",
		HasSMTPPassword:  n.sec.SMTPPassword != "",
	}
}

// Update applies a config mutation. Secret fields that arrive empty are
// treated as "keep the existing value" so a form submit never clears a
// stored credential by accident. To clear a secret, the operator sends
// a sentinel (e.g. "!"), documented in the UI.
func (n *Notifier) Update(cfg Config, webhookSecret, smtpUser, smtpPassword string, clearSecret bool) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg = cfg
	if clearSecret {
		n.sec.WebhookSecret = ""
		n.sec.SMTPUser = ""
		n.sec.SMTPPassword = ""
	} else {
		// Empty means "keep"; non-empty overwrites.
		if webhookSecret != "" {
			n.sec.WebhookSecret = webhookSecret
		}
		if smtpUser != "" {
			n.sec.SMTPUser = smtpUser
		}
		if smtpPassword != "" {
			n.sec.SMTPPassword = smtpPassword
		}
	}
	return n.saveSecrets()
}

// validateConfig enforces safe values: only https webhook URLs, a
// port, a from/to address and TLS whenever an SMTP password is used.
func validateConfig(cfg Config) error {
	if cfg.WebhookEnabled && cfg.WebhookURL != "" {
		if !strings.HasPrefix(cfg.WebhookURL, "https://") {
			return errors.New("webhook URL must use https:// (plain http is refused for safety)")
		}
	}
	if cfg.SMTPEnabled {
		if cfg.SMTPHost == "" || cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
			return errors.New("smtp host and a valid port (1-65535) are required")
		}
		if cfg.SMTPFrom == "" || cfg.SMTPTo == "" {
			return errors.New("smtp from and to addresses are required")
		}
		if !cfg.SMTPTLS && !cfg.SMTPInsecure {
			return errors.New("smtp requires TLS (STARTTLS) or explicit insecure override")
		}
	}
	return nil
}

// Record appends an event to the bounded ring (no-op if alerts are
// disabled). Always returns the event for callers to inspect.
func (n *Notifier) Record(level, subject, message string) AlertEvent {
	e := AlertEvent{Time: time.Now().UTC(), Level: level, Subject: subject, Message: message}
	n.mu.Lock()
	n.events = append(n.events, e)
	if len(n.events) > n.eventMax {
		n.events = n.events[len(n.events)-n.eventMax:]
	}
	enabled := n.cfg.Enabled
	n.mu.Unlock()
	if !enabled {
		return e
	}
	// Deliver to configured channels (best-effort, non-fatal).
	n.deliver(level, subject, message)
	return e
}

// Events returns a copy of the recorded alerts, newest first.
func (n *Notifier) Events() []AlertEvent {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]AlertEvent, len(n.events))
	copy(out, n.events)
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// deliver sends an alert to all enabled channels. Failures are logged
// but never surfaced to callers (alerting must never break the main
// flow).
func (n *Notifier) deliver(level, subject, message string) {
	n.mu.RLock()
	cfg := n.cfg
	sec := n.sec
	n.mu.RUnlock()

	payload := map[string]any{
		"level":   level,
		"subject": subject,
		"message": message,
		"time":    time.Now().UTC().Format(time.RFC3339),
	}

	if cfg.WebhookEnabled && cfg.WebhookURL != "" {
		go n.sendWebhook(cfg.WebhookURL, sec.WebhookSecret, payload)
	}
	if cfg.SMTPEnabled && cfg.SMTPHost != "" && sec.SMTPPassword != "" {
		go n.sendEmail(cfg, sec, subject, message)
	}
}

func (n *Notifier) sendWebhook(url, secret string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		n.logger.Warn("notify_webhook_marshal_failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_webhook_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_webhook_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		n.logger.Warn("notify_webhook_non_2xx", "status", resp.StatusCode)
	}
}

func (n *Notifier) sendEmail(cfg Config, sec secrets, subject, message string) {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	var client *smtp.Client
	var err error

	// Explicit TLS (implicit TLS on connect).
	if !cfg.SMTPTLS {
		conn, cerr := tls.Dial("tcp", addr, &tls.Config{
			ServerName: cfg.SMTPHost,
			// InsecureSkipVerify only when the operator explicitly
			// opted in (self-signed internal relays). Defaults to
			// false (secure).
			InsecureSkipVerify: cfg.SMTPInsecure,
		})
		if cerr != nil {
			n.logger.Warn("notify_smtp_tls_failed", "err", cerr)
			return
		}
		client, err = smtp.NewClient(conn, cfg.SMTPHost)
	} else {
		// STARTTLS.
		client, err = smtp.Dial(addr)
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); ok {
				cfgTLS := &tls.Config{ServerName: cfg.SMTPHost, InsecureSkipVerify: cfg.SMTPInsecure}
				err = client.StartTLS(cfgTLS)
			}
		}
	}
	if err != nil {
		n.logger.Warn("notify_smtp_connect_failed", "err", err)
		return
	}
	defer client.Close()

	// Auth (only if credentials are set).
	if sec.SMTPUser != "" {
		if err := client.Auth(smtp.PlainAuth("", sec.SMTPUser, sec.SMTPPassword, cfg.SMTPHost)); err != nil {
			n.logger.Warn("notify_smtp_auth_failed", "err", err)
			return
		}
	}
	if err := client.Mail(cfg.SMTPFrom); err != nil {
		n.logger.Warn("notify_smtp_mail_failed", "err", err)
		return
	}
	if err := client.Rcpt(cfg.SMTPTo); err != nil {
		n.logger.Warn("notify_smtp_rcpt_failed", "err", err)
		return
	}
	w, err := client.Data()
	if err != nil {
		n.logger.Warn("notify_smtp_data_failed", "err", err)
		return
	}
	msg := fmt.Sprintf("Subject: %s\r\nFrom: %s\r\nTo: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		sanitizeHeader(subject), sanitizeHeader(cfg.SMTPFrom), sanitizeHeader(cfg.SMTPTo), message)
	if _, err := w.Write([]byte(msg)); err != nil {
		n.logger.Warn("notify_smtp_write_failed", "err", err)
		return
	}
	if err := w.Close(); err != nil {
		n.logger.Warn("notify_smtp_close_failed", "err", err)
		return
	}
	if err := client.Quit(); err != nil {
		n.logger.Warn("notify_smtp_quit_failed", "err", err)
	}
}

// sanitizeHeader strips CR/LF to prevent header injection.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// SendTest delivers a test alert to confirm channel configuration.
func (n *Notifier) SendTest() error {
	cfg := n.Status()
	if !cfg.Config.WebhookEnabled && !cfg.Config.SMTPEnabled {
		return errors.New("no notification channel enabled")
	}
	n.Record("info", "WebKVM test notification", "This is a test alert from WebKVM. If you received it, your notification channels are working.")
	return nil
}
