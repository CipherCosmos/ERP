package rbac

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTheWebMenuNamesRealPermissions checks the one place the web client still
// has to spell a permission out.
//
// The menu decides which screens to show from the permission list the server
// sends with the session. A typo there hides a screen silently and forever: the
// string matches nothing, the entry never appears, and nobody can tell that from
// a role genuinely lacking the permission. This is the cheapest place to catch
// that, and it is the only cross-language coupling left after the client stopped
// keeping its own role matrix.
//
// It skips rather than fails when the file is absent, so the backend still
// builds and tests on its own.
func TestTheWebMenuNamesRealPermissions(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "pages", "Menu.tsx")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("web client not present at %s: %v", path, err)
	}

	named := regexp.MustCompile(`permission:\s*'([^']+)'`).FindAllStringSubmatch(string(src), -1)
	if len(named) == 0 {
		t.Fatalf("no permissions found in %s; this check is not reading what it thinks it is", path)
	}

	// The vocabulary, not the union of the roles: user.register belongs to the
	// owner alone, through the wildcard, and deriving the set from the role table
	// would report it as unknown.
	known := map[string]bool{}
	for _, p := range All {
		known[p] = true
	}

	var unknown []string
	for _, m := range named {
		if !known[m[1]] {
			unknown = append(unknown, m[1])
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("the web menu names %d permission(s) this system does not define:\n  %s\n\n"+
			"Either the string is a typo, or the permission was renamed here and not there.",
			len(unknown), strings.Join(unknown, "\n  "))
	}
	t.Logf("%d menu entries checked against %d known permissions", len(named), len(known))
}
