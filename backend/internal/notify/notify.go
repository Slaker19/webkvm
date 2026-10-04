// Package notify provides proactive alerting: it delivers event
// notifications to configured channels (generic HTTPS webhook, Discord,
// Telegram, Slack/Mattermost and/or SMTP email) and records an audit ring.
//
// Security model
// -------------
// Secrets (the webhook bearer token, Telegram bot token and SMTP password)
// are stored separately from config.json in {dataDir}/notify-secrets.json
// with mode 0600. The regular config backup captures config.json and
// app state, NOT this file, so a leaked backup never contains notification
// credentials.
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
	"html"
	"log/slog"
	"mime"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config is the operator-facing notification configuration.
type Config struct {
	Enabled bool `json:"enabled"`

	// Webhook channel (Generic HTTPS).
	WebhookEnabled bool   `json:"webhook_enabled"`
	WebhookURL     string `json:"webhook_url,omitempty"`

	// Discord channel (Webhook).
	DiscordEnabled    bool   `json:"discord_enabled"`
	DiscordWebhookURL string `json:"discord_webhook_url,omitempty"`

	// Telegram channel (Bot API).
	TelegramEnabled bool   `json:"telegram_enabled"`
	TelegramChatID  string `json:"telegram_chat_id,omitempty"`

	// Slack / Mattermost channel (Incoming Webhook).
	SlackEnabled    bool   `json:"slack_enabled"`
	SlackWebhookURL string `json:"slack_webhook_url,omitempty"`

	// Ntfy channel.
	NtfyEnabled   bool   `json:"ntfy_enabled"`
	NtfyServerURL string `json:"ntfy_server_url,omitempty"`
	NtfyTopic     string `json:"ntfy_topic,omitempty"`

	// Gotify channel.
	GotifyEnabled   bool   `json:"gotify_enabled"`
	GotifyServerURL string `json:"gotify_server_url,omitempty"`

	// SMTP channel.
	SMTPEnabled  bool   `json:"smtp_enabled"`
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPFrom     string `json:"smtp_from,omitempty"`
	SMTPTo       string `json:"smtp_to,omitempty"`
	SMTPTLS      bool   `json:"smtp_tls"` // STARTTLS or TLS on connect
	SMTPInsecure bool   `json:"smtp_insecure"`

	// Alert thresholds.
	DiskFreePercent  int `json:"disk_free_percent"` // warn below this %
	CheckIntervalSec int `json:"check_interval_sec"`
}

// secrets holds the credential material, persisted separately with mode 0600.
type secrets struct {
	WebhookSecret    string `json:"webhook_secret,omitempty"`
	TelegramBotToken string `json:"telegram_bot_token,omitempty"`
	SMTPUser         string `json:"smtp_user,omitempty"`
	SMTPPassword     string `json:"smtp_password,omitempty"`
	NtfyToken        string `json:"ntfy_token,omitempty"`
	GotifyToken      string `json:"gotify_token,omitempty"`
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
	mu          sync.RWMutex
	cfg         Config
	sec         secrets
	secretsPath string

	events   []AlertEvent
	eventMax int

	logger     *slog.Logger
	httpClient *http.Client
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
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
		},
	}
	if err := n.loadSecrets(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Notifier) client() *http.Client {
	if n.httpClient != nil {
		return n.httpClient
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
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

// saveSecrets persists the secrets file atomically with 0600 perms.
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
// only booleans indicating whether each secret is present.
type Status struct {
	Config           Config `json:"config"`
	HasWebhookSecret bool   `json:"has_webhook_secret"`
	HasTelegramToken bool   `json:"has_telegram_token"`
	HasSMTPUser      bool   `json:"has_smtp_user"`
	HasSMTPPassword  bool   `json:"has_smtp_password"`
	HasNtfyToken     bool   `json:"has_ntfy_token"`
	HasGotifyToken   bool   `json:"has_gotify_token"`
}

// Status returns the safe view (no secrets) plus booleans.
func (n *Notifier) Status() Status {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return Status{
		Config:           n.cfg,
		HasWebhookSecret: n.sec.WebhookSecret != "",
		HasTelegramToken: n.sec.TelegramBotToken != "",
		HasSMTPUser:      n.sec.SMTPUser != "",
		HasSMTPPassword:  n.sec.SMTPPassword != "",
		HasNtfyToken:     n.sec.NtfyToken != "",
		HasGotifyToken:   n.sec.GotifyToken != "",
	}
}

// Update applies a config mutation. Secret fields that arrive empty are
// treated as "keep the existing value" so a form submit never clears a
// stored credential by accident.
func (n *Notifier) Update(cfg Config, webhookSecret, telegramBotToken, smtpUser, smtpPassword, ntfyToken, gotifyToken string, clearSecret bool) error {
	if err := validateConfig(cfg, telegramBotToken, n.sec.TelegramBotToken, gotifyToken, n.sec.GotifyToken); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg = cfg
	if clearSecret {
		n.sec.WebhookSecret = ""
		n.sec.TelegramBotToken = ""
		n.sec.SMTPUser = ""
		n.sec.SMTPPassword = ""
		n.sec.NtfyToken = ""
		n.sec.GotifyToken = ""
	} else {
		// Empty means "keep"; non-empty overwrites.
		if webhookSecret != "" {
			n.sec.WebhookSecret = webhookSecret
		}
		if telegramBotToken != "" {
			n.sec.TelegramBotToken = telegramBotToken
		}
		if smtpUser != "" {
			n.sec.SMTPUser = smtpUser
		}
		if smtpPassword != "" {
			n.sec.SMTPPassword = smtpPassword
		}
		if ntfyToken != "" {
			n.sec.NtfyToken = ntfyToken
		}
		if gotifyToken != "" {
			n.sec.GotifyToken = gotifyToken
		}
	}
	return n.saveSecrets()
}

// validateConfig enforces safe values: only https webhook URLs, valid ports,
// and TLS requirements for SMTP.
func validateConfig(cfg Config, newTelegramToken, existingTelegramToken, newGotifyToken, existingGotifyToken string) error {
	if cfg.WebhookEnabled && cfg.WebhookURL != "" {
		if !strings.HasPrefix(cfg.WebhookURL, "https://") {
			return errors.New("webhook URL must use https:// (plain http is refused for safety)")
		}
	}
	if cfg.DiscordEnabled && cfg.DiscordWebhookURL != "" {
		if !strings.HasPrefix(cfg.DiscordWebhookURL, "https://") {
			return errors.New("discord webhook URL must use https://")
		}
	}
	if cfg.SlackEnabled && cfg.SlackWebhookURL != "" {
		if !strings.HasPrefix(cfg.SlackWebhookURL, "https://") {
			return errors.New("slack webhook URL must use https://")
		}
	}
	if cfg.NtfyEnabled {
		if cfg.NtfyTopic == "" {
			return errors.New("ntfy topic is required when Ntfy is enabled")
		}
		if cfg.NtfyServerURL != "" && !strings.HasPrefix(cfg.NtfyServerURL, "http://") && !strings.HasPrefix(cfg.NtfyServerURL, "https://") {
			return errors.New("ntfy server URL must begin with http:// or https://")
		}
	}
	if cfg.GotifyEnabled {
		if cfg.GotifyServerURL == "" {
			return errors.New("gotify server URL is required when Gotify is enabled")
		}
		if !strings.HasPrefix(cfg.GotifyServerURL, "http://") && !strings.HasPrefix(cfg.GotifyServerURL, "https://") {
			return errors.New("gotify server URL must begin with http:// or https://")
		}
		if newGotifyToken == "" && existingGotifyToken == "" {
			return errors.New("gotify application token is required when Gotify is enabled")
		}
	}
	if cfg.TelegramEnabled {
		if cfg.TelegramChatID == "" {
			return errors.New("telegram chat ID is required when Telegram is enabled")
		}
		if newTelegramToken == "" && existingTelegramToken == "" {
			return errors.New("telegram bot token is required when Telegram is enabled")
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// deliver sends an alert to all enabled channels. Failures are logged
// but never surfaced to callers.
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
	if cfg.DiscordEnabled && cfg.DiscordWebhookURL != "" {
		go n.sendDiscord(cfg.DiscordWebhookURL, level, subject, message)
	}
	if cfg.TelegramEnabled && cfg.TelegramChatID != "" && sec.TelegramBotToken != "" {
		go n.sendTelegram(sec.TelegramBotToken, cfg.TelegramChatID, level, subject, message)
	}
	if cfg.SlackEnabled && cfg.SlackWebhookURL != "" {
		go n.sendSlack(cfg.SlackWebhookURL, level, subject, message)
	}
	if cfg.NtfyEnabled && cfg.NtfyTopic != "" {
		serverURL := cfg.NtfyServerURL
		if serverURL == "" {
			serverURL = "https://ntfy.sh"
		}
		go n.sendNtfy(serverURL, cfg.NtfyTopic, sec.NtfyToken, level, subject, message)
	}
	if cfg.GotifyEnabled && cfg.GotifyServerURL != "" && sec.GotifyToken != "" {
		go n.sendGotify(cfg.GotifyServerURL, sec.GotifyToken, level, subject, message)
	}
	if cfg.SMTPEnabled && cfg.SMTPHost != "" && sec.SMTPPassword != "" {
		go n.sendEmail(cfg, sec, subject, message)
	}
}

func (n *Notifier) sendWebhook(targetURL, secret string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		n.logger.Warn("notify_webhook_marshal_failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_webhook_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := n.client()
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

func (n *Notifier) sendDiscord(webhookURL, level, subject, message string) {
	var color int
	switch level {
	case "warning":
		color = 0xf59e0b // Amber
	case "critical", "error":
		color = 0xef4444 // Red
	default:
		color = 0x22c55e // Green
	}

	payload := map[string]any{
		"username": "WebKVM",
		"embeds": []map[string]any{
			{
				"title":       fmt.Sprintf("[%s] %s", strings.ToUpper(level), subject),
				"description": message,
				"color":       color,
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
				"footer": map[string]any{
					"text": "WebKVM Virtualization Management",
				},
			},
		},
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_discord_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.client()
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_discord_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
}

func (n *Notifier) sendTelegram(botToken, chatID, level, subject, message string) {
	var icon string
	switch level {
	case "warning":
		icon = "[WARN]"
	case "critical", "error":
		icon = "[ALERT]"
	default:
		icon = "[INFO]"
	}

	text := fmt.Sprintf("%s <b>[WebKVM %s]</b>\n<b>%s</b>\n\n%s",
		icon, strings.ToUpper(level), html.EscapeString(subject), html.EscapeString(message))

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", url.PathEscape(botToken))
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_telegram_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.client()
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_telegram_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
}

func (n *Notifier) sendSlack(webhookURL, level, subject, message string) {
	var color string
	switch level {
	case "warning":
		color = "#f59e0b"
	case "critical", "error":
		color = "#ef4444"
	default:
		color = "#22c55e"
	}

	payload := map[string]any{
		"text": fmt.Sprintf("*[WebKVM %s]* %s", strings.ToUpper(level), subject),
		"attachments": []map[string]any{
			{
				"color": color,
				"title": subject,
				"text":  message,
				"ts":    time.Now().UTC().Unix(),
			},
		},
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_slack_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.client()
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_slack_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
}

func (n *Notifier) sendNtfy(serverURL, topic, token, level, subject, message string) {
	targetURL := strings.TrimRight(serverURL, "/") + "/" + url.PathEscape(topic)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(message))
	if err != nil {
		n.logger.Warn("notify_ntfy_req_failed", "err", err)
		return
	}

	priority := "3"
	tag := "information_source"
	switch level {
	case "critical", "error":
		priority = "5"
		tag = "rotating_light,fire"
	case "warning":
		priority = "4"
		tag = "warning"
	}

	req.Header.Set("Title", fmt.Sprintf("[%s] %s", strings.ToUpper(level), subject))
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tag)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := n.client()
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_ntfy_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		n.logger.Warn("notify_ntfy_non_2xx", "status", resp.StatusCode)
	}
}

func (n *Notifier) sendGotify(serverURL, token, level, subject, message string) {
	targetURL := strings.TrimRight(serverURL, "/") + "/message"
	priority := 5
	switch level {
	case "critical", "error":
		priority = 8
	case "warning":
		priority = 6
	}

	payload := map[string]any{
		"title":    fmt.Sprintf("[%s] %s", strings.ToUpper(level), subject),
		"message":  message,
		"priority": priority,
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		n.logger.Warn("notify_gotify_req_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Gotify-Key", token)
	}

	client := n.client()
	resp, err := client.Do(req)
	if err != nil {
		n.logger.Warn("notify_gotify_delivery_failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		n.logger.Warn("notify_gotify_non_2xx", "status", resp.StatusCode)
	}
}

func (n *Notifier) sendEmail(cfg Config, sec secrets, subject, message string) {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)
	var client *smtp.Client
	var err error

	if !cfg.SMTPTLS {
		conn, cerr := tls.Dial("tcp", addr, &tls.Config{
			ServerName:         cfg.SMTPHost,
			InsecureSkipVerify: cfg.SMTPInsecure,
		})
		if cerr != nil {
			n.logger.Warn("notify_smtp_tls_failed", "err", cerr)
			return
		}
		client, err = smtp.NewClient(conn, cfg.SMTPHost)
	} else {
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
	cleanSubject := mime.QEncoding.Encode("utf-8", sanitizeHeader(subject))
	cleanFrom := sanitizeHeader(cfg.SMTPFrom)
	cleanTo := sanitizeHeader(cfg.SMTPTo)
	msg := fmt.Sprintf("Subject: %s\r\nFrom: %s\r\nTo: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n",
		cleanSubject, cleanFrom, cleanTo, message)
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

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// SendTest delivers a test alert to confirm channel configuration.
func (n *Notifier) SendTest() error {
	cfg := n.Status()
	if !cfg.Config.WebhookEnabled && !cfg.Config.DiscordEnabled &&
		!cfg.Config.TelegramEnabled && !cfg.Config.SlackEnabled &&
		!cfg.Config.NtfyEnabled && !cfg.Config.GotifyEnabled &&
		!cfg.Config.SMTPEnabled {
		return errors.New("no notification channel enabled")
	}
	n.Record("info", "WebKVM test notification", "This is a test alert from WebKVM. If you received it, your notification channels are working.")
	return nil
}
