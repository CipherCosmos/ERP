package postgres

import (
	"context"
	"fmt"
	"strings"

	"gbos/internal/domain/audit"
	platform "gbos/internal/platform/postgres"

	"github.com/google/uuid"
)

// auditDefaultLimit matches what the audit screen asks for when the caller
// does not say.
const auditDefaultLimit = 50

// auditMaxLimit is the ceiling a caller cannot exceed. The trail is the
// largest table in the system, so an unbounded listing is a way to take the
// server down from a browser.
const auditMaxLimit = 500

// Audit reads and writes the change trail.
type Audit struct{}

// NewAudit returns an audit repository.
func NewAudit() *Audit { return &Audit{} }

func auditClampLimit(limit int) int {
	if limit <= 0 {
		return auditDefaultLimit
	}
	if limit > auditMaxLimit {
		return auditMaxLimit
	}
	return limit
}

// auditColumns is the read model the trail screen renders.
const auditColumns = `a.id, a.user_id, COALESCE(u.username, ''), a.store_id, a.action,
	COALESCE(a.table_name, ''), COALESCE(a.record_id, ''),
	COALESCE(a.old_value, ''), COALESCE(a.new_value, ''), a.created_at`

// List returns one store's trail, most recent first.
func (r *Audit) List(ctx context.Context, q platform.Querier, f audit.Filter) ([]audit.Entry, error) {
	var sb strings.Builder
	// LEFT JOIN, not JOIN: the user reference is ON DELETE RESTRICT so a row
	// should always match, but an entry that somehow lost its user must still
	// appear in the trail. An inner join would make it disappear, which is the
	// one thing an audit trail must never do.
	sb.WriteString(`SELECT ` + auditColumns + `
		  FROM audit_logs a
		  LEFT JOIN users u ON u.id = a.user_id
		 WHERE a.store_id = $1`)
	args := []any{f.StoreID}

	if f.UserID != nil {
		args = append(args, *f.UserID)
		fmt.Fprintf(&sb, ` AND a.user_id = $%d`, len(args))
	}
	if f.TableName != "" {
		args = append(args, f.TableName)
		fmt.Fprintf(&sb, ` AND a.table_name = $%d`, len(args))
	}

	args = append(args, auditClampLimit(f.Limit))
	fmt.Fprintf(&sb, ` ORDER BY a.created_at DESC, a.id DESC LIMIT $%d`, len(args))

	rows, err := q.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	entries := make([]audit.Entry, 0)
	for rows.Next() {
		var e audit.Entry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Username, &e.StoreID, &e.Action,
			&e.TableName, &e.RecordID, &e.OldValue, &e.NewValue, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", platform.MapError(err))
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return entries, nil
}

// DistinctTables returns the resources this store's trail actually mentions,
// in name order.
//
// The screen's filter is built from this rather than from a list typed into
// the client, because a hand-maintained list is wrong the moment a route is
// added or renamed — and wrong in the silent direction, offering a filter that
// matches nothing.
func (r *Audit) DistinctTables(ctx context.Context, q platform.Querier, storeID uuid.UUID) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT table_name
		   FROM audit_logs
		  WHERE store_id = $1 AND table_name IS NOT NULL AND table_name <> ''
		  ORDER BY table_name`, storeID)
	if err != nil {
		return nil, platform.MapError(err)
	}
	defer rows.Close()

	tables := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan audit table name: %w", platform.MapError(err))
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, platform.MapError(err)
	}
	return tables, nil
}

// Record inserts one trail entry.
func (r *Audit) Record(ctx context.Context, q platform.Querier, e audit.Entry) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO audit_logs (id, user_id, store_id, action, table_name, record_id, old_value, new_value)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.UserID, e.StoreID, e.Action,
		nullIfBlank(e.TableName), nullIfBlank(e.RecordID),
		nullIfBlank(e.OldValue), nullIfBlank(e.NewValue))
	return platform.MapError(err)
}
