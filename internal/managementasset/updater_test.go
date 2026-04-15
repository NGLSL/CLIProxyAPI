package managementasset

import "testing"

func TestResolveReleaseURLDefaultsToForkManagementRepository(t *testing.T) {
	t.Parallel()

	const want = "https://api.github.com/repos/NGLSL/Cli-Proxy-API-Management-Center/releases/latest"
	if got := resolveReleaseURL(""); got != want {
		t.Fatalf("resolveReleaseURL(\"\") = %q, want %q", got, want)
	}
}
