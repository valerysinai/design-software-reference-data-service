package hierarchy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
)

func TestEveryHierarchyRouteRejectsMissingFeatureAndLearner(t *testing.T) {
	mux := http.NewServeMux()
	security.RegisterProtectedRoutes(mux, hierarchyTokenValidator{}, NewHandler(nil).Routes())
	for _, path := range []string{
		"/macroregions", "/macroregions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/microregions", "/microregions/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/departments", "/departments/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/municipalities", "/municipalities/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/training-centers", "/training-centers/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/institutional-units", "/institutional-units/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/catalogs", "/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/details", "/reports/active-catalog",
	} {
		for _, token := range []string{"missing-feature", "learner"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s (%s): got %d, want 403", path, token, rec.Code)
			}
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, base := range []string{"/macroregions", "/training-centers", "/institutional-units"} {
			path := base
			if method != http.MethodPost {
				path += "/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			}
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Authorization", "Bearer missing-feature")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s: got %d, want 403", method, path, rec.Code)
			}
		}
	}
}

func TestCatalogRoutesRequireCatalogFeatureWithoutChangingHierarchyRBAC(t *testing.T) {
	mux := http.NewServeMux()
	queries := application.NewQueryService(routeReader{})
	security.RegisterProtectedRoutes(mux, hierarchyTokenValidator{}, NewHandler(queries).Routes())

	for _, path := range []string{"/catalogs", "/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/details", "/reports/active-catalog"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer catalog-view")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
			t.Fatalf("%s with catalog feature got %d: %s", path, rec.Code, rec.Body.String())
		}

		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer hierarchy-view")
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s with hierarchy-only feature got %d, want 403", path, rec.Code)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/catalogs", "/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/details", "/catalogs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/details/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"} {
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Authorization", "Bearer hierarchy-view")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s with hierarchy-only feature got %d, want 403", method, path, rec.Code)
			}
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/macroregions", nil)
	req.Header.Set("Authorization", "Bearer hierarchy-view")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("hierarchy route with hierarchy feature got %d: %s", rec.Code, rec.Body.String())
	}
}

type routeReader struct{}

func (routeReader) List(context.Context, application.ListQuery) (application.Page, error) {
	return application.Page{Data: []json.RawMessage{}}, nil
}
func (routeReader) Get(context.Context, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`), nil
}
func (routeReader) ListCatalogDetails(context.Context, string, application.ListQuery) (application.Page, error) {
	return application.Page{Data: []json.RawMessage{}}, nil
}
func (routeReader) ReportActiveCatalog(context.Context, string, int, int) ([]application.ActiveCatalogReportEntry, int, error) {
	return []application.ActiveCatalogReportEntry{}, 0, nil
}

type hierarchyTokenValidator struct{}

func (hierarchyTokenValidator) Validate(_ context.Context, token string) (security.Principal, error) {
	switch token {
	case "catalog-view":
		return security.Principal{Features: []string{"REF_CATALOG_VIEW:GLOBAL"}}, nil
	case "catalog-manage":
		return security.Principal{Features: []string{"REF_CATALOG_MANAGE:GLOBAL"}}, nil
	case "hierarchy-view":
		return security.Principal{Features: []string{"REF_HIERARCHY_VIEW:GLOBAL"}}, nil
	default:
		return security.Principal{Roles: []string{token}}, nil
	}
}
