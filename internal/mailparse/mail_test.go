package mailparse

import "testing"

func TestParseAddressTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		address   string
		wantChat  int64
		wantTopic int64
		wantErr   bool
	}{
		{
			name:      "chat and thread",
			address:   "test+-1001234567890+23@gmail.com",
			wantChat:  -1001234567890,
			wantTopic: 23,
		},
		{
			name:     "chat only",
			address:  "test+-1001234567890@gmail.com",
			wantChat: -1001234567890,
		},
		{
			name:    "missing suffix",
			address: "test@gmail.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotChat, gotTopic, err := parseAddressTarget(tt.address)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.address)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAddressTarget(%q) error: %v", tt.address, err)
			}
			if gotChat != tt.wantChat || gotTopic != tt.wantTopic {
				t.Fatalf("parseAddressTarget(%q) = (%d, %d), want (%d, %d)", tt.address, gotChat, gotTopic, tt.wantChat, tt.wantTopic)
			}
		})
	}
}

func TestExtractFirstURL(t *testing.T) {
	t.Parallel()

	text := "Build failed\nhttps://example.com/dashboard?id=42\nPlease check it"
	cleaned, gotURL := extractFirstURL(text)

	if gotURL != "https://example.com/dashboard?id=42" {
		t.Fatalf("unexpected url: %q", gotURL)
	}

	wantText := "Build failed\n\nPlease check it"
	if cleaned != wantText {
		t.Fatalf("unexpected cleaned text: %q, want %q", cleaned, wantText)
	}
}

func TestNormalizeSafeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "https ok",
			raw:  "https://example.com/dashboard?id=42",
			want: "https://example.com/dashboard?id=42",
		},
		{
			name:    "javascript blocked",
			raw:     "javascript:alert(1)",
			wantErr: true,
		},
		{
			name:    "missing host blocked",
			raw:     "https:///only-path",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeSafeURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSafeURL(%q) error: %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeSafeURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
