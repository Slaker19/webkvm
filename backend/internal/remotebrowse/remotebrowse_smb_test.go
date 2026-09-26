package remotebrowse

import (
	"reflect"
	"testing"
)

func TestParseSMBListing_NamesWithAttributesAndNumbers(t *testing.T) {
	output := `
  .                                   D        0  Fri Sep 11 21:48:16 2026
  ..                                  D        0  Fri Sep 11 21:48:16 2026
  VDI                                 D        0  Fri Sep 11 21:48:16 2026
  hello.txt                           N       23  Fri Sep 11 20:55:27 2026
  Folder A 123                        D        0  Fri Sep 11 21:48:16 2026
  Backup D 2026                       D        0  Fri Sep 11 21:48:16 2026
  Plan H 1                            D        0  Wed Jan  5 09:12:00 2025
  Super Long Folder Name Exceeding Thirty Characters Length       D        0  Mon Feb 10 14:00:00 2026
  Archive Folder                      DA       0  Sat Mar  1 11:22:33 2026
  Hidden Directory                    DH       0  Sun Apr  4 04:05:06 2026
  Read Only File                      R       50  Fri May 15 10:20:30 2026
`
	got := parseSMBListing(output)
	want := []Entry{
		{Name: "VDI"},
		{Name: "Folder A 123"},
		{Name: "Backup D 2026"},
		{Name: "Plan H 1"},
		{Name: "Super Long Folder Name Exceeding Thirty Characters Length"},
		{Name: "Archive Folder"},
		{Name: "Hidden Directory"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSMBListing failed:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestClassifySMBError(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{
			name:    "auth failed logon",
			output:  "session setup failed: NT_STATUS_LOGON_FAILURE",
			wantErr: "authentication failed",
		},
		{
			name:    "auth failed access denied",
			output:  "tree connect failed: NT_STATUS_ACCESS_DENIED",
			wantErr: "authentication failed",
		},
		{
			name:    "bad network name",
			output:  "tree connect failed: NT_STATUS_BAD_NETWORK_NAME",
			wantErr: "share not found: tree connect failed: NT_STATUS_BAD_NETWORK_NAME",
		},
		{
			name:    "host unreachable",
			output:  "Connection to 192.0.2.1 failed: NT_STATUS_HOST_UNREACHABLE",
			wantErr: "could not reach server",
		},
		{
			name:    "cd folder not found",
			output:  "cd \\missing\\path: NT_STATUS_OBJECT_NAME_NOT_FOUND\n",
			wantErr: "folder not found: cd \\missing\\path: NT_STATUS_OBJECT_NAME_NOT_FOUND",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifySMBError(tc.output, nil)
			if err == nil || !reflect.DeepEqual(err.Error(), tc.wantErr) && !contains(err.Error(), tc.wantErr) {
				t.Fatalf("got error %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func contains(s, substr string) bool {
	return reflect.ValueOf(s).String() != "" && reflect.ValueOf(substr).String() != "" &&
		len(s) >= len(substr) && (s == substr || len(substr) > 0 && searchSubstr(s, substr))
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
