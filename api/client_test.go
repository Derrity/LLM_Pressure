package api

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "host only",
			in:   "https://api.example.com",
			want: "https://api.example.com/v1",
		},
		{
			name: "already v1",
			in:   "https://api.example.com/v1/",
			want: "https://api.example.com/v1",
		},
		{
			name: "full chat completions endpoint",
			in:   "https://api.example.com/v1/chat/completions",
			want: "https://api.example.com/v1",
		},
		{
			name: "nested v1 endpoint",
			in:   "https://gateway.example.com/openai/v1/chat/completions",
			want: "https://gateway.example.com/openai/v1",
		},
		{
			name: "missing v1 but full endpoint",
			in:   "https://api.example.com/chat/completions",
			want: "https://api.example.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeBaseURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeBaseURLRejectsInvalidURL(t *testing.T) {
	if _, err := NormalizeBaseURL("api.example.com/v1"); err == nil {
		t.Fatal("NormalizeBaseURL() error = nil, want invalid URL error")
	}
}
