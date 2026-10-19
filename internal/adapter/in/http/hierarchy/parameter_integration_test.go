package hierarchy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/postgres"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestParameterIntegration(t *testing.T) {
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

	key := fmt.Sprintf("HU_REF_007_%d", time.Now().UnixNano())
	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)), application.NewCommandService(postgres.NewRepository(db)))
	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, parameterIntegrationValidator{}, handler.Routes())
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM parameterization.parameter WHERE key = $1`, key)
	})

	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	created := request(http.MethodPost, "/parameters", "manage", fmt.Sprintf(`{"key":"%s","value":"48","value_type":"integer","description":"Horas semanales"}`, key))
	if created.Code != http.StatusCreated {
		t.Fatalf("create got %d: %s", created.Code, created.Body.String())
	}
	var parameter struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		Value     string `json:"value"`
		ValueType string `json:"value_type"`
	}
	if err := json.NewDecoder(created.Body).Decode(&parameter); err != nil {
		t.Fatal(err)
	}
	if parameter.ID == "" || parameter.Key != key || parameter.Value != "48" || parameter.ValueType != "integer" {
		t.Fatalf("unexpected created parameter: %+v", parameter)
	}

	duplicate := request(http.MethodPost, "/parameters", "manage", fmt.Sprintf(`{"key":"%s","value":"49","value_type":"integer"}`, key))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate got %d: %s", duplicate.Code, duplicate.Body.String())
	}

	invalid := request(http.MethodPost, "/parameters", "manage", fmt.Sprintf(`{"key":"%s_invalid","value":"not-an-integer","value_type":"integer"}`, key))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid value got %d: %s", invalid.Code, invalid.Body.String())
	}

	list := request(http.MethodGet, "/parameters?key="+key, "view", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list got %d: %s", list.Code, list.Body.String())
	}
	var page struct {
		Data []struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(list.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].Key != key {
		t.Fatalf("unexpected parameter page: %+v", page.Data)
	}

	get := request(http.MethodGet, "/parameters/"+parameter.ID, "view", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get got %d: %s", get.Code, get.Body.String())
	}

	updated := request(http.MethodPut, "/parameters/"+parameter.ID, "manage", `{"value":"true","value_type":"boolean","description":"Bandera actualizada"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update got %d: %s", updated.Code, updated.Body.String())
	}
	if !strings.Contains(updated.Body.String(), `"value":"true"`) || !strings.Contains(updated.Body.String(), `"value_type":"boolean"`) {
		t.Fatalf("unexpected updated parameter: %s", updated.Body.String())
	}

	invalidUpdate := request(http.MethodPut, "/parameters/"+parameter.ID, "manage", `{"value":"not-json","value_type":"json"}`)
	if invalidUpdate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid update got %d: %s", invalidUpdate.Code, invalidUpdate.Body.String())
	}

	for _, method := range []string{http.MethodDelete} {
		unsupported := request(method, "/parameters/"+parameter.ID, "manage", "")
		if unsupported.Code != http.StatusMethodNotAllowed {
			t.Fatalf("delete got %d, want 405: %s", unsupported.Code, unsupported.Body.String())
		}
	}

	for _, operation := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/parameters", ""},
		{http.MethodPost, "/parameters", `{"key":"x","value":"x","value_type":"string"}`},
		{http.MethodPut, "/parameters/" + parameter.ID, `{"value":"x"}`},
	} {
		for _, token := range []string{"learner", ""} {
			rec := request(operation.method, operation.path, token, operation.body)
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with token %q got %d: %s", operation.method, operation.path, token, rec.Code, rec.Body.String())
			}
		}
	}
}

type parameterIntegrationValidator struct{}

func (parameterIntegrationValidator) Validate(_ context.Context, token string) (security.Principal, error) {
	switch token {
	case "view":
		return security.Principal{Features: []string{"REF_PARAMETER_VIEW:GLOBAL"}}, nil
	case "manage":
		return security.Principal{ActorID: "admin-actor", Features: []string{"REF_PARAMETER_MANAGE:GLOBAL"}}, nil
	default:
		return security.Principal{Roles: []string{"LEARNER"}}, nil
	}
}
