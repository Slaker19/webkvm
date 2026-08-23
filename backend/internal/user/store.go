package user

import (
	"crypto/rand"
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

	"webkvm/internal/models"

	"golang.org/x/crypto/bcrypt"
)

type Store struct {
	mu       sync.RWMutex
	filePath string
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
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         role,
		Email:        req.Email,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Active:       true,
		Quota:        req.Quota,
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

	if req.Password != nil {
		if err := validatePasswordStrength(*req.Password); err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		u.PasswordHash = string(hash)
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

	if err := s.save(); err != nil {
		return nil, err
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
	return s.save()
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
			// Legacy record (password field was json:"-", never
			// persisted). The seed admin gets the documented default
			// password re-applied (hashed) so the install isn't
			// permanently locked out. Other users without hashes
			// are dropped — they couldn't log in anyway.
			if u.Username == "admin" {
				hash, herr := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
				if herr != nil {
					return herr
				}
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

func validatePasswordStrength(pw string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(pw) > 128 {
		return errors.New("password must be at most 128 characters")
	}
	return nil
}
