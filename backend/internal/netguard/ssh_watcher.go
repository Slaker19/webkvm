package netguard

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	// Regex patterns matching common SSH / PAM failure log messages
	sshFailedPasswordRe = regexp.MustCompile(`Failed (?:password|none) for (?:invalid user )?\S+ from (\S+)`)
	sshInvalidUserRe    = regexp.MustCompile(`Invalid user \S+ from (\S+)`)
	sshPamAuthFailureRe = regexp.MustCompile(`authentication failure;.*rhost=(\S+)`)
	sshDisconnectedRe   = regexp.MustCompile(`(?:Disconnected from|Connection closed by) authenticating user \S+ (\S+)`)
	sshPreauthClosedRe  = regexp.MustCompile(`Connection (?:closed|reset) by (\S+) port \d+ \[preauth\]`)
)

// ExtractSSHFailureIP extracts a client IPv4/IPv6 address from an SSH log line if it represents an auth failure.
func ExtractSSHFailureIP(line string) string {
	var candidate string

	if m := sshFailedPasswordRe.FindStringSubmatch(line); len(m) > 1 {
		candidate = m[1]
	} else if m := sshInvalidUserRe.FindStringSubmatch(line); len(m) > 1 {
		candidate = m[1]
	} else if m := sshPamAuthFailureRe.FindStringSubmatch(line); len(m) > 1 {
		candidate = m[1]
	} else if m := sshDisconnectedRe.FindStringSubmatch(line); len(m) > 1 {
		candidate = m[1]
	} else if m := sshPreauthClosedRe.FindStringSubmatch(line); len(m) > 1 {
		candidate = m[1]
	}

	if candidate == "" {
		return ""
	}

	clean := strings.TrimSpace(candidate)
	// Strip port or bracket syntax if present
	if host, _, err := net.SplitHostPort(clean); err == nil {
		clean = host
	}
	clean = strings.Trim(clean, "[]")

	if parsed := net.ParseIP(clean); parsed != nil {
		return parsed.String()
	}
	return ""
}

// StartSSHWatcher begins listening to SSH authentication logs in a background loop.
func (j *Jail) StartSSHWatcher(ctx context.Context) {
	j.logger.Info("ssh_jail_watcher_started")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		j.runSSHWatcher(ctx)

		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (j *Jail) runSSHWatcher(ctx context.Context) {
	// 1. Try systemd journalctl if present
	if _, err := exec.LookPath("journalctl"); err == nil {
		cmd := exec.CommandContext(ctx, "journalctl",
			"-u", "ssh",
			"-u", "sshd",
			"-u", "ssh.service",
			"-u", "sshd.service",
			"-f", "-n", "0", "-o", "cat",
		)
		stdout, err := cmd.StdoutPipe()
		if err == nil {
			if err := cmd.Start(); err == nil {
				j.logger.Info("ssh_jail_streaming_journalctl")
				j.scanStream(stdout)
				_ = cmd.Wait()
				return
			}
		}
	}

	// 2. Fallback to /var/log/auth.log or /var/log/secure if available
	authLogs := []string{"/var/log/auth.log", "/var/log/secure"}
	for _, logFile := range authLogs {
		if _, err := os.Stat(logFile); err == nil {
			if _, err := exec.LookPath("tail"); err == nil {
				cmd := exec.CommandContext(ctx, "tail", "-n", "0", "-F", logFile)
				stdout, err := cmd.StdoutPipe()
				if err == nil {
					if err := cmd.Start(); err == nil {
						j.logger.Info("ssh_jail_streaming_tail", "file", logFile)
						j.scanStream(stdout)
						_ = cmd.Wait()
						return
					}
				}
			}
		}
	}

	// If neither journalctl nor log file is available, wait and retry
	time.Sleep(10 * time.Second)
}

func (j *Jail) scanStream(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if ip := ExtractSSHFailureIP(line); ip != "" {
			j.RecordSSHFailure(ip, "SSH authentication failure")
		}
	}
}
