// Package postgres implements institutional hierarchy reads using PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/persistence"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	"github.com/jackc/pgx/v5/pgconn"
)

type resource struct {
	table, columns string
	fields         map[string]string
}

var resources = map[string]resource{
	"catalogs":            {"parameterization.catalog", "id, code, name, description, is_active, created_at", map[string]string{"code": "code", "is_active": "is_active"}},
	"parameters":          {"parameterization.parameter", "id, key, value, value_type, description, created_at", map[string]string{"key": "key", "value_type": "value_type"}},
	"macroregions":        {"institutional_structure.macroregion", "id, name, code, is_active", map[string]string{"code": "code", "is_active": "is_active"}},
	"microregions":        {"institutional_structure.microregion", "id, macroregion_id, name, code, is_active", map[string]string{"macroregion_id": "macroregion_id", "code": "code", "is_active": "is_active"}},
	"departments":         {"institutional_structure.department", "id, microregion_id, name, dane_code, is_active", map[string]string{"microregion_id": "microregion_id", "dane_code": "dane_code", "is_active": "is_active"}},
	"municipalities":      {"institutional_structure.municipality", "id, department_id, name, dane_code, is_active", map[string]string{"department_id": "department_id", "dane_code": "dane_code", "is_active": "is_active"}},
	"training-centers":    {"institutional_structure.training_center", "id, municipality_id, center_code, name, address, phone, is_active, created_at, updated_at", map[string]string{"municipality_id": "municipality_id", "center_code": "center_code", "is_active": "is_active"}},
	"institutional-units": {"institutional_structure.institutional_unit", "id, training_center_id, name, unit_type, is_active, created_at", map[string]string{"training_center_id": "training_center_id", "unit_type": "unit_type", "is_active": "is_active"}},
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) List(ctx context.Context, query application.ListQuery) (application.Page, error) {
	resource, ok := resources[query.Resource]
	if !ok {
		return application.Page{}, fmt.Errorf("unknown hierarchy resource")
	}
	where, args := where(resource, query.Filters)
	tx, err := r.readTx(ctx)
	if err != nil {
		return application.Page{}, err
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+resource.table+where, args...).Scan(&total); err != nil {
		return application.Page{}, err
	}
	args = append(args, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, "SELECT row_to_json(t) FROM (SELECT "+resource.columns+" FROM "+resource.table+where+" ORDER BY "+query.Sort+" LIMIT $"+strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args))+") t", args...)
	if err != nil {
		return application.Page{}, err
	}
	defer rows.Close()
	data := make([]json.RawMessage, 0)
	for rows.Next() {
		var item json.RawMessage
		if err = rows.Scan(&item); err != nil {
			return application.Page{}, err
		}
		data = append(data, item)
	}
	if err = rows.Err(); err != nil {
		return application.Page{}, err
	}
	if err = tx.Commit(); err != nil {
		return application.Page{}, err
	}
	return application.Page{Data: data, TotalItems: total}, nil
}

func (r *Repository) Get(ctx context.Context, name, id string) (json.RawMessage, error) {
	resource, ok := resources[name]
	if !ok {
		return nil, fmt.Errorf("unknown hierarchy resource")
	}
	tx, err := r.readTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item json.RawMessage
	err = tx.QueryRowContext(ctx, "SELECT row_to_json(t) FROM (SELECT "+resource.columns+" FROM "+resource.table+" WHERE id = $1) t", id).Scan(&item)
	if err == sql.ErrNoRows {
		return nil, application.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *Repository) ListCatalogDetails(ctx context.Context, catalogID string, query application.ListQuery) (application.Page, error) {
	if _, err := r.catalogExists(ctx, catalogID); err != nil {
		return application.Page{}, err
	}
	filterWhere, args := where(resource{fields: map[string]string{"code": "d.code", "is_active": "d.is_active"}}, query.Filters)
	where := " WHERE d.catalog_id = $" + strconv.Itoa(len(args)+1)
	if filterClause := strings.TrimPrefix(filterWhere, " WHERE"); filterClause != "" {
		where += " AND " + filterClause
	}
	args = append(args, catalogID)
	return r.listQuery(ctx, "parameterization.catalog_detail d", "d.id, d.catalog_id, d.code, d.label, d.display_order, d.is_active, d.created_at", where, args, query)
}

func (r *Repository) ReportActiveCatalog(ctx context.Context, catalogCode string, page, pageSize int) ([]application.ActiveCatalogReportEntry, int, error) {
	tx, err := r.readTx(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	filters := ""
	args := []any{pageSize, (page - 1) * pageSize}
	if catalogCode != "" {
		filters = " AND c.code = $3"
		args = append(args, catalogCode)
	}
	var total int
	countArgs := []any{}
	countWhere := " WHERE c.is_active = true"
	if catalogCode != "" {
		countWhere += " AND c.code = $1"
		countArgs = append(countArgs, catalogCode)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM parameterization.catalog c"+countWhere, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH paged_catalogs AS (
			SELECT c.id, c.code, c.name
			FROM parameterization.catalog c
			WHERE c.is_active = true`+filters+`
			ORDER BY c.code ASC
			LIMIT $1 OFFSET $2
		)
		SELECT c.code, c.name, d.code, d.label, d.display_order
		FROM paged_catalogs c
		LEFT JOIN parameterization.catalog_detail d
			ON d.catalog_id = c.id AND d.is_active = true
		ORDER BY c.code ASC, d.display_order ASC NULLS LAST, d.code ASC`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := make([]application.ActiveCatalogReportEntry, 0)
	byCode := map[string]int{}
	for rows.Next() {
		var catalogCode, catalogName string
		var detailCode, detailLabel sql.NullString
		var displayOrder sql.NullInt64
		if err := rows.Scan(&catalogCode, &catalogName, &detailCode, &detailLabel, &displayOrder); err != nil {
			return nil, 0, err
		}
		index, ok := byCode[catalogCode]
		if !ok {
			entries = append(entries, application.ActiveCatalogReportEntry{CatalogCode: catalogCode, CatalogName: catalogName, Details: []application.CatalogReportDetail{}})
			index = len(entries) - 1
			byCode[catalogCode] = index
		}
		if detailCode.Valid {
			var order *int
			if displayOrder.Valid {
				value := int(displayOrder.Int64)
				order = &value
			}
			entries[index].Details = append(entries[index].Details, application.CatalogReportDetail{Code: detailCode.String, Label: detailLabel.String, DisplayOrder: order})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

func (r *Repository) catalogExists(ctx context.Context, id string) (bool, error) {
	tx, err := r.readTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM parameterization.catalog WHERE id = $1)", id).Scan(&exists); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if !exists {
		return false, application.ErrNotFound
	}
	return true, nil
}

func (r *Repository) listQuery(ctx context.Context, table, columns, whereClause string, args []any, query application.ListQuery) (application.Page, error) {
	tx, err := r.readTx(ctx)
	if err != nil {
		return application.Page{}, err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+whereClause, args...).Scan(&total); err != nil {
		return application.Page{}, err
	}
	args = append(args, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := tx.QueryContext(ctx, "SELECT row_to_json(t) FROM (SELECT "+columns+" FROM "+table+whereClause+" ORDER BY "+query.Sort+" LIMIT $"+strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args))+") t", args...)
	if err != nil {
		return application.Page{}, err
	}
	defer rows.Close()
	data := make([]json.RawMessage, 0)
	for rows.Next() {
		var item json.RawMessage
		if err := rows.Scan(&item); err != nil {
			return application.Page{}, err
		}
		data = append(data, item)
	}
	if err := rows.Err(); err != nil {
		return application.Page{}, err
	}
	if err := tx.Commit(); err != nil {
		return application.Page{}, err
	}
	return application.Page{Data: data, TotalItems: total}, nil
}

func (r *Repository) readTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE reference_reader"); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func where(resource resource, filters map[string]string) (string, []any) {
	clauses, args := make([]string, 0, len(filters)), make([]any, 0, len(filters))
	for key, value := range filters {
		column := resource.fields[key]
		args = append(args, value)
		clauses = append(clauses, column+" = $"+strconv.Itoa(len(args)))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// Write operations intentionally depend on PostgreSQL constraints. A prior
// SELECT would not make uniqueness or parent checks safe under concurrency.
type writableResource struct {
	table, returning string
	fields           func(application.Command) []writeField
}

type writeField struct {
	column string
	value  any
	set    bool
}

var writableResources = map[string]writableResource{
	"catalogs": {"parameterization.catalog", "id, code, name, description, is_active, created_at", func(c application.Command) []writeField {
		return []writeField{{"code", value(c.Code), c.Code != nil}, {"name", value(c.Name), c.Name != nil}, {"description", value(c.Description), c.Description != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"parameters": {"parameterization.parameter", "id, key, value, value_type, description, created_at", func(c application.Command) []writeField {
		return []writeField{{"key", value(c.Key), c.Key != nil}, {"value", value(c.Value), c.Value != nil}, {"value_type", value(c.ValueType), c.ValueType != nil}, {"description", value(c.Description), c.Description != nil}}
	}},
	"catalog-details": {"parameterization.catalog_detail", "id, catalog_id, code, label, display_order, is_active, created_at", func(c application.Command) []writeField {
		return []writeField{{"catalog_id", value(c.ParentID), c.ParentID != nil}, {"code", value(c.Code), c.Code != nil}, {"label", value(c.Name), c.Name != nil}, {"display_order", value(c.DisplayOrder), c.DisplayOrder != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"macroregions": {"institutional_structure.macroregion", "id, name, code, is_active", func(c application.Command) []writeField {
		return []writeField{{"name", value(c.Name), c.Name != nil}, {"code", value(c.Code), c.Code != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"microregions": {"institutional_structure.microregion", "id, macroregion_id, name, code, is_active", func(c application.Command) []writeField {
		return []writeField{{"macroregion_id", value(c.ParentID), c.ParentID != nil}, {"name", value(c.Name), c.Name != nil}, {"code", value(c.Code), c.Code != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"departments": {"institutional_structure.department", "id, microregion_id, name, dane_code, is_active", func(c application.Command) []writeField {
		return []writeField{{"microregion_id", value(c.ParentID), c.ParentID != nil}, {"name", value(c.Name), c.Name != nil}, {"dane_code", value(c.DaneCode), c.DaneCode != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"municipalities": {"institutional_structure.municipality", "id, department_id, name, dane_code, is_active", func(c application.Command) []writeField {
		return []writeField{{"department_id", value(c.ParentID), c.ParentID != nil}, {"name", value(c.Name), c.Name != nil}, {"dane_code", value(c.DaneCode), c.DaneCode != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"training-centers": {"institutional_structure.training_center", "id, municipality_id, center_code, name, address, phone, is_active, created_at, updated_at", func(c application.Command) []writeField {
		return []writeField{{"municipality_id", value(c.ParentID), c.ParentID != nil}, {"center_code", value(c.Code), c.Code != nil}, {"name", value(c.Name), c.Name != nil}, {"address", value(c.Address), c.Address != nil}, {"phone", value(c.Phone), c.Phone != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
	"institutional-units": {"institutional_structure.institutional_unit", "id, training_center_id, name, unit_type, is_active, created_at", func(c application.Command) []writeField {
		return []writeField{{"training_center_id", value(c.ParentID), c.ParentID != nil}, {"name", value(c.Name), c.Name != nil}, {"unit_type", value(c.UnitType), c.UnitType != nil}, {"is_active", value(c.IsActive), c.IsActive != nil}}
	}},
}

func value[T any](pointer *T) any {
	if pointer == nil {
		return nil
	}
	return *pointer
}

func (r *Repository) Create(ctx context.Context, name string, command application.Command) (json.RawMessage, error) {
	return r.create(ctx, name, command, nil)
}

func (r *Repository) CreateWithOutbox(ctx context.Context, name string, command application.Command, aggregateType, eventType string, build func(json.RawMessage) (any, error)) (json.RawMessage, error) {
	return r.create(ctx, name, command, r.outboxAppender(ctx, aggregateType, eventType, build))
}

func (r *Repository) UpdateWithOutbox(ctx context.Context, name, id string, command application.Command, aggregateType, eventType string, build func(json.RawMessage) (any, error)) (json.RawMessage, error) {
	return r.update(ctx, name, id, command, r.outboxAppender(ctx, aggregateType, eventType, build))
}

func (r *Repository) DeactivateWithOutbox(ctx context.Context, name, id string, command application.Command, parents []string, aggregateType, eventType string, build func(json.RawMessage) (any, error)) (json.RawMessage, error) {
	return r.deactivate(ctx, name, id, command, parents, r.outboxAppender(ctx, aggregateType, eventType, build))
}

func (r *Repository) outboxAppender(ctx context.Context, aggregateType, eventType string, build func(json.RawMessage) (any, error)) func(*sql.Tx, json.RawMessage) error {
	return func(tx *sql.Tx, item json.RawMessage) error {
		payload, err := build(item)
		if err != nil {
			return err
		}
		event, err := persistence.NewOutboxEvent(aggregateType, aggregateID(item), eventType, payload)
		if err != nil {
			return err
		}
		return persistence.NewOutboxStore(r.db).InsertTx(ctx, tx, event)
	}
}

func (r *Repository) create(ctx context.Context, name string, command application.Command, appendEvent func(*sql.Tx, json.RawMessage) error) (json.RawMessage, error) {
	resource, ok := writableResources[name]
	if !ok {
		return nil, fmt.Errorf("unknown hierarchy resource")
	}
	tx, err := r.writeTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	statement, args := insertStatement(resource, command)
	item, err := writeRow(ctx, tx, statement, args)
	if err != nil {
		return nil, translateWriteError(err)
	}
	if appendEvent != nil {
		if err := appendEvent(tx, item); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func aggregateID(item json.RawMessage) string {
	var value struct {
		ID        string `json:"id"`
		CatalogID string `json:"catalog_id"`
	}
	_ = json.Unmarshal(item, &value)
	if value.CatalogID != "" {
		return value.CatalogID
	}
	return value.ID
}
func (r *Repository) Update(ctx context.Context, name, id string, command application.Command) (json.RawMessage, error) {
	return r.update(ctx, name, id, command, nil)
}

func (r *Repository) update(ctx context.Context, name, id string, command application.Command, appendEvent func(*sql.Tx, json.RawMessage) error) (json.RawMessage, error) {
	resource, ok := writableResources[name]
	if !ok {
		return nil, fmt.Errorf("unknown hierarchy resource")
	}
	tx, err := r.writeTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if name == "training-centers" && command.Code != nil {
		var current string
		err = tx.QueryRowContext(ctx, "SELECT center_code FROM institutional_structure.training_center WHERE id = $1", id).Scan(&current)
		if err == sql.ErrNoRows {
			return nil, application.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if current != *command.Code {
			return nil, application.ErrConflict
		}
	}
	if name == "catalog-details" && command.ParentID != nil {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM parameterization.catalog_detail WHERE id = $1 AND catalog_id = $2)", id, *command.ParentID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, application.ErrNotFound
		}
	}
	statement, args := updateStatement(resource, command, id)
	item, err := writeRow(ctx, tx, statement, args)
	if err != nil {
		return nil, translateWriteError(err)
	}
	if appendEvent != nil {
		if err := appendEvent(tx, item); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}
func insertStatement(resource writableResource, command application.Command) (string, []any) {
	columns, placeholders, args := []string{}, []string{}, []any{}
	for _, field := range resource.fields(command) {
		if field.set {
			columns = append(columns, field.column)
			args = append(args, field.value)
			placeholders = append(placeholders, "$"+strconv.Itoa(len(args)))
		}
	}
	return "INSERT INTO " + resource.table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ") RETURNING " + resource.returning, args
}
func updateStatement(resource writableResource, command application.Command, id string) (string, []any) {
	sets, args := []string{}, []any{}
	for _, field := range resource.fields(command) {
		if field.set {
			args = append(args, field.value)
			sets = append(sets, field.column+" = $"+strconv.Itoa(len(args)))
		}
	}
	args = append(args, id)
	return "UPDATE " + resource.table + " SET " + strings.Join(sets, ", ") + " WHERE id = $" + strconv.Itoa(len(args)) + " RETURNING " + resource.returning, args
}
func (r *Repository) Deactivate(ctx context.Context, name, id string, command application.Command, parentIDs ...string) (json.RawMessage, error) {
	return r.deactivate(ctx, name, id, command, parentIDs, nil)
}

func (r *Repository) deactivate(ctx context.Context, name, id string, command application.Command, parentIDs []string, appendEvent func(*sql.Tx, json.RawMessage) error) (json.RawMessage, error) {
	resource, ok := writableResources[name]
	if !ok {
		return nil, fmt.Errorf("unknown hierarchy resource")
	}
	tx, err := r.writeTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if name == "catalog-details" && len(parentIDs) > 0 {
		var item json.RawMessage
		err := tx.QueryRowContext(ctx, `SELECT row_to_json(t) FROM (
			SELECT d.id, d.catalog_id, c.code AS catalog_code, d.code, d.label
			FROM parameterization.catalog_detail d
			JOIN parameterization.catalog c ON c.id = d.catalog_id
			WHERE d.id = $1 AND d.catalog_id = $2
		) t`, id, parentIDs[0]).Scan(&item)
		if err == sql.ErrNoRows {
			return nil, application.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, "UPDATE "+resource.table+" SET is_active = false WHERE id = $1 AND catalog_id = $2", id, parentIDs[0])
		if err != nil {
			return nil, translateWriteError(err)
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return nil, application.ErrNotFound
		}
		if appendEvent != nil {
			if err := appendEvent(tx, item); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return item, nil
	}
	var item json.RawMessage
	if name == "catalogs" {
		if err := tx.QueryRowContext(ctx, `SELECT row_to_json(t) FROM (
			SELECT id, code, name FROM parameterization.catalog WHERE id = $1
		) t`, id).Scan(&item); err == sql.ErrNoRows {
			return nil, application.ErrNotFound
		} else if err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE "+resource.table+" SET is_active = false WHERE id = $1", id)
	if err != nil {
		return nil, translateWriteError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, application.ErrNotFound
	}
	if appendEvent != nil && len(item) > 0 {
		if err := appendEvent(tx, item); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func writeRow(ctx context.Context, tx *sql.Tx, statement string, args []any) (json.RawMessage, error) {
	var item json.RawMessage
	err := tx.QueryRowContext(ctx, "WITH written AS ("+statement+") SELECT row_to_json(written) FROM written", args...).Scan(&item)
	if err == sql.ErrNoRows {
		return nil, application.ErrNotFound
	}
	return item, err
}
func (r *Repository) writeTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL ROLE reference_writer"); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func translateWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == "23505" && uniqueConstraints[pgErr.ConstraintName] {
		return application.ErrConflict
	}
	if pgErr.Code == "23503" && (pgErr.ConstraintName == "fk_microregion_macroregion" || pgErr.ConstraintName == "fk_department_microregion" || pgErr.ConstraintName == "fk_municipality_department" || pgErr.ConstraintName == "fk_catalog_detail_catalog") {
		return application.ErrNotFound
	}
	if pgErr.Code == "23503" && pgErr.ConstraintName == "fk_institutional_unit_training_center" {
		return application.ErrNotFound
	}
	return err
}

var uniqueConstraints = map[string]bool{
	"uq_catalog_code":                    true,
	"uq_catalog_detail_catalog_id_code":  true,
	"uq_parameter_key":                   true,
	"uq_macroregion_code":                true,
	"uq_microregion_macroregion_id_code": true,
	"uq_department_dane_code":            true,
	"uq_department_microregion_id_name":  true,
	"uq_municipality_dane_code":          true,
	"uq_municipality_department_id_name": true,
	"uq_training_center_center_code":     true,
}
