package evidence

import "testing"

// globFixtures is the shared conformance table (AC-S1/G3/G16): the SAME cases
// exercise the matcher used by forbidden_paths, the lessons resolver, and
// risk-path gating, so all three surfaces stay byte-identical.
var globFixtures = []struct {
	pattern string
	path    string
	want    bool
}{
	// prefix/** — any number of trailing segments, including the prefix itself.
	{"clawfans-api/**/payment*/**", "clawfans-api/internal/payments/charge.go", true},
	{"infra/**", "infra/a/b/c.tf", true},
	{"infra/**", "infra", true},
	{"infra/**", "infrastructure/x", false},
	// `*` is confined to one segment: infra/* must NOT cross `/` (the G16 bug).
	{"infra/*", "infra/main.tf", true},
	{"infra/*", "infra/a/b/c.tf", false},
	// **/segment/** — segment anywhere in the tree.
	{"**/migrations/**", "clawfans-api/db/migrations/001.sql", true},
	{"**/migrations/**", "migrations/001.sql", true},
	{"**/migrations/**", "db/migration/001.sql", false},
	// wildcard within a segment.
	{"clawfans-api/**/payment*/**", "clawfans-api/x/payment-service/y.go", true},
	{"clawfans-api/**/entitlement*/**", "clawfans-api/internal/entitlements/cache.go", true},
	{"clawfans-api/**/entitlement*/**", "clawfans-api/internal/billing/cache.go", false},
	// exact literal.
	{"README.md", "README.md", true},
	{"README.md", "docs/README.md", false},
}

func TestMatchGlobConformance(t *testing.T) {
	for _, tc := range globFixtures {
		if got := matchGlob(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestValidateGlobPattern(t *testing.T) {
	valid := []string{"prefix/**", "**/segment/**", "a/*/b", "clawfans-api/**/payment*/**", "README.md"}
	for _, p := range valid {
		if err := validateGlobPattern(p); err != nil {
			t.Errorf("validateGlobPattern(%q) = %v, want nil", p, err)
		}
	}
	invalid := []string{"", "a?b", "a/{x,y}/b", "pre**fix/**", "seg**/x"}
	for _, p := range invalid {
		if err := validateGlobPattern(p); err == nil {
			t.Errorf("validateGlobPattern(%q) = nil, want error", p)
		}
	}
}

func TestMatchAnyGlob(t *testing.T) {
	pats := []string{"infra/**", "**/migrations/**"}
	if !matchAnyGlob(pats, "infra/x.tf") {
		t.Error("expected infra/x.tf to match")
	}
	if !matchAnyGlob(pats, "db/migrations/1.sql") {
		t.Error("expected db/migrations/1.sql to match")
	}
	if matchAnyGlob(pats, "src/app.go") {
		t.Error("expected src/app.go not to match")
	}
}
