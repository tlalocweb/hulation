package config

import "testing"

func TestInferGitUsername(t *testing.T) {
	// Clear env to start from a known baseline; t.Setenv restores at end.
	t.Setenv("GITLAB_AUTH_TOKEN_USERNAME", "")
	t.Setenv("GITHUB_AUTH_TOKEN_USERNAME", "")

	cases := []struct {
		name string
		url  string
		env  map[string]string
		want string
	}{
		{"gitlab default", "https://gitlab.com/foo/bar.git", nil, "oauth2"},
		{"gitlab subdomain default", "https://www.gitlab.com/foo/bar.git", nil, "oauth2"},
		{"github default", "https://github.com/foo/bar.git", nil, "x-access-token"},
		{"github enterprise hostname", "https://github.example.com/foo.git", nil, "x-access-token"},
		{"gitlab override", "https://gitlab.com/foo.git",
			map[string]string{"GITLAB_AUTH_TOKEN_USERNAME": "deploy-bot"}, "deploy-bot"},
		{"github override", "https://github.com/foo.git",
			map[string]string{"GITHUB_AUTH_TOKEN_USERNAME": "ci-machine"}, "ci-machine"},
		{"unknown host returns empty", "https://bitbucket.org/foo.git", nil, ""},
		{"malformed url returns empty", "not-a-url", nil, ""},
		{"empty url returns empty", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := inferGitUsername(tc.url)
			if got != tc.want {
				t.Errorf("inferGitUsername(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
