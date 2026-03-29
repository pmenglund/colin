package githubutil

import "testing"

func TestParsePullRequestURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		raw       string
		wantLogin string
		wantRepo  string
		wantNum   int
		wantOK    bool
	}{
		{
			name:      "standard github url",
			raw:       "https://github.com/pmenglund/colin/pull/42",
			wantLogin: "pmenglund",
			wantRepo:  "colin",
			wantNum:   42,
			wantOK:    true,
		},
		{
			name:      "github url with extra suffix",
			raw:       "https://github.com/pmenglund/colin/pull/42/files",
			wantLogin: "pmenglund",
			wantRepo:  "colin",
			wantNum:   42,
			wantOK:    true,
		},
		{
			name:      "www github host with query string",
			raw:       "https://www.github.com/pmenglund/colin/pull/42?diff=split",
			wantLogin: "pmenglund",
			wantRepo:  "colin",
			wantNum:   42,
			wantOK:    true,
		},
		{
			name:   "non http scheme",
			raw:    "ftp://github.com/pmenglund/colin/pull/42",
			wantOK: false,
		},
		{
			name:   "non github host",
			raw:    "https://example.com/pmenglund/colin/pull/42",
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			login, repo, number, ok := ParsePullRequestURL(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ParsePullRequestURL() ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if login != tc.wantLogin || repo != tc.wantRepo || number != tc.wantNum {
				t.Fatalf("ParsePullRequestURL() = %q, %q, %d want %q, %q, %d", login, repo, number, tc.wantLogin, tc.wantRepo, tc.wantNum)
			}
		})
	}
}

func TestCanonicalPullRequestURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{
			name: "already canonical",
			raw:  "https://github.com/pmenglund/colin/pull/42",
			want: "https://github.com/pmenglund/colin/pull/42",
			ok:   true,
		},
		{
			name: "suffixed github url",
			raw:  "https://www.github.com/pmenglund/colin/pull/42/files?diff=split",
			want: "https://github.com/pmenglund/colin/pull/42",
			ok:   true,
		},
		{
			name: "non github url",
			raw:  "https://example.com/pmenglund/colin/pull/42",
			ok:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalPullRequestURL(tc.raw)
			if ok != tc.ok {
				t.Fatalf("CanonicalPullRequestURL() ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got != tc.want {
				t.Fatalf("CanonicalPullRequestURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
