package git

import "testing"

func TestParseGitHubUsername(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantUser string
		wantOK   bool
	}{
		// SSH URLs
		{
			name:     "SSH with .git suffix",
			url:      "git@github.com:braw/croft.git",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "SSH without .git suffix",
			url:      "git@github.com:canny/glen",
			wantUser: "canny",
			wantOK:   true,
		},
		{
			name:     "SSH with nested path",
			url:      "git@github.com:bonnie-glen/auld-croft.git",
			wantUser: "bonnie-glen",
			wantOK:   true,
		},

		// HTTPS URLs
		{
			name:     "HTTPS with .git suffix",
			url:      "https://github.com/braw/croft.git",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "HTTPS without .git suffix",
			url:      "https://github.com/braw/croft",
			wantUser: "braw",
			wantOK:   true,
		},
		{
			name:     "HTTPS with hyphenated user",
			url:      "https://github.com/bonnie-braw/auld-croft.git",
			wantUser: "bonnie-braw",
			wantOK:   true,
		},
		{
			name:     "HTTP (no TLS)",
			url:      "http://github.com/canny/croft.git",
			wantUser: "canny",
			wantOK:   true,
		},

		// Non-GitHub URLs — should return empty
		{
			name:     "GitLab URL",
			url:      "https://gitlab.com/braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "Bitbucket URL",
			url:      "https://bitbucket.org/braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "SSH non-GitHub",
			url:      "git@gitlab.com:braw/croft.git",
			wantUser: "",
			wantOK:   false,
		},

		// Malformed URLs
		{
			name:     "empty string",
			url:      "",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "just a word",
			url:      "thrawn",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "GitHub URL with no path",
			url:      "https://github.com/",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "GitHub URL with only user",
			url:      "https://github.com/braw",
			wantUser: "",
			wantOK:   false,
		},
		{
			name:     "SSH with no slash in path",
			url:      "git@github.com:kenneep",
			wantUser: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotOK := ParseGitHubUsername(tt.url)
			if gotUser != tt.wantUser || gotOK != tt.wantOK {
				t.Errorf("ParseGitHubUsername(%q) = (%q, %v), want (%q, %v)",
					tt.url, gotUser, gotOK, tt.wantUser, tt.wantOK)
			}
		})
	}
}
