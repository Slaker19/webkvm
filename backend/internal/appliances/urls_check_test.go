package appliances

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Every builtin download URL must (a) pass the source whitelist and (b)
// actually resolve on the network — a catalog entry that 404s ships a
// broken deploy button.
func TestBuiltinCatalogURLsValidateAndResolve(t *testing.T) {
	for _, a := range Defaults {
		if a.URL == "" {
			continue
		}
		if err := ValidateSourceURL(a.URL); err != nil {
			t.Errorf("%s: %v", a.ID, err)
			continue
		}
		// Network check only on demand: CI runners may have no egress.
		if os.Getenv("CHECK_CATALOG_URLS") == "" {
			continue
		}
		out, err := exec.Command("curl", "-sIL", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "30", a.URL).Output()
		code := strings.TrimSpace(string(out))
		if err != nil || code == "000" {
			t.Errorf("%s: %s unreachable (%s, %v)", a.ID, a.URL, code, err)
			continue
		}
		if code[0] == '4' || code[0] == '5' {
			t.Errorf("%s: %s returns %s", a.ID, a.URL, code)
		}
	}
}
