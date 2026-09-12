package rbac

import "testing"

// TestPermissionsForIsConsistentWithRoleHas pins the two accessors against each
// other: whatever PermissionsFor lists, RoleHas must agree with, and vice versa
// for the whole vocabulary. A client hiding controls from the list and a server
// refusing requests from RoleHas have to make the same decision, or the UI shows
// buttons that 403.
func TestPermissionsForIsConsistentWithRoleHas(t *testing.T) {
	vocabulary := map[string]bool{}
	for _, set := range rolePermissions {
		for _, p := range set {
			if p != PermAll {
				vocabulary[p] = true
			}
		}
	}
	if len(vocabulary) < 20 {
		t.Fatalf("only %d permissions found; this test is not reading the table", len(vocabulary))
	}

	for role := range rolePermissions {
		listed := map[string]bool{}
		for _, p := range PermissionsFor(role) {
			listed[p] = true
			if !RoleHas(role, p) {
				t.Errorf("%s: PermissionsFor lists %q but RoleHas says no", role, p)
			}
		}
		for p := range vocabulary {
			if RoleHas(role, p) && !listed[p] {
				t.Errorf("%s: RoleHas allows %q but PermissionsFor omits it", role, p)
			}
		}
	}
}

func TestPermissionsForAnUnknownRoleIsEmptyNotNil(t *testing.T) {
	got := PermissionsFor("janitor")
	if got == nil {
		t.Fatal("want an empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("want nothing for an unknown role, got %v", got)
	}
}

// TestTheOwnerWildcardIsExpanded: a client must not have to understand "*".
func TestTheOwnerWildcardIsExpanded(t *testing.T) {
	owner := PermissionsFor("owner")
	for _, p := range owner {
		if p == PermAll {
			t.Fatal(`PermissionsFor("owner") returned the wildcard rather than expanding it`)
		}
	}
	if len(owner) < 20 {
		t.Fatalf("owner holds %d permissions, want the whole vocabulary", len(owner))
	}
	if !RoleHas("owner", PermStoreCreate) {
		t.Fatal("premise wrong: an owner should be able to create a store")
	}
}

// TestAllCoversTheRoleTable keeps the written-out vocabulary honest. A
// permission added to a role but not to All would never reach an owner's
// session, so the owner would be refused nothing by the server and shown
// nothing by the client — a disagreement that is very hard to see.
func TestAllCoversTheRoleTable(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range All {
		if p == PermAll {
			t.Error("All must not contain the wildcard; it is what the wildcard expands to")
		}
		if listed[p] {
			t.Errorf("All lists %q twice", p)
		}
		listed[p] = true
	}

	for role, set := range rolePermissions {
		for _, p := range set {
			if p == PermAll {
				continue
			}
			if !listed[p] {
				t.Errorf("role %q holds %q, which is missing from All", role, p)
			}
		}
	}

	// And the other direction: the owner must actually receive everything.
	owner := map[string]bool{}
	for _, p := range PermissionsFor("owner") {
		owner[p] = true
	}
	for _, p := range All {
		if !owner[p] {
			t.Errorf("the owner's permission list omits %q", p)
		}
		if !RoleHas("owner", p) {
			t.Errorf("RoleHas says the owner lacks %q", p)
		}
	}
}
