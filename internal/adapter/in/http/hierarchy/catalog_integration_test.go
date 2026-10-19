package hierarchy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/postgres"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCatalogReadIntegration(t *testing.T) {
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" {
		t.Skip("REFERENCE_DATA_TEST_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	fixtures := createCatalogFixtures(t, db)
	t.Cleanup(fixtures.cleanup)

	mux := http.NewServeMux()
	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)))
	security.RegisterProtectedRoutes(mux, integrationValidator{}, handler.Routes())
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer allowed")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		return recorder
	}

	catalogs := get("/catalogs?code=" + url.QueryEscape(fixtures.activeCode) + "&is_active=true&page=1&page_size=1&sort=code:asc")
	if catalogs.Code != http.StatusOK {
		t.Fatalf("GET /catalogs got %d: %s", catalogs.Code, catalogs.Body.String())
	}
	var catalogResponse struct {
		Data       []map[string]any `json:"data"`
		Pagination struct {
			Page       int `json:"page"`
			PageSize   int `json:"page_size"`
			TotalItems int `json:"total_items"`
			TotalPages int `json:"total_pages"`
		} `json:"pagination"`
	}
	if err := json.NewDecoder(catalogs.Body).Decode(&catalogResponse); err != nil {
		t.Fatal(err)
	}
	if len(catalogResponse.Data) != 1 || catalogResponse.Pagination.Page != 1 || catalogResponse.Pagination.PageSize != 1 || catalogResponse.Pagination.TotalItems != 1 {
		t.Fatalf("unexpected catalog page: %+v", catalogResponse)
	}
	catalog := catalogResponse.Data[0]
	catalogID, _ := catalog["id"].(string)
	catalogCode, _ := catalog["code"].(string)
	if catalogID == "" || catalogCode == "" {
		t.Fatalf("catalog fixture is missing id or code: %+v", catalog)
	}

	for _, path := range []string{
		"/catalogs?code=" + url.QueryEscape(fixtures.activeCode),
		"/catalogs?is_active=true&code=" + url.QueryEscape(fixtures.activeCode),
		"/catalogs?code=" + url.QueryEscape(fixtures.activeCode) + "&is_active=true",
	} {
		recorder := get(path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}

	details := get("/catalogs/" + catalogID + "/details?page=1&page_size=20")
	if details.Code != http.StatusOK {
		t.Fatalf("GET catalog details got %d: %s", details.Code, details.Body.String())
	}
	var detailResponse struct {
		Data       []map[string]any `json:"data"`
		Pagination map[string]int   `json:"pagination"`
	}
	if err := json.NewDecoder(details.Body).Decode(&detailResponse); err != nil {
		t.Fatal(err)
	}
	if detailResponse.Pagination["page"] != 1 || detailResponse.Pagination["page_size"] != 20 {
		t.Fatalf("unexpected detail pagination: %+v", detailResponse.Pagination)
	}
	missingDetails := get("/catalogs/ffffffff-ffff-ffff-ffff-ffffffffffff/details")
	if missingDetails.Code != http.StatusNotFound {
		t.Fatalf("missing catalog details got %d: %s", missingDetails.Code, missingDetails.Body.String())
	}

	report := get("/reports/active-catalog?page=1&page_size=20")
	if report.Code != http.StatusOK {
		t.Fatalf("GET active catalog report got %d: %s", report.Code, report.Body.String())
	}
	var reportResponse struct {
		Data []struct {
			CatalogCode string `json:"catalog_code"`
			Details     []struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.NewDecoder(report.Body).Decode(&reportResponse); err != nil {
		t.Fatal(err)
	}
	var foundActive, foundActiveDetail, foundInactive bool
	for _, entry := range reportResponse.Data {
		var active bool
		if err := db.QueryRow(`SELECT is_active FROM parameterization.catalog WHERE code = $1`, entry.CatalogCode).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if !active {
			t.Fatalf("inactive catalog in report: %s", entry.CatalogCode)
		}
		if entry.CatalogCode == fixtures.activeCode {
			foundActive = true
			for _, detail := range entry.Details {
				if detail.Code == fixtures.activeDetailCode {
					foundActiveDetail = true
				}
				if detail.Code == fixtures.inactiveDetailCode {
					t.Fatalf("inactive detail in report: %s", detail.Code)
				}
			}
		}
		if entry.CatalogCode == fixtures.inactiveCode {
			foundInactive = true
		}
	}
	if !foundActive || !foundActiveDetail || foundInactive {
		t.Fatalf("report active filtering is incorrect: active=%t active_detail=%t inactive=%t", foundActive, foundActiveDetail, foundInactive)
	}
	filteredReport := get("/reports/active-catalog?catalog_code=" + url.QueryEscape(catalogCode))
	if filteredReport.Code != http.StatusOK {
		t.Fatalf("filtered report got %d: %s", filteredReport.Code, filteredReport.Body.String())
	}
	var filtered struct {
		Data []struct {
			CatalogCode string `json:"catalog_code"`
		} `json:"data"`
	}
	if err := json.NewDecoder(filteredReport.Body).Decode(&filtered); err != nil {
		t.Fatal(err)
	}
	if len(filtered.Data) != 1 || filtered.Data[0].CatalogCode != catalogCode {
		t.Fatalf("unexpected filtered report: %+v", filtered.Data)
	}
}

type catalogFixtures struct {
	activeCode         string
	inactiveCode       string
	activeDetailCode   string
	inactiveDetailCode string
	activeID           string
	inactiveID         string
	db                 *sql.DB
}

func createCatalogFixtures(t *testing.T, db *sql.DB) catalogFixtures {
	t.Helper()
	stamp := fmt.Sprintf("%x", time.Now().UnixNano())
	if len(stamp) < 16 {
		stamp = strings.Repeat("0", 16-len(stamp)) + stamp
	}
	activeCode := "IT005A" + stamp[:12]
	inactiveCode := "IT005I" + stamp[4:12]
	activeDetailCode := "ACTIVE_" + stamp[:8]
	inactiveDetailCode := "INACTIVE_" + stamp[8:16]
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), "SET LOCAL ROLE reference_writer"); err != nil {
		tx.Rollback()
		t.Fatalf("set fixture role: %v", err)
	}
	var activeID, inactiveID string
	if err := tx.QueryRowContext(context.Background(), `INSERT INTO parameterization.catalog (code, name, description, is_active) VALUES ($1, 'HU-REF-005 activo', 'fixture de integration test', true) RETURNING id`, activeCode).Scan(&activeID); err != nil {
		tx.Rollback()
		t.Fatalf("insert active catalog: %v", err)
	}
	if err := tx.QueryRowContext(context.Background(), `INSERT INTO parameterization.catalog (code, name, description, is_active) VALUES ($1, 'HU-REF-005 inactivo', 'fixture de integration test', false) RETURNING id`, inactiveCode).Scan(&inactiveID); err != nil {
		tx.Rollback()
		t.Fatalf("insert inactive catalog: %v", err)
	}
	for _, detail := range []struct {
		catalogID string
		code      string
		active    bool
	}{
		{activeID, activeDetailCode, true},
		{activeID, inactiveDetailCode, false},
		{inactiveID, "INACTIVE_PARENT_" + stamp, false},
	} {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO parameterization.catalog_detail (catalog_id, code, label, display_order, is_active) VALUES ($1, $2, $3, $4, $5)`, detail.catalogID, detail.code, detail.code, 1, detail.active); err != nil {
			tx.Rollback()
			t.Fatalf("insert catalog detail: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return catalogFixtures{activeCode: activeCode, inactiveCode: inactiveCode, activeDetailCode: activeDetailCode, inactiveDetailCode: inactiveDetailCode, activeID: activeID, inactiveID: inactiveID, db: db}
}

func (f catalogFixtures) cleanup() {
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		return
	}
	if _, err = tx.ExecContext(context.Background(), "SET LOCAL ROLE reference_writer"); err != nil {
		tx.Rollback()
		return
	}
	_, _ = tx.ExecContext(context.Background(), "DELETE FROM parameterization.catalog_detail WHERE catalog_id IN ($1, $2)", f.activeID, f.inactiveID)
	_, _ = tx.ExecContext(context.Background(), "DELETE FROM parameterization.catalog WHERE id IN ($1, $2)", f.activeID, f.inactiveID)
	_ = tx.Commit()
}
