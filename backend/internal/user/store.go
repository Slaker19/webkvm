package user

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"webkvm/internal/models"

	"golang.org/x/crypto/bcrypt"
)

type Store struct {
	mu       sync.RWMutex
	filePath string
	dataDir  string
	users    map[string]*models.User
}

// NewStore loads (or seeds) the user store at {dataDir}/users.json.
// File permissions are 0600; bcrypt is the password hash format.
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, "users.json")
	s := &Store{
		filePath: path,
		dataDir:  dataDir,
		users:    make(map[string]*models.User),
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	// Seed the default admin only if the store is empty.
	if len(s.users) == 0 {
		adminPassword := os.Getenv("WEBKVM_ADMIN_PASSWORD")
		if adminPassword == "" {
			raw := make([]byte, 24)
			if _, err := rand.Read(raw); err != nil {
				return nil, fmt.Errorf("generate admin password: %w", err)
			}
			adminPassword = base64.RawURLEncoding.EncodeToString(raw)
			pwPath := filepath.Join(dataDir, "admin-password.initial")
			if err := os.WriteFile(pwPath, []byte(adminPassword), 0600); err != nil {
				return nil, fmt.Errorf("persist admin password: %w", err)
			}
			slog.Warn("admin_password_generated",
				"saved_to", pwPath,
				"msg", "the initial admin password was generated randomly and saved with restricted permissions; retrieve it from the indicated file and change it after first login.")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash default password: %w", err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		s.users["admin"] = &models.User{
			Username:           "admin",
			PasswordHash:       string(hash),
			Role:               "admin",
			CreatedAt:          now,
			Active:             true,
			MustChangePassword: true,
			LastLoginAt:        now,
		}
		if err := s.save(); err != nil {
			return nil, err
		}
	}

	return s, nil
}

func (s *Store) List() []models.UserResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]models.UserResponse, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u.ToResponse())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Username < out[j].Username
	})
	return out
}

func (s *Store) Get(username string) (*models.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	cp := *u
	return &cp, nil
}

// MustChangePassword returns the current value of the flag for the
// given user. It's a fast path used by the must_change_password
// middleware so it doesn't have to materialize a full User struct
// on every request.
func (s *Store) MustChangePassword(username string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return false, fmt.Errorf("user not found")
	}
	return u.MustChangePassword, nil
}

// SessionStatus is the per-request check auth.SessionEnforcer uses: it
// answers both "must this user change their password" and "what session
// epoch are they currently on" from the single map lookup MustChangePassword
// already did, so wiring in the epoch check adds no extra store hit.
func (s *Store) SessionStatus(username string) (mustChange bool, epoch int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return false, 0, fmt.Errorf("user not found")
	}
	return u.MustChangePassword, u.SessionEpoch, nil
}

// BumpSessionEpoch increments username's session epoch, invalidating every
// token issued before the call (they carry the old epoch and are rejected
// by auth.SessionEnforcer on their next request) without needing a
// per-token revocation list. Returns the new epoch.
//
// Self-targeting is refused, mirroring Delete's "cannot delete your own
// account" guard: a session epoch is a single per-account counter with no
// concept of "this browser tab" vs "my other sessions" — bumping your own
// epoch would immediately log out the very request that asked for it.
func (s *Store) BumpSessionEpoch(username, callerUsername string) (int, error) {
	if username == callerUsername {
		return 0, fmt.Errorf("cannot revoke your own sessions")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return 0, fmt.Errorf("user not found")
	}
	u.SessionEpoch++
	if err := s.save(); err != nil {
		u.SessionEpoch--
		return 0, err
	}
	return u.SessionEpoch, nil
}

// Create hashes the password with bcrypt, validates the role, and
// persists the new user.
func (s *Store) Create(req models.CreateUserRequest) (*models.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(req.Username) == "" {
		return nil, fmt.Errorf("username is required")
	}
	if req.Password == "" {
		return nil, fmt.Errorf("password is required")
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		return nil, err
	}
	if _, ok := s.users[req.Username]; ok {
		return nil, fmt.Errorf("user already exists")
	}
	role := req.Role
	if role == "" {
		role = models.RoleOperator
	}
	if !models.IsValidRole(role) {
		return nil, fmt.Errorf("invalid role %q (must be admin/operator/viewer)", role)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	u := &models.User{
		Username:           req.Username,
		PasswordHash:       string(hash),
		Role:               role,
		Email:              req.Email,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
		Active:             true,
		Quota:              req.Quota,
		AllowedPools:       req.AllowedPools,
		AllowedNetworks:    req.AllowedNetworks,
		AllowedTags:        req.AllowedTags,
		MustChangePassword: req.MustChangePassword,
	}
	s.users[req.Username] = u
	if err := s.save(); err != nil {
		return nil, err
	}

	cp := *u
	return &cp, nil
}

// Update applies partial changes. The caller is responsible for RBAC
// (RequireRole). The last-admin protection is enforced here for safety.
func (s *Store) Update(username string, req models.UpdateUserRequest) (*models.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("user not found")
	}

	var adminPasswordChanged bool
	if req.Password != nil {
		if err := validatePasswordStrength(*req.Password); err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		u.PasswordHash = string(hash)
		// The initial-secret file cleanup is armed but only executed
		// after save() below succeeds (V12-SEC-03: strictly
		// transactional — a failed write must not destroy the only
		// record of the admin's initial password).
		if username == "admin" {
			adminPasswordChanged = true
		}
		u.MustChangePassword = false
	}
	if req.Role != nil {
		if !models.IsValidRole(*req.Role) {
			return nil, fmt.Errorf("invalid role %q (must be admin/operator/viewer)", *req.Role)
		}
		// Prevent removing the last admin.
		if username == "admin" && *req.Role != models.RoleAdmin {
			if err := s.assertAtLeastOneAdminLocked(username, models.RoleAdmin); err != nil {
				return nil, err
			}
		}
		u.Role = *req.Role
	}
	if req.Email != nil {
		u.Email = *req.Email
	}
	if req.Active != nil {
		if !*req.Active && username == "admin" {
			return nil, fmt.Errorf("cannot deactivate the admin user")
		}
		u.Active = *req.Active
	}
	if req.Quota != nil {
		// Validating negative values would be confusing; clamp them.
		q := *req.Quota
		if q.MaxVMs < 0 {
			q.MaxVMs = 0
		}
		if q.MaxVCPUs < 0 {
			q.MaxVCPUs = 0
		}
		if q.MaxRAMMB < 0 {
			q.MaxRAMMB = 0
		}
		if q.MaxDiskGB < 0 {
			q.MaxDiskGB = 0
		}
		u.Quota = q
	}
	if req.AllowedPools != nil {
		// Non-nil replaces the allowlist; an empty slice clears it so
		// the user may use every pool again.
		u.AllowedPools = *req.AllowedPools
	}
	if req.AllowedNetworks != nil {
		u.AllowedNetworks = *req.AllowedNetworks
	}
	if req.AllowedTags != nil {
		u.AllowedTags = *req.AllowedTags
	}

	if err := s.save(); err != nil {
		return nil, err
	}
	if adminPasswordChanged {
		s.clearInitialAdminPasswordFilesLocked(username)
	}

	cp := *u
	return &cp, nil
}

// ChangePassword verifies the old password and sets a new one.
func (s *Store) ChangePassword(username, oldPassword, newPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.users[username]
	if !ok {
		return fmt.Errorf("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPassword)); err != nil {
		return fmt.Errorf("current password is incorrect")
	}
	if err := validatePasswordStrength(newPassword); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	u.MustChangePassword = false
	if err := s.save(); err != nil {
		// V12-SEC-03: strictly transactional — only after a successful
		// persist may the initial-secret files be destroyed.
		return err
	}
	s.clearInitialAdminPasswordFilesLocked(username)
	return nil
}

// MarkLogin records the last login timestamp (best-effort, errors
// logged but not propagated so login never fails on persistence issues).
func (s *Store) MarkLogin(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return
	}
	u.LastLoginAt = time.Now().UTC().Format(time.RFC3339)
	_ = s.save()
}

// Delete prevents self-delete and removal of the last admin.
func (s *Store) Delete(username, callerUsername string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if username == "admin" {
		return fmt.Errorf("cannot delete the admin user")
	}
	if username == callerUsername {
		return fmt.Errorf("cannot delete your own account")
	}
	u, ok := s.users[username]
	if !ok {
		return fmt.Errorf("user not found")
	}
	// If the target is an admin, ensure at least one admin remains.
	if u.Role == models.RoleAdmin {
		if err := s.assertAtLeastOneAdminLocked(username, models.RoleAdmin); err != nil {
			return err
		}
	}
	delete(s.users, username)
	return s.save()
}

// Validate checks the credentials and returns the user on success.
// Returns (nil, false) on bad credentials (constant-time bcrypt compare
// already prevents timing attacks).
func (s *Store) Validate(username, password string) (*models.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, ok := s.users[username]
	if !ok {
		// Run a dummy bcrypt compare to keep response time constant
		// when the user does not exist.
		_ = bcrypt.CompareHashAndPassword(
			[]byte("$2a$10$abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTU"),
			[]byte(password),
		)
		return nil, false
	}
	if !u.Active {
		return nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, false
	}
	cp := *u
	return &cp, true
}

// load reads users.json. Returns an error if the file exists but is
// malformed. A missing file is treated as an empty store. Legacy files
// (without password_hash) are migrated in-memory: admin is re-seeded
// with the default hash and MustChangePassword=true.
func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read users.json: %w", err)
	}

	var users []models.User
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("parse users.json: %w", err)
	}

	needsMigration := false
	for _, u := range users {
		if u.PasswordHash == "" {
			// Legacy/corrupted record (password field was json:"-",
			// never persisted, or the file was hand-edited/partially
			// restored). The seed admin is re-seeded with a freshly
			// generated random password (like a first-run install)
			// rather than a hardcoded, guessable one — a literal
			// "admin" password here would let anyone regain a
			// trivially-guessable admin credential just by producing
			// a users.json missing that field. Other users without
			// hashes are dropped — they couldn't log in anyway.
			if u.Username == "admin" {
				raw := make([]byte, 24)
				if _, rerr := rand.Read(raw); rerr != nil {
					return rerr
				}
				adminPassword := base64.RawURLEncoding.EncodeToString(raw)
				hash, herr := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
				if herr != nil {
					return herr
				}
				pwPath := filepath.Join(filepath.Dir(s.filePath), "admin-password.reset")
				if werr := os.WriteFile(pwPath, []byte(adminPassword), 0600); werr != nil {
					return werr
				}
				slog.Warn("admin_password_reset_missing_hash",
					"saved_to", pwPath,
					"msg", "users.json had an admin record with no password hash (corrupted or hand-edited file); a new random password was generated and saved with restricted permissions — retrieve it from the indicated file and change it after login.")
				u.PasswordHash = string(hash)
				u.MustChangePassword = true
				u.Active = true
				needsMigration = true
			} else {
				continue
			}
		}
		s.users[u.Username] = &u
	}

	if needsMigration {
		if err := s.save(); err != nil {
			return err
		}
	}
	return nil
}

// save writes the store to disk atomically with 0600 permissions.
func (s *Store) save() error {
	users := make([]models.User, 0, len(s.users))
	for _, u := range s.users {
		users = append(users, *u)
	}
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.filePath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// assertAtLeastOneAdminLocked verifies there's at least one admin
// user besides `exclude`. Caller must hold s.mu.
func (s *Store) assertAtLeastOneAdminLocked(exclude string, role string) error {
	if role != models.RoleAdmin {
		return nil
	}
	count := 0
	for name, u := range s.users {
		if name == exclude {
			continue
		}
		if u.Role == models.RoleAdmin && u.Active {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("cannot perform this action: at least one active admin must remain")
	}
	return nil
}

// validatePasswordStrength is the single password-quality gate. Every
// password a human sets — user self-service change, admin create, admin
// reset — flows through here (the guest's OS password is a separate
// concern in the VM console). Policy:
//   - minimum 8 characters (grandfathered: existing hashes are never
//     re-validated until their next change) — matches the frontend's
//     own "Password (min 8)" hint, which used to be flatly wrong: this
//     gate silently required 12, so a password the UI itself suggested
//     was acceptable would still get rejected server-side.
//   - if shorter than 16, at least 3 of 4 character classes must be
//     present (lower, upper, digit, symbol),
//   - must not be an exact (case-insensitive) member of the embedded
//     common-password denylist, which absorbs trivial transformations
//     like "Password1" or "P@ssw0rd2" that evade naive rules.
//
// clearInitialAdminPasswordFiles removes admin-password.initial and
// admin-password.reset AFTER the store persisted a password change that
// cleared the admin's MustChangePassword flag. Strictly transactional:
// called only when save() returned nil, so a failed store write never
// destroys the only record of the admin's initial password. The caller
// must hold s.mu (reads the in-memory flag captured before the change).
func (s *Store) clearInitialAdminPasswordFilesLocked(username string) {
	if username != "admin" || s.dataDir == "" {
		return
	}
	for _, fn := range []string{"admin-password.initial", "admin-password.reset"} {
		p := filepath.Join(s.dataDir, fn)
		if err := os.Remove(p); err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("admin_password_file_remove_failed", "path", p, "err", err)
			}
			// Nonexistent is the happy path (already cleaned or never written).
			continue
		}
		slog.Info("admin_password_file_removed", "path", p)
	}
}

func validatePasswordStrength(pw string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters (16+ releases the complexity requirement)")
	}
	if len(pw) > 128 {
		return errors.New("password must be at most 128 characters")
	}
	if err := checkDenylist(pw); err != nil {
		return err
	}
	if len(pw) < 16 {
		classes := 0
		classes += boolToInt(strings.IndexFunc(pw, unicode.IsLower) >= 0)
		classes += boolToInt(strings.IndexFunc(pw, unicode.IsUpper) >= 0)
		classes += boolToInt(strings.IndexFunc(pw, unicode.IsDigit) >= 0)
		classes += boolToInt(strings.IndexFunc(pw, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) >= 0)
		if classes < 3 {
			return errors.New("passwords under 16 characters need at least 3 of: lowercase, uppercase, digit, symbol")
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// commonPasswordsRaw is the compiled-in denylist (see
// common_passwords.txt next to this file). Matched lowercased, exact string.
//
//go:embed common_passwords.txt
var commonPasswordsFS embed.FS

var commonPasswordsRaw = mustReadDenylist()

func mustReadDenylist() string {
	b, err := commonPasswordsFS.ReadFile("common_passwords.txt")
	if err != nil {
		panic(fmt.Sprintf("embedded denylist missing: %v", err))
	}
	return string(b)
}

var commonPasswords = buildDenylist()

func buildDenylist() map[string]struct{} {
	set := make(map[string]struct{}, 2048)
	for _, line := range strings.Split(commonPasswordsRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ToLower(line)] = struct{}{}
	}
	return set
}

// checkDenylist rejects passwords appearing verbatim (case-insensitive)
// in the denylist, as well as the classic "word + digit/symbol" tail
// decorations of an entry (e.g. "Password1", "freedom123").
func checkDenylist(pw string) error {
	low := strings.ToLower(pw)
	if _, bad := commonPasswords[low]; bad {
		return errors.New("password appears in the common-password list; pick something unique")
	}
	trimmed := strings.TrimRight(low, "0123456789!@#$%^&*()")
	if trimmed != "" && trimmed != low {
		if _, bad := commonPasswords[trimmed]; bad {
			return errors.New("password is a decorated version of a common password; pick something unique")
		}
	}
	return nil
}
