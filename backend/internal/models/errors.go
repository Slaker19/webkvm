package models

import "errors"

// ErrUserNotFound is returned when an account does not exist. It lives
// here (rather than in user or auth) so both sides can reference it:
// user wraps it, auth.SessionEnforcer matches it to answer 401 instead
// of 500 for tokens whose account was deleted.
var ErrUserNotFound = errors.New("user not found")
