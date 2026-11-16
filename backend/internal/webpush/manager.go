// Package webpush stores per-user browser push subscriptions and sends
// Web Push notifications with a VAPID keypair generated on first start.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wplib "github.com/SherClockHolmes/webpush-go"
)

const (
	maxSubscriptionsPerUser = 20
	maxBodyBytes            = 180
)

type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type Subscription struct {
	Endpoint  string    `json:"endpoint"`
	Keys      Keys      `json:"keys"`
	Owner     string    `json:"owner"`
	Locale    string    `json:"locale,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type state struct {
	VAPIDPublic   string         `json:"vapid_public"`
	VAPIDPrivate  string         `json:"vapid_private"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type Manager struct {
	mu      sync.Mutex
	path    string
	subject string
	public  string
	private string
	subs    []Subscription
}

// New loads (or creates) the VAPID keypair and subscription store at
// {dataDir}/webpush.json. The private key never leaves this file.
func New(dataDir string) (*Manager, error) {
	subject := strings.TrimSpace(os.Getenv("WEBKVM_VAPID_SUBJECT"))
	if subject == "" {
		subject = "mailto:webkvm@localhost"
	}
	m := &Manager{
		path:    filepath.Join(dataDir, "webpush.json"),
		subject: subject,
	}
	data, err := os.ReadFile(m.path)
	if err == nil {
		var st state
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("parse %s: %w", m.path, err)
		}
		m.public, m.private, m.subs = st.VAPIDPublic, st.VAPIDPrivate, st.Subscriptions
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if m.public == "" || m.private == "" {
		priv, pub, err := wplib.GenerateVAPIDKeys()
		if err != nil {
			return nil, fmt.Errorf("generate VAPID keys: %w", err)
		}
		m.private, m.public = priv, pub
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) PublicKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.public
}

func (m *Manager) Subscribe(owner string, sub Subscription) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("owner is required")
	}
	if !strings.HasPrefix(sub.Endpoint, "https://") {
		return errors.New("subscription endpoint must be https")
	}
	if sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("subscription keys are required")
	}
	sub.Owner = owner
	sub.CreatedAt = time.Now().UTC()
	if sub.Locale != "es" && sub.Locale != "ca" {
		sub.Locale = "en"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.subs[:0]
	count := 0
	for _, existing := range m.subs {
		if existing.Endpoint == sub.Endpoint {
			continue
		}
		if existing.Owner == owner {
			count++
		}
		out = append(out, existing)
	}
	if count >= maxSubscriptionsPerUser {
		return fmt.Errorf("too many push subscriptions (max %d)", maxSubscriptionsPerUser)
	}
	out = append(out, sub)
	m.subs = out
	return m.saveLocked()
}

func (m *Manager) Unsubscribe(owner, endpoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.subs[:0]
	for _, sub := range m.subs {
		if sub.Owner == owner && sub.Endpoint == endpoint {
			continue
		}
		out = append(out, sub)
	}
	m.subs = out
	return m.saveLocked()
}

// Notify sends to every subscription owned by owner. Expired endpoints are
// dropped from the store.
func (m *Manager) Notify(owner, title, body, url string) {
	m.mu.Lock()
	subs := make([]Subscription, 0)
	for _, sub := range m.subs {
		if sub.Owner == owner {
			subs = append(subs, sub)
		}
	}
	pub, priv := m.public, m.private
	m.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": url})
	expired := make([]string, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, sub := range subs {
		resp, err := wplib.SendNotificationWithContext(ctx, payload, &wplib.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     wplib.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
		}, &wplib.Options{
			Subscriber:      m.subject,
			TTL:             3600,
			VAPIDPublicKey:  pub,
			VAPIDPrivateKey: priv,
		})
		if err != nil {
			continue
		}
		if resp != nil {
			if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
				expired = append(expired, sub.Endpoint)
			}
			_ = resp.Body.Close()
		}
	}
	if len(expired) > 0 {
		m.mu.Lock()
		out := m.subs[:0]
		for _, sub := range m.subs {
			keep := true
			for _, endpoint := range expired {
				if sub.Endpoint == endpoint {
					keep = false
					break
				}
			}
			if keep {
				out = append(out, sub)
			}
		}
		m.subs = out
		_ = m.saveLocked()
		m.mu.Unlock()
	}
}

// NotifyJob is the hook used by the async job runner. It localizes the few
// push strings per subscription locale; job names are user data and are
// passed through unchanged.
func (m *Manager) NotifyJob(owner, name, status, detail string) {
	bodyPrefix := name
	if status == "error" && detail != "" {
		bodyPrefix = name + ": " + truncate(detail, maxBodyBytes)
	}
	m.mu.Lock()
	subs := make([]Subscription, 0)
	for _, sub := range m.subs {
		if sub.Owner == owner {
			subs = append(subs, sub)
		}
	}
	m.mu.Unlock()
	for _, sub := range subs {
		m.notifyOne(sub, localize(sub.Locale, status == "error"), bodyPrefix, "/#/status")
	}
}

func (m *Manager) notifyOne(sub Subscription, title, body, url string) {
	m.mu.Lock()
	pub, priv := m.public, m.private
	m.mu.Unlock()
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": url})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := wplib.SendNotificationWithContext(ctx, payload, &wplib.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     wplib.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
	}, &wplib.Options{
		Subscriber:      m.subject,
		TTL:             3600,
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
	})
	if resp != nil {
		_ = resp.Body.Close()
	}
	_ = err
}

func localize(locale string, failed bool) string {
	switch locale {
	case "es":
		if failed {
			return "Tarea de WebKVM fallida"
		}
		return "Tarea de WebKVM completada"
	case "ca":
		if failed {
			return "Tasca de WebKVM fallida"
		}
		return "Tasca de WebKVM completada"
	default:
		if failed {
			return "WebKVM job failed"
		}
		return "WebKVM job finished"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (m *Manager) saveLocked() error {
	st := state{VAPIDPublic: m.public, VAPIDPrivate: m.private, Subscriptions: m.subs}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// NotifyTest sends one notification to the owner's subscriptions. Used by
// the Account page button so the permission/subscription flow is testable
// without waiting for a job.
func (m *Manager) NotifyTest(owner, locale string) int {
	m.mu.Lock()
	subs := make([]Subscription, 0)
	for _, sub := range m.subs {
		if sub.Owner == owner {
			subs = append(subs, sub)
		}
	}
	m.mu.Unlock()
	title, body := testText(locale)
	for _, sub := range subs {
		m.notifyOne(sub, title, body, "/#/account")
	}
	return len(subs)
}

func testText(locale string) (string, string) {
	switch locale {
	case "es":
		return "Notificación de prueba de WebKVM", "Las notificaciones del navegador funcionan."
	case "ca":
		return "Notificació de prova de WebKVM", "Les notificacions del navegador funcionen."
	default:
		return "WebKVM test notification", "Browser notifications are working."
	}
}
