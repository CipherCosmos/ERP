package handlers

import "strconv"

// clampLimit turns a client-supplied row limit into a bounded decimal string
// that is safe to interpolate into a SQL LIMIT clause. A limit cannot be bound
// as a query parameter here, so the value must never survive as
// attacker-controlled text: everything this returns comes from strconv.Itoa.
func clampLimit(raw string, fallback, max int) string {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		n = fallback
	}
	if n > max {
		n = max
	}
	return strconv.Itoa(n)
}
