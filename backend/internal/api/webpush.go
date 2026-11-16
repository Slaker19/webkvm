package api

import (
	"net/http"

	"webkvm/internal/webpush"
)

var webPushManager *webpush.Manager

// SetWebPushManager wires the browser push manager (nil disables the
// feature, which is what tests and minimal deployments get).
func SetWebPushManager(m *webpush.Manager) { webPushManager = m }

func (h *Handler) GetWebPushKey(w http.ResponseWriter, r *http.Request) {
	if webPushManager == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "webpush_disabled", "browser push is not configured")
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"public_key": webPushManager.PublicKey()})
}

func (h *Handler) SubscribeWebPush(w http.ResponseWriter, r *http.Request) {
	if webPushManager == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "webpush_disabled", "browser push is not configured")
		return
	}
	owner := jobOwner(r)
	if owner == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Locale string `json:"locale"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	err := webPushManager.Subscribe(owner, webpush.Subscription{
		Endpoint: req.Endpoint,
		Keys:     webpush.Keys{P256dh: req.Keys.P256dh, Auth: req.Keys.Auth},
		Locale:   req.Locale,
	})
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusCreated, map[string]string{"status": "subscribed"})
}

func (h *Handler) UnsubscribeWebPush(w http.ResponseWriter, r *http.Request) {
	if webPushManager == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "webpush_disabled", "browser push is not configured")
		return
	}
	owner := jobOwner(r)
	if owner == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := webPushManager.Unsubscribe(owner, req.Endpoint); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "unsubscribed"})
}

func (h *Handler) TestWebPush(w http.ResponseWriter, r *http.Request) {
	if webPushManager == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "webpush_disabled", "browser push is not configured")
		return
	}
	owner := jobOwner(r)
	if owner == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Locale string `json:"locale"`
	}
	_ = decodeBody(r, &req)
	sent := webPushManager.NotifyTest(owner, req.Locale)
	jsonResp(w, http.StatusOK, map[string]any{"status": "sent", "subscriptions": sent})
}
