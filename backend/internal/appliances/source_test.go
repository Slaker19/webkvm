package appliances

import (
	"testing"
)

func TestValidateSourceURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid official host",
			url:     "https://cloud-images.ubuntu.com/releases/22.04/release/ubuntu-22.04-server-cloudimg-amd64.img",
			wantErr: false,
		},
		{
			name:    "valid official host 2",
			url:     "https://download.fedoraproject.org/pub/fedora/linux/releases/39/Cloud/x86_64/images/Fedora-Cloud-Base-39-1.5.x86_64.qcow2",
			wantErr: false,
		},
		{
			name:    "valid github release asset",
			url:     "https://github.com/home-assistant/operating-system/releases/download/12.3/haos_ova-12.3.qcow2.xz",
			wantErr: false,
		},
		{
			name:    "invalid github path",
			url:     "https://github.com/someuser/somerepo/releases/download/v1.0/image.qcow2",
			wantErr: true,
		},
		{
			name:    "http scheme",
			url:     "http://cloud-images.ubuntu.com/releases/22.04/release/ubuntu-22.04-server-cloudimg-amd64.img",
			wantErr: true,
		},
		{
			name:    "unknown host",
			url:     "https://evil-hacker.com/image.qcow2",
			wantErr: true,
		},
		{
			name:    "invalid url format",
			url:     "://invalid-url",
			wantErr: true,
		},
        {
			name:    "subdomain mismatch",
			url:     "https://cloud-images.ubuntu.com.evil.com/x.qcow2",
			wantErr: true,
		},
        {
			name:    "ftp scheme",
			url:     "ftp://cloud-images.ubuntu.com/x.img",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSourceURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSourceURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
