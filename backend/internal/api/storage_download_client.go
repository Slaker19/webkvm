package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// safeISOFilename rejects names that contain path separators or `..`
// components up front, then returns filepath.Base of the result. This
// means a filename like "../../etc/passwd" is rejected with a clear
// error rather than silently rewritten to "passwd".
func safeISOFilename(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("filename is required")
	}
	if strings.ContainsAny(trimmed, "/\\") {
		return "", fmt.Errorf("filename must not contain path separators")
	}
	if strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("filename must not contain '..'")
	}
	// Reject control characters and null bytes.
	if strings.ContainsAny(trimmed, "\x00\n\r\t") {
		return "", fmt.Errorf("filename contains invalid characters")
	}
	cleaned := filepath.Base(trimmed)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("invalid filename")
	}
	return cleaned, nil
}

// safeDownloadURL blocks requests aimed at loopback, private, or
// link-local addresses (RFC1918, IPv4 link-local including cloud
// metadata 169.254.169.254, IPv6 ULA, etc). Returns nil if the URL
// is safe, otherwise an error.
func safeDownloadURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL must include a host")
	}
	// Quick textual check for the common local names.
	switch strings.ToLower(host) {
	case "localhost", "ip6-localhost", "ip6-loopback":
		return fmt.Errorf("URL host is not allowed")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve host: %w", err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("URL resolves to a blocked address: %s", ip)
		}
	}
	return nil
}

// isBlockedIP returns true if ip is loopback, private, link-local,
// multicast, or otherwise unsuitable for outbound HTTP from a
// server-side fetch.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// 169.254.0.0/16 (cloud metadata) and 100.64.0.0/10 (CGNAT) aren't
	// covered by the standard library categorisation in all versions.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 0.0.0.0/8 ("this network"), 198.18.0.0/15 (benchmarking) and
		// 240.0.0.0/4 (reserved) are never legitimate download hosts.
		if v4[0] == 0 || (v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)) || v4[0] >= 240 {
			return true
		}
		return false
	}
	// NAT64 (64:ff9b::/96, 64:ff9b:1::/48) would let a hostname reach IPv4
	// internal addresses through a translator.
	if len(ip) == net.IPv6len && ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b {
		if ip[4] == 0 && ip[5] == 0 {
			return true
		}
		if ip[4] == 0 && ip[5] == 1 {
			return true
		}
	}
	return false
}

// secureDownloadWithProgress downloads url to destPath using a
// DNS-rebind-safe transport, reporting progress via updateJob(jobID, ...)
// as it goes. This is the single entry point every server-side fetch must
// use: ISO downloads, appliance/OVA images and cloud base images all share
// it, so the SSRF protections (blocked address ranges, redirect
// re-validation) can never drift apart between call sites again.
func secureDownloadWithProgress(jobID, url, destPath string, timeout time.Duration, maxBytes int64) (int64, error) {
	if err := safeDownloadURL(url); err != nil {
		return 0, err
	}
	return downloadWithClient(newSafeDownloadClient(timeout), jobID, url, destPath, maxBytes)
}

// newSafeDownloadClient builds the DNS-rebind-safe http.Client shared by
// every download call site: it resolves the host itself, refuses every
// blocked address (loopback/private/link-local/CGNAT), dials the approved
// IP directly (so no second lookup can re-open a rebinding window), and
// re-validates each redirect target.
func newSafeDownloadClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.LookupIP(host)
			if err != nil {
				return nil, fmt.Errorf("resolve blocked: %w", err)
			}
			var lastErr error
			for _, ip := range ips {
				if isBlockedIP(ip) {
					continue
				}
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("connection to %s is not allowed", host)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http scheme blocked")
			}
			if err := safeDownloadURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// downloadWithClient performs the GET and the progress-reporting copy with
// an already-built client. Split out from secureDownloadWithProgress so
// tests can inject a plain client against httptest.NewServer, which is
// unavoidably on 127.0.0.1 and would otherwise always be refused by the
// loopback check in safeDownloadURL.
func downloadWithClient(client *http.Client, jobID, url, destPath string, maxBytes int64) (int64, error) {
	if strings.Contains(destPath, "..") {
		return 0, fmt.Errorf("invalid destination path: traversal not allowed")
	}
	resp, err := client.Get(url)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Read one byte beyond the advertised limit so an oversized response is
	// detected instead of being silently truncated and marked completed.
	limitReader := io.LimitReader(resp.Body, maxBytes+1)
	dst, err := createUploadFile(destPath)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("destination %q already exists; refusing to overwrite", filepath.Base(destPath))
		}
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer dst.Close()

	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	updateJobFull(jobID, 0, "downloading", "", 0, total, 0, 0)
	// Progress updates are throttled to ~4/s so a fast download can't
	// hammer the job mutex.
	var lastReport time.Time
	var lastBytes int64
	var smoothedSpeed float64

	prog := &progressReportingWriter{
		w:     dst,
		total: total,
		report: func(n int64) {
			now := time.Now()
			if n < total && now.Sub(lastReport) < 250*time.Millisecond {
				return
			}
			var speedBps int64
			var etaSeconds int64
			if !lastReport.IsZero() {
				elapsed := now.Sub(lastReport).Seconds()
				if elapsed > 0 {
					currentSpeed := float64(n-lastBytes) / elapsed
					if smoothedSpeed == 0 {
						smoothedSpeed = currentSpeed
					} else {
						smoothedSpeed = smoothedSpeed*0.7 + currentSpeed*0.3
					}
					speedBps = int64(smoothedSpeed)
					if speedBps > 0 && total > n {
						etaSeconds = (total - n) / speedBps
					}
				}
			}
			lastReport = now
			lastBytes = n

			pct := 0.0
			if total > 0 {
				pct = float64(n) / float64(total) * 100
				if pct > 100 {
					pct = 100
				}
			}
			updateJobFull(jobID, pct, "downloading", "", n, total, speedBps, etaSeconds)
		},
	}
	written, err := io.Copy(prog, limitReader)
	if err != nil {
		_ = os.Remove(destPath)
		return written, fmt.Errorf("download interrupted: %w", err)
	}
	if written > maxBytes {
		_ = dst.Close()
		_ = os.Remove(destPath)
		return written, fmt.Errorf("download exceeds the %d byte limit", maxBytes)
	}
	return written, nil
}
