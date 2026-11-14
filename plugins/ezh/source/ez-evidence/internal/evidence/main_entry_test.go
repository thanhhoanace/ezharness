package evidence

import (
	"strings"
	"testing"
)

// AC-N1: `ez <cmd>` version/help/usage dispatch works; unknown blocks.
func TestRunMainVersionAndHelp(t *testing.T) {
	var out, errBuf strings.Builder
	if code := RunMain("ez", "v6.3.0", []string{"version"}, &out, &errBuf); code != exitOK {
		t.Fatalf("version exit = %d", code)
	}
	if !strings.Contains(out.String(), "ez v6.3.0") {
		t.Fatalf("version output = %q", out.String())
	}

	out.Reset()
	if code := RunMain("ez", "v6.3.0", []string{"help"}, &out, &errBuf); code != exitOK {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"loopcheck", "lesson", "attest"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage missing %q", want)
		}
	}

	errBuf.Reset()
	if code := RunMain("ez", "v6.3.0", []string{"bogus"}, &out, &errBuf); code != exitBlock {
		t.Fatalf("unknown cmd exit = %d, want block", code)
	}
}

// AC-N2: the deprecated `ezh` name still dispatches with the same exit code and
// its usage/version reflect the invoked binary name.
func TestRunMainAliasName(t *testing.T) {
	var out, errBuf strings.Builder
	if code := RunMain("ezh", "v6.3.0", []string{"version"}, &out, &errBuf); code != exitOK {
		t.Fatalf("alias version exit = %d", code)
	}
	if !strings.Contains(out.String(), "ezh v6.3.0") {
		t.Fatalf("alias version output = %q", out.String())
	}
}

// Positional arguments are a usage error, never silently ignored: Go's flag
// parser stops at the first non-flag token, so `lesson lookup --changed a b`
// would otherwise drop "b" (and any flags after it) and still exit 0.
func TestPositionalArgsRejected(t *testing.T) {
	cases := [][]string{
		{"lesson", "lookup", "--changed", "a.go", "stray.go"},
		{"lesson", "lint", "stray"},
		{"loopcheck", "--contract", "x.yaml", "stray"},
	}
	for _, args := range cases {
		var out, errBuf strings.Builder
		code := RunMain("ez", "v6.3.0", args, &out, &errBuf)
		if code == exitOK {
			t.Fatalf("%v: exit 0, want usage error", args)
		}
		if !strings.Contains(errBuf.String(), "unexpected positional arguments") {
			t.Fatalf("%v: stderr %q lacks positional-args error", args, errBuf.String())
		}
	}
}

// The machine-readable capability contract: tooling (install.sh probe) checks
// this instead of grepping human error text to decide binary freshness.
func TestCapabilitiesCommand(t *testing.T) {
	var out, errBuf strings.Builder
	if code := RunMain("ez", "v6.3.0", []string{"capabilities"}, &out, &errBuf); code != exitOK {
		t.Fatalf("capabilities exit = %d, stderr %q", code, errBuf.String())
	}
	for _, want := range []string{`"protocol_version":1`, `"loopcheck"`, `"lesson.lookup"`, `"lesson.lint"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("capabilities output %q lacks %s", out.String(), want)
		}
	}
}
