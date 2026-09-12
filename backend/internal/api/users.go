package api

import (
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, h.userStore.List())
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req models.CreateUserRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := h.userStore.Create(req)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "user.create", Resource: u.Username,
	})
	jsonResp(w, http.StatusCreated, u.ToResponse())
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	var req models.UpdateUserRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := h.userStore.Update(username, req)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "user.update", Resource: username,
	})
	jsonResp(w, http.StatusOK, u.ToResponse())
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	caller, _, _ := audit.FromRequest(r)
	if err := h.userStore.Delete(username, caller); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "user.delete", Resource: username,
	})
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// RevokeUserSessions forces every token currently held by `username` to be
// rejected on its next request (see models.User.SessionEpoch and
// auth.SessionEnforcer) — a real "log out everywhere" for another user,
// as opposed to auth.Manager.Revoke, which can only invalidate a token
// the caller already holds. Self-targeting is refused by the store (an
// epoch counter can't distinguish "this browser tab" from "my other
// sessions" — see user.Store.BumpSessionEpoch).
func (h *Handler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	caller, _, _ := audit.FromRequest(r)
	epoch, err := h.userStore.BumpSessionEpoch(username, caller)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "user.revoke_sessions", Resource: username,
	})
	jsonResp(w, http.StatusOK, map[string]interface{}{"status": "revoked", "session_epoch": epoch})
}

// ChangeMyPassword lets the authenticated user change their own password.
func (h *Handler) ChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	caller, _, _ := audit.FromRequest(r)
	if caller == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req models.ChangeMyPasswordRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		jsonErr(w, http.StatusBadRequest, "old_password and new_password are required")
		return
	}
	if err := h.userStore.ChangePassword(caller, req.OldPassword, req.NewPassword); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, role, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: user, Role: role, IP: ip, Action: "user.change_password", Resource: caller,
	})
	jsonResp(w, http.StatusOK, map[string]string{"status": "password changed"})
}
