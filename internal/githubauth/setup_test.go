package githubauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseRepositoryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    Repository
		wantErr bool
	}{
		{
			name: "ssh",
			raw:  "git@github.com:acme/widgets.git",
			want: Repository{Owner: "acme", Name: "widgets", URL: "git@github.com:acme/widgets.git"},
		},
		{
			name: "https",
			raw:  "https://github.com/acme/widgets.git",
			want: Repository{Owner: "acme", Name: "widgets", URL: "https://github.com/acme/widgets.git"},
		},
		{
			name: "ssh scheme",
			raw:  "ssh://git@github.com/acme/widgets.git",
			want: Repository{Owner: "acme", Name: "widgets", URL: "ssh://git@github.com/acme/widgets.git"},
		},
		{
			name:    "non github host",
			raw:     "git@example.com:acme/widgets.git",
			wantErr: true,
		},
		{
			name:    "missing repo path",
			raw:     "https://github.com/acme",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseRepositoryURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ParseRepositoryURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRepositoryURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseRepositoryURL() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBuildSetupDetailsIncludesRecommendedPermissions(t *testing.T) {
	t.Parallel()

	details := BuildSetupDetails(Repository{
		Owner: "acme",
		Name:  "widgets",
		URL:   "git@github.com:acme/widgets.git",
	})

	parsed, err := url.Parse(details.FineGrainedTokenURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	query := parsed.Query()
	if got := query.Get("target_name"); got != "acme" {
		t.Fatalf("target_name = %q, want acme", got)
	}
	if got := query.Get("contents"); got != "write" {
		t.Fatalf("contents = %q, want write", got)
	}
	if got := query.Get("pull_requests"); got != "write" {
		t.Fatalf("pull_requests = %q, want write", got)
	}
	if got := query.Get("expires_in"); got != "90" {
		t.Fatalf("expires_in = %q, want 90", got)
	}
}

func TestRenderInstructionsMentionsFallbackAndSetupCommand(t *testing.T) {
	t.Parallel()

	text := RenderInstructions(BuildSetupDetails(Repository{
		Owner: "acme",
		Name:  "widgets",
		URL:   "git@github.com:acme/widgets.git",
	}), "colin setup github token")

	for _, want := range []string{
		"fine-grained personal access token",
		"Contents: Read and write; Pull requests: Read and write",
		"colin setup github token",
		"classic personal access token with the `repo` scope",
		"GITHUB_TOKEN",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("RenderInstructions() missing %q in %q", want, text)
		}
	}
}
