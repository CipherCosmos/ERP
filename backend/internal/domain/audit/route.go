package audit

import "strings"

// apiPrefix is stripped from a route before it is read, so the trail records
// `invoices` rather than `api`. Versioning the URL should not reshape the
// audit trail.
const apiPrefix = "/api/v1"

// DescribeRoute derives what a mutating request changed from the route that
// served it: which resource, which record, and what was done to it.
//
// It takes the route *template* (`/api/v1/invoices/:id/void`) alongside the
// request path (`/api/v1/invoices/7f3a…/void`). The template says which
// segments are identifiers; the path supplies their values. Reading the path
// alone cannot distinguish the two, which is how a trail ends up with a
// separate `table_name` for every id it has ever seen.
//
// The rules, in order:
//
//   - table is the first segment — the resource the request acted on.
//   - recordID is the value of the *last* identifier, which is the most
//     specific record named. On `/stores/:id/terminals/:terminalID/deactivate`
//     that is the terminal being deactivated, not the store containing it.
//   - action is whatever literal segments remain, joined: `void`, `approve`,
//     `gstr1_mark_filed`. A route with none is a plain REST mutation, so the
//     method names it: create, update or delete.
//
// This replaces a constant `http_request`, which put every row in one bucket
// and left the screen's resource filter — and the (table_name, record_id)
// index behind it — matching nothing.
func DescribeRoute(method, template, path string) (table, recordID, action string) {
	template = strings.TrimSuffix(strings.TrimPrefix(template, apiPrefix), "/")
	path = strings.TrimSuffix(strings.TrimPrefix(path, apiPrefix), "/")

	// gin leaves the template empty when no route matched. Such a request was
	// answered 404 and is not recorded, but a caller that records one anyway
	// should get the resource rather than nothing.
	if template == "" {
		template = path
	}

	templateSegments := splitPath(template)
	pathSegments := splitPath(path)
	if len(templateSegments) == 0 {
		return "", "", methodAction(method)
	}

	table = normaliseSegment(templateSegments[0])

	var actionParts []string
	for i, segment := range templateSegments[1:] {
		if isRouteParam(segment) {
			// Positions line up because the value came from this template. A
			// mismatched pair yields no id rather than a misattributed one.
			if i+1 < len(pathSegments) {
				recordID = pathSegments[i+1]
			}
			continue
		}
		actionParts = append(actionParts, normaliseSegment(segment))
	}

	if len(actionParts) == 0 {
		return table, recordID, methodAction(method)
	}
	return table, recordID, strings.Join(actionParts, "_")
}

// splitPath returns the non-empty segments of a path.
func splitPath(p string) []string {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	out := segments[:0]
	for _, s := range segments {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isRouteParam reports whether a template segment stands for a value rather
// than naming itself. gin writes `:id` for one segment and `*rest` for the
// remainder of a path.
func isRouteParam(segment string) bool {
	return strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*")
}

// normaliseSegment renders a URL segment the way the rest of the trail names
// things, so `purchase-returns` filters as `purchase_returns`.
func normaliseSegment(segment string) string {
	return strings.ToLower(strings.ReplaceAll(segment, "-", "_"))
}

// methodAction names a plain REST mutation, for a route that carries no verb
// of its own.
func methodAction(method string) string {
	switch strings.ToUpper(method) {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	default:
		// NewEntry rejects a blank action, and an unrecorded change is worse
		// than an oddly named one.
		return strings.ToLower(method)
	}
}
