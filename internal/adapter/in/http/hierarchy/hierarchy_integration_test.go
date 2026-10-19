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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/postgres"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// These tests intentionally use PostgreSQL, never a database mock. Set
// REFERENCE_DATA_TEST_DSN to an instance migrated with reference-data-db.
func TestHierarchyReadIntegration(t *testing.T) {
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

	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)))
	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, integrationValidator{}, handler.Routes())

	filterKeys := map[string][]string{
		"macroregions":        {"code", "is_active"},
		"microregions":        {"macroregion_id", "code", "is_active"},
		"departments":         {"microregion_id", "dane_code", "is_active"},
		"municipalities":      {"department_id", "dane_code", "is_active"},
		"training-centers":    {"municipality_id", "center_code", "is_active"},
		"institutional-units": {"training_center_id", "unit_type", "is_active"},
	}
	for _, resource := range []string{"macroregions", "microregions", "departments", "municipalities", "training-centers", "institutional-units"} {
		t.Run(resource+" list", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/"+resource+"?page=1&page_size=1&sort=id:asc", nil)
			req.Header.Set("Authorization", "Bearer allowed")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
			}
			var response struct {
				Data       []json.RawMessage `json:"data"`
				Pagination struct {
					Page       int `json:"page"`
					PageSize   int `json:"page_size"`
					TotalItems int `json:"total_items"`
					TotalPages int `json:"total_pages"`
				} `json:"pagination"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Pagination.Page != 1 || response.Pagination.PageSize != 1 || response.Pagination.TotalItems < len(response.Data) {
				t.Fatalf("invalid pagination: %+v", response.Pagination)
			}
			if len(response.Data) == 0 {
				t.Fatalf("%s demo fixture is missing", resource)
			}
			{
				var item map[string]any
				if err := json.Unmarshal(response.Data[0], &item); err != nil {
					t.Fatal(err)
				}
				id, _ := item["id"].(string)
				detail := httptest.NewRequest(http.MethodGet, "/"+resource+"/"+id, nil)
				detail.Header.Set("Authorization", "Bearer allowed")
				detailRecorder := httptest.NewRecorder()
				mux.ServeHTTP(detailRecorder, detail)
				if detailRecorder.Code != http.StatusOK {
					t.Fatalf("detail got %d: %s", detailRecorder.Code, detailRecorder.Body.String())
				}
				for _, key := range filterKeys[resource] {
					value := filterValue(t, item[key])
					filtered := httptest.NewRequest(http.MethodGet, "/"+resource+"?"+key+"="+url.QueryEscape(value), nil)
					filtered.Header.Set("Authorization", "Bearer allowed")
					filteredRecorder := httptest.NewRecorder()
					mux.ServeHTTP(filteredRecorder, filtered)
					if filteredRecorder.Code != http.StatusOK {
						t.Fatalf("%s filter %s got %d: %s", resource, key, filteredRecorder.Code, filteredRecorder.Body.String())
					}
					var filteredResponse struct {
						Data []map[string]any `json:"data"`
					}
					if err := json.NewDecoder(filteredRecorder.Body).Decode(&filteredResponse); err != nil {
						t.Fatal(err)
					}
					if len(filteredResponse.Data) == 0 || filterValue(t, filteredResponse.Data[0][key]) != value {
						t.Fatalf("%s filter %s did not restrict PostgreSQL results", resource, key)
					}
				}
				sorted := httptest.NewRequest(http.MethodGet, "/"+resource+"?page_size=100&sort=id:desc", nil)
				sorted.Header.Set("Authorization", "Bearer allowed")
				sortedRecorder := httptest.NewRecorder()
				mux.ServeHTTP(sortedRecorder, sorted)
				if sortedRecorder.Code != http.StatusOK {
					t.Fatalf("sort got %d: %s", sortedRecorder.Code, sortedRecorder.Body.String())
				}
				var sortedResponse struct {
					Data []map[string]any `json:"data"`
				}
				if err := json.NewDecoder(sortedRecorder.Body).Decode(&sortedResponse); err != nil {
					t.Fatal(err)
				}
				for i := 1; i < len(sortedResponse.Data); i++ {
					if filterValue(t, sortedResponse.Data[i-1]["id"]) < filterValue(t, sortedResponse.Data[i]["id"]) {
						t.Fatalf("%s sort=id:desc was not applied", resource)
					}
				}
			}
		})
	}

	for _, resource := range []string{"macroregions", "microregions", "departments", "municipalities", "training-centers", "institutional-units"} {
		req := httptest.NewRequest(http.MethodGet, "/"+resource+"/ffffffff-ffff-ffff-ffff-ffffffffffff", nil)
		req.Header.Set("Authorization", "Bearer allowed")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s missing got %d: %s", resource, rec.Code, rec.Body.String())
		}
	}

	// HU-REF-016: verify the official Liquibase demo seed is visible through
	// the PostgreSQL-backed endpoint and points to its demo training center.
	const demoUnitID = "a0000000-0000-0000-0000-000000000006"
	const demoTrainingCenterID = "a0000000-0000-0000-0000-000000000005"
	var centerName string
	if err := db.QueryRow(`SELECT tc.name
		FROM institutional_structure.institutional_unit iu
		JOIN institutional_structure.training_center tc ON tc.id = iu.training_center_id
		WHERE iu.id = $1 AND iu.training_center_id = $2`, demoUnitID, demoTrainingCenterID).Scan(&centerName); err != nil {
		t.Fatalf("HU-REF-016 demo unit/center seed is missing or mislinked: %v", err)
	}
	if centerName != "Centro de Comercio y Servicios" {
		t.Fatalf("HU-REF-016 demo unit points to center %q, want Centro de Comercio y Servicios", centerName)
	}

	demoUnitRequest := httptest.NewRequest(http.MethodGet, "/institutional-units/"+demoUnitID, nil)
	demoUnitRequest.Header.Set("Authorization", "Bearer allowed")
	demoUnitResponse := httptest.NewRecorder()
	mux.ServeHTTP(demoUnitResponse, demoUnitRequest)
	if demoUnitResponse.Code != http.StatusOK {
		t.Fatalf("HU-REF-016 demo unit endpoint got %d: %s", demoUnitResponse.Code, demoUnitResponse.Body.String())
	}
	var demoUnit map[string]any
	if err := json.NewDecoder(demoUnitResponse.Body).Decode(&demoUnit); err != nil {
		t.Fatalf("decode HU-REF-016 demo unit: %v", err)
	}
	if demoUnit["id"] != demoUnitID || demoUnit["training_center_id"] != demoTrainingCenterID ||
		demoUnit["name"] != "Coordinación Académica" || demoUnit["unit_type"] != "ACADEMIC" {
		t.Fatalf("HU-REF-016 endpoint returned unexpected demo unit: %+v", demoUnit)
	}

	for _, token := range []string{"denied", "learner"} {
		req := httptest.NewRequest(http.MethodGet, "/macroregions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s got %d", token, rec.Code)
		}
	}
}

// TestHierarchyWriteIntegration is intentionally PostgreSQL-only: it proves
// the named UNIQUE/FK constraints, reassignment and soft delete end-to-end.
func TestCatalogWriteIntegration(t *testing.T) {
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" {
		t.Skip("REFERENCE_DATA_TEST_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(); err != nil {
		t.Fatal(err)
	}

	publisher := &catalogEventPublisher{}
	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)), application.NewCommandService(postgres.NewRepository(db), application.WithEventPublisher(publisher)))
	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, integrationValidator{}, handler.Routes())
	seed := fmt.Sprintf("%016x", time.Now().UnixNano())
	uniqueSeed := seed[len(seed)-12:]
	catalogCodeA := "CATA" + uniqueSeed[:6]
	catalogCodeB := "CATB" + uniqueSeed[3:9]
	catalogCodeC := "CATC" + uniqueSeed[6:12]
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM parameterization.catalog_detail WHERE catalog_id IN (SELECT id FROM parameterization.catalog WHERE code IN ($1, $2, $3))`, catalogCodeA, catalogCodeB, catalogCodeC)
		_, _ = db.Exec(`DELETE FROM parameterization.catalog WHERE code IN ($1, $2, $3)`, catalogCodeA, catalogCodeB, catalogCodeC)
	})

	createCatalog := func(body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/catalogs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer catalog-managed")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("POST /catalogs got %d: %s", rec.Code, rec.Body.String())
		}
		var item map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &item)
		return item
	}
	createDetail := func(catalogID, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/catalogs/"+catalogID+"/details", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer catalog-managed")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("POST /catalogs/%s/details got %d: %s", catalogID, rec.Code, rec.Body.String())
		}
		var item map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &item)
		return item
	}
	updateDetail := func(catalogID, detailID, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/catalogs/"+catalogID+"/details/"+detailID, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer catalog-managed")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("PUT /catalogs/%s/details/%s got %d: %s", catalogID, detailID, rec.Code, rec.Body.String())
		}
	}
	deleteDetail := func(catalogID, detailID string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/catalogs/"+catalogID+"/details/"+detailID, nil)
		req.Header.Set("Authorization", "Bearer catalog-managed")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("DELETE /catalogs/%s/details/%s got %d: %s", catalogID, detailID, rec.Code, rec.Body.String())
		}
	}

	catalogA := createCatalog(fmt.Sprintf(`{"code":"%s","name":"Catálogo A","description":"desc a"}`, catalogCodeA), http.StatusCreated)
	if catalogA["code"] != catalogCodeA {
		t.Fatalf("created catalog code mismatch: %#v", catalogA)
	}
	createCatalog(fmt.Sprintf(`{"code":"%s","name":"Catálogo duplicado","description":"dup"}`, catalogCodeA), http.StatusConflict)

	catalogB := createCatalog(fmt.Sprintf(`{"code":"%s","name":"Catálogo B","description":"desc b"}`, catalogCodeB), http.StatusCreated)
	catalogAID := catalogA["id"].(string)
	catalogBID := catalogB["id"].(string)

	firstDetail := createDetail(catalogAID, `{"code":"VAL_A","label":"Valor A","display_order":10}`, http.StatusCreated)
	if firstDetail["label"] != "Valor A" {
		t.Fatalf("unexpected detail payload: %#v", firstDetail)
	}
	createDetail(catalogAID, `{"code":"VAL_A","label":"Duplicado","display_order":11}`, http.StatusConflict)
	createDetail(catalogBID, `{"code":"VAL_A","label":"Mismo código otro catálogo","display_order":7}`, http.StatusCreated)
	if len(publisher.events) != 4 {
		t.Fatalf("unexpected create event count: %#v", publisher.events)
	}
	if publisher.events[0].CatalogCode != catalogCodeA || publisher.events[0].ChangedItems[0].ItemID != catalogAID || publisher.events[0].ChangedItems[0].ChangeType != application.ChangeTypeAdded {
		t.Fatalf("unexpected catalog A create event payload: %#v", publisher.events[0])
	}
	if publisher.events[1].CatalogCode != catalogCodeB || publisher.events[1].ChangedItems[0].ItemID != catalogBID || publisher.events[1].ChangedItems[0].ChangeType != application.ChangeTypeAdded {
		t.Fatalf("unexpected catalog B create event payload: %#v", publisher.events[1])
	}
	if publisher.events[2].CatalogCode != catalogCodeA || publisher.events[2].ChangedItems[0].ChangeType != application.ChangeTypeAdded {
		t.Fatalf("unexpected first detail create event payload: %#v", publisher.events[2])
	}
	if publisher.events[3].CatalogCode != catalogCodeB || publisher.events[3].ChangedItems[0].ChangeType != application.ChangeTypeAdded {
		t.Fatalf("unexpected second detail create event payload: %#v", publisher.events[3])
	}

	updateDetail(catalogAID, firstDetail["id"].(string), `{"label":"Valor A actualizado","display_order":13}`, http.StatusOK)
	if len(publisher.events) != 5 || publisher.events[4].ChangedItems[0].ChangeType != application.ChangeTypeUpdated {
		t.Fatalf("unexpected update event payload: %#v", publisher.events)
	}

	deleteDetail(catalogAID, firstDetail["id"].(string), http.StatusNoContent)
	if len(publisher.events) != 6 {
		t.Fatalf("unexpected deactivate event payload: %#v", publisher.events)
	}
	deactivatedEvent := publisher.events[5]
	if deactivatedEvent.CatalogCode != catalogCodeA || deactivatedEvent.ChangedItems[0].ItemID != firstDetail["id"].(string) || deactivatedEvent.ChangedItems[0].Code != "VAL_A" || deactivatedEvent.ChangedItems[0].Label != "Valor A actualizado" || deactivatedEvent.ChangedItems[0].ChangeType != application.ChangeTypeDeactivated || deactivatedEvent.UpdatedBy != "admin-actor" || deactivatedEvent.UpdatedAt == "" {
		t.Fatalf("unexpected deactivate event payload: %#v", publisher.events)
	}

	badCatalogDetail := createDetail("ffffffff-ffff-ffff-ffff-ffffffffffff", `{"code":"FAIL","label":"No existe"}`, http.StatusNotFound)
	_ = badCatalogDetail
	wrongCatalogID := createCatalog(fmt.Sprintf(`{"code":"%s","name":"Catálogo C","description":"desc c"}`, catalogCodeC), http.StatusCreated)
	catalogCID := wrongCatalogID["id"].(string)
	otherDetail := createDetail(catalogCID, `{"code":"VAL_X","label":"Valor X"}`, http.StatusCreated)
	updateDetail(catalogAID, otherDetail["id"].(string), `{"label":"No pertenece"}`, http.StatusNotFound)

	var active bool
	if err := db.QueryRow(`SELECT is_active FROM parameterization.catalog WHERE id=$1`, catalogAID).Scan(&active); err != nil || !active {
		t.Fatalf("catalog unexpectedly deactivated while only a detail was deleted for id=%s active=%t err=%v", catalogAID, active, err)
	}
	var detailActive bool
	if err := db.QueryRow(`SELECT is_active FROM parameterization.catalog_detail WHERE id=$1`, firstDetail["id"].(string)).Scan(&detailActive); err != nil || detailActive {
		t.Fatalf("detail soft delete not persisted for id=%s active=%t err=%v", firstDetail["id"].(string), detailActive, err)
	}

	// catalog delete is a soft delete too, matching the service conventions.
	deleteReq := httptest.NewRequest(http.MethodDelete, "/catalogs/"+catalogAID, nil)
	deleteReq.Header.Set("Authorization", "Bearer catalog-managed")
	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /catalogs/%s got %d: %s", catalogAID, deleteRec.Code, deleteRec.Body.String())
	}
	if err := db.QueryRow(`SELECT is_active FROM parameterization.catalog WHERE id=$1`, catalogAID).Scan(&active); err != nil || active {
		t.Fatalf("catalog soft delete failed: active=%t err=%v", active, err)
	}
}

func TestHierarchyWriteIntegration(t *testing.T) {
	dsn := os.Getenv("REFERENCE_DATA_TEST_DSN")
	if dsn == "" {
		t.Skip("REFERENCE_DATA_TEST_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(); err != nil {
		t.Fatal(err)
	}
	publisher := &capturingPublisher{}
	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)), application.NewCommandService(postgres.NewRepository(db), application.WithEventPublisher(publisher)))
	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, integrationValidator{}, handler.Routes())
	seed := fmt.Sprintf("%016x", time.Now().UnixNano())
	stamp := seed[len(seed)-12:]
	codeA, codeB := "M"+stamp[:6], "N"+stamp[6:12]
	municipalityCode := stamp[:8]
	departmentCodeA, departmentCodeB, departmentCodeC := "A"+stamp[8:12], "B"+stamp[4:8], "C"+stamp[8:12]
	centerCode := "C" + stamp[5:12]
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM institutional_structure.institutional_unit WHERE training_center_id IN (SELECT id FROM institutional_structure.training_center WHERE center_code IN ($1, $2))`, centerCode, "D"+stamp)
		_, _ = db.Exec(`DELETE FROM institutional_structure.training_center WHERE center_code IN ($1, $2)`, centerCode, "D"+stamp)
		_, _ = db.Exec(`DELETE FROM institutional_structure.municipality WHERE dane_code IN ($1, $2)`, municipalityCode, "X"+municipalityCode[1:])
		_, _ = db.Exec(`DELETE FROM institutional_structure.department WHERE dane_code IN ($1, $2, $3)`, departmentCodeA, departmentCodeB, departmentCodeC)
		_, _ = db.Exec(`DELETE FROM institutional_structure.microregion WHERE code IN ($1, $2)`, "MIC"+stamp[:6], "MIB"+stamp[:6])
		_, _ = db.Exec(`DELETE FROM institutional_structure.macroregion WHERE code IN ($1, $2)`, codeA, codeB)
	})
	post := func(path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer managed")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("POST %s got %d: %s", path, rec.Code, rec.Body.String())
		}
		var item map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &item)
		return item
	}
	put := func(path, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer managed")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("PUT %s got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	macroA := post("/macroregions", fmt.Sprintf(`{"name":"Macro A","code":"%s"}`, codeA), http.StatusCreated)
	macroB := post("/macroregions", fmt.Sprintf(`{"name":"Macro B","code":"%s"}`, codeB), http.StatusCreated)
	post("/macroregions", fmt.Sprintf(`{"name":"Duplicada","code":"%s"}`, codeA), http.StatusConflict)
	macroAID, macroBID := macroA["id"].(string), macroB["id"].(string)
	put("/macroregions/"+macroAID, fmt.Sprintf(`{"name":"Macro A","code":"%s"}`, codeB), http.StatusConflict)
	put("/macroregions/"+macroAID, fmt.Sprintf(`{"name":"Macro A actualizada","code":"%s"}`, codeA), http.StatusOK)
	microA := post("/microregions", fmt.Sprintf(`{"name":"Micro A","code":"MIC%s","macroregion_id":"%s"}`, stamp[:6], macroAID), http.StatusCreated)
	post("/microregions", fmt.Sprintf(`{"name":"Sin padre","code":"BAD%s","macroregion_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, stamp[:6]), http.StatusNotFound)
	post("/microregions", fmt.Sprintf(`{"name":"Duplicada","code":"MIC%s","macroregion_id":"%s"}`, stamp[:6], macroAID), http.StatusConflict)
	put("/microregions/"+microA["id"].(string), fmt.Sprintf(`{"name":"Micro A","code":"MIC%s","macroregion_id":"%s"}`, stamp[:6], macroBID), http.StatusOK)
	post("/microregions", fmt.Sprintf(`{"name":"Mismo código, otro padre","code":"MIC%s","macroregion_id":"%s"}`, stamp[:6], macroAID), http.StatusCreated)
	microB := post("/microregions", fmt.Sprintf(`{"name":"Micro B","code":"MIB%s","macroregion_id":"%s"}`, stamp[:6], macroBID), http.StatusCreated)
	put("/microregions/"+microA["id"].(string), `{"macroregion_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, http.StatusNotFound)
	put("/microregions/"+microA["id"].(string), fmt.Sprintf(`{"code":"MIB%s"}`, stamp[:6]), http.StatusConflict)
	departmentA := post("/departments", fmt.Sprintf(`{"name":"Departamento A","dane_code":"%s","microregion_id":"%s"}`, departmentCodeA, microA["id"].(string)), http.StatusCreated)
	post("/departments", fmt.Sprintf(`{"name":"Departamento A","dane_code":"%s","microregion_id":"%s"}`, departmentCodeB, microA["id"].(string)), http.StatusConflict)
	post("/departments", fmt.Sprintf(`{"name":"Sin padre","dane_code":"%s","microregion_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, departmentCodeC), http.StatusNotFound)
	post("/departments", fmt.Sprintf(`{"name":"Otro","dane_code":"%s","microregion_id":"%s"}`, departmentCodeA, microB["id"].(string)), http.StatusConflict)
	departmentB := post("/departments", fmt.Sprintf(`{"name":"Departamento B","dane_code":"%s","microregion_id":"%s"}`, departmentCodeB, microB["id"].(string)), http.StatusCreated)
	post("/departments", fmt.Sprintf(`{"name":"Departamento A","dane_code":"%s","microregion_id":"%s"}`, departmentCodeC, microB["id"].(string)), http.StatusCreated)
	put("/departments/"+departmentA["id"].(string), fmt.Sprintf(`{"name":"Departamento A actualizado","dane_code":"%s","microregion_id":"%s"}`, departmentCodeA, microA["id"].(string)), http.StatusOK)
	put("/departments/"+departmentA["id"].(string), fmt.Sprintf(`{"name":"Departamento A","microregion_id":"%s"}`, microB["id"]), http.StatusConflict)
	put("/departments/"+departmentA["id"].(string), fmt.Sprintf(`{"name":"Departamento A movido","microregion_id":"%s"}`, microB["id"]), http.StatusOK)
	put("/departments/"+departmentA["id"].(string), `{"microregion_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, http.StatusNotFound)
	municipality := post("/municipalities", fmt.Sprintf(`{"name":"Municipio A","dane_code":"%s","department_id":"%s"}`, municipalityCode, departmentA["id"].(string)), http.StatusCreated)
	post("/municipalities", fmt.Sprintf(`{"name":"Municipio A","dane_code":"%s","department_id":"%s"}`, "Z"+municipalityCode[1:], departmentA["id"].(string)), http.StatusConflict)
	post("/municipalities", fmt.Sprintf(`{"name":"Municipio A","dane_code":"%s","department_id":"%s"}`, "X"+municipalityCode[1:], departmentB["id"].(string)), http.StatusCreated)
	post("/municipalities", fmt.Sprintf(`{"name":"Sin padre","dane_code":"%s","department_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, "Y"+municipalityCode[1:]), http.StatusNotFound)
	put("/municipalities/"+municipality["id"].(string), fmt.Sprintf(`{"name":"Municipio A","department_id":"%s"}`, departmentB["id"]), http.StatusConflict)
	put("/municipalities/"+municipality["id"].(string), fmt.Sprintf(`{"name":"Municipio A movido","department_id":"%s"}`, departmentB["id"]), http.StatusOK)
	put("/municipalities/"+municipality["id"].(string), `{"department_id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`, http.StatusNotFound)
	center := post("/training-centers", fmt.Sprintf(`{"name":"Centro A","center_code":"%s","municipality_id":"%s","address":"Direccion","phone":"3000000000"}`, centerCode, municipality["id"].(string)), http.StatusCreated)
	post("/training-centers", fmt.Sprintf(`{"name":"Duplicado","center_code":"%s","municipality_id":"%s"}`, centerCode, municipality["id"].(string)), http.StatusConflict)
	centerID := center["id"].(string)
	put("/training-centers/"+centerID, `{"name":"Centro A actualizado"}`, http.StatusOK)
	put("/training-centers/"+centerID, fmt.Sprintf(`{"name":"Centro A","center_code":"%s"}`, centerCode), http.StatusOK)
	put("/training-centers/"+centerID, fmt.Sprintf(`{"center_code":"D%s"}`, stamp), http.StatusBadRequest)
	var persistedCode string
	if err := db.QueryRow(`SELECT center_code FROM institutional_structure.training_center WHERE id=$1`, centerID).Scan(&persistedCode); err != nil || persistedCode != centerCode {
		t.Fatalf("center_code changed after rejected update: code=%s err=%v", persistedCode, err)
	}
	if len(publisher.events) != 1 || publisher.events[0].TrainingCenterID != centerID || publisher.events[0].CenterCode != centerCode || publisher.events[0].Name != "Centro A" || publisher.events[0].MunicipalityID != municipality["id"].(string) {
		t.Fatalf("unexpected training center event: %+v", publisher.events)
	}
	unit := post("/institutional-units", fmt.Sprintf(`{"name":"Unidad Academica","training_center_id":"%s","unit_type":"ACADEMIC"}`, centerID), http.StatusCreated)
	post("/institutional-units", `{"name":"Sin centro","training_center_id":"ffffffff-ffff-ffff-ffff-ffffffffffff","unit_type":"MIXED"}`, http.StatusNotFound)
	post("/institutional-units", fmt.Sprintf(`{"name":"Unidad Administrativa","training_center_id":"%s","unit_type":"ADMINISTRATIVE"}`, centerID), http.StatusCreated)
	post("/institutional-units", fmt.Sprintf(`{"name":"Unidad Mixta","training_center_id":"%s","unit_type":"MIXED"}`, centerID), http.StatusCreated)
	post("/institutional-units", fmt.Sprintf(`{"name":"Unidad Invalida","training_center_id":"%s","unit_type":"INVALID"}`, centerID), http.StatusBadRequest)
	// Each delete retains its row and only changes its active flag.
	deleteAndAssert := func(path, table, id string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path+id, nil)
		req.Header.Set("Authorization", "Bearer managed")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s got %d: %s", path, rec.Code, rec.Body.String())
		}
		var active bool
		if err := db.QueryRow(`SELECT is_active FROM `+table+` WHERE id=$1`, id).Scan(&active); err != nil || active {
			t.Fatalf("soft delete %s: active=%t err=%v", table, active, err)
		}
	}
	deleteAndAssert("/institutional-units/", "institutional_structure.institutional_unit", unit["id"].(string))
	deleteAndAssert("/training-centers/", "institutional_structure.training_center", centerID)
	deleteAndAssert("/municipalities/", "institutional_structure.municipality", municipality["id"].(string))
	deleteAndAssert("/departments/", "institutional_structure.department", departmentA["id"].(string))
	deleteAndAssert("/microregions/", "institutional_structure.microregion", microA["id"].(string))
	deleteAndAssert("/macroregions/", "institutional_structure.macroregion", macroAID)
	// The request middleware, not roles in a handler, enforces the manage feature.
	req := httptest.NewRequest(http.MethodPost, "/macroregions", strings.NewReader(`{"name":"Denied","code":"DENIED"}`))
	req.Header.Set("Authorization", "Bearer denied")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing manage feature got %d", rec.Code)
	}
}

func filterValue(t *testing.T, value any) string {
	t.Helper()
	switch value := value.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	default:
		t.Fatalf("unsupported filter value %T", value)
		return ""
	}
}

type integrationValidator struct{}

func (integrationValidator) Validate(_ context.Context, token string) (security.Principal, error) {
	if token == "allowed" {
		return security.Principal{Features: []string{"REF_HIERARCHY_VIEW:GLOBAL", "REF_CATALOG_VIEW:GLOBAL"}}, nil
	}
	if token == "managed" {
		return security.Principal{ActorID: "admin-actor", Features: []string{"REF_HIERARCHY_MANAGE:GLOBAL"}}, nil
	}
	if token == "catalog-managed" {
		return security.Principal{ActorID: "admin-actor", Features: []string{"REF_CATALOG_MANAGE:GLOBAL"}}, nil
	}
	return security.Principal{Roles: []string{"LEARNER"}}, nil
}

type capturingPublisher struct {
	events []application.TrainingCenterCreated
}

type catalogEventPublisher struct {
	events []application.CatalogUpdated
}

func (p *capturingPublisher) PublishTrainingCenterCreated(_ context.Context, event application.TrainingCenterCreated) error {
	p.events = append(p.events, event)
	return nil
}

func (p *capturingPublisher) PublishCatalogUpdated(_ context.Context, _ application.CatalogUpdated) error {
	return nil
}

func (p *catalogEventPublisher) PublishTrainingCenterCreated(_ context.Context, _ application.TrainingCenterCreated) error {
	return nil
}

func (p *catalogEventPublisher) PublishCatalogUpdated(_ context.Context, event application.CatalogUpdated) error {
	p.events = append(p.events, event)
	return nil
}
