package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/DonaldMurillo/gofastr-plugins/pluginhost"
)

// The scanner needs the camera on the HOST page: an opaque origin cannot hold
// the permission, so the host captures and hands frames in. That requirement
// used to live only in prose — docs/scanner.md told adopters to relax the
// Permissions-Policy, and this app did it in a comment beside the config. A
// prose requirement is one careless edit from being untrue.
//
// gofastr v0.74.0 lets the manifest declare it, so it can be checked. These
// two tests are the check: the app's real policy must satisfy every
// requirement its plugins declare, and the check must be capable of failing.
func warningsFor(t *testing.T, policy string) string {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pluginhost.CheckHostRequirements(log, policy, hostRequirementModules()...)
	return buf.String()
}

func TestExamplePolicySatisfiesEveryDeclaredHostRequirement(t *testing.T) {
	if got := warningsFor(t, examplePermissionsPolicy); got != "" {
		t.Errorf("the app serves PermissionsPolicy %q, which does not satisfy a plugin that mounts here:\n%s",
			examplePermissionsPolicy, got)
	}

	// The policy has to actually name the camera, not merely avoid warning.
	// CheckHostRequirements is deliberately narrow: it warns only on the empty
	// allowlist "camera=()", so a policy that dropped the directive entirely
	// would stay silent while leaving the grant to the browser's default.
	if !strings.Contains(examplePermissionsPolicy, "camera=(self)") {
		t.Errorf("PermissionsPolicy %q must grant camera=(self): scanner declares it, and the "+
			"boot check stays silent on a MISSING directive, so silence is not proof",
			examplePermissionsPolicy)
	}
}

// Teeth. A check that cannot fail would pass just as happily on an app that
// denies the camera, which is the whole condition it exists to catch.
func TestHostRequirementCheckFailsOnTheFrameworkDefault(t *testing.T) {
	const denies = "geolocation=(), microphone=(), camera=()"
	got := warningsFor(t, denies)
	if got == "" {
		t.Fatalf("policy %q denies the camera and no plugin was reported; the check is inert", denies)
	}
	for _, want := range []string{"scanner", "permissions-policy:camera"} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning must name %q so a host knows what to fix; got:\n%s", want, got)
		}
	}
}
