package hierarchy

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"math/big"
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

// TestReferenceDataSecurityPostgres exercises the real JWT/JWKS middleware
// before a real reference-data PostgreSQL query. It is skipped only when the
// repository's documented integration DSN is not configured.
func TestReferenceDataSecurityPostgres(t *testing.T) {
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

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "integration",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()
	client, err := security.NewJWKSClient(jwks.URL, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := security.NewJWKSValidator("integration-issuer", client)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	handler := NewHandler(application.NewQueryService(postgres.NewRepository(db)))
	security.RegisterProtectedRoutes(mux, validator, handler.Routes())

	request := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/macroregions?page=1&page_size=1", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		return recorder
	}

	if response := request(""); response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"UNAUTHENTICATED"`) {
		t.Fatalf("missing token: %d %s", response.Code, response.Body.String())
	}
	if response := request(integrationJWT(t, key, []string{"REF_CATALOG_VIEW:GLOBAL"})); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"FORBIDDEN"`) {
		t.Fatalf("missing feature: %d %s", response.Code, response.Body.String())
	}
	if response := request(integrationJWT(t, key, []string{"REF_HIERARCHY_VIEW:GLOBAL"})); response.Code != http.StatusOK {
		t.Fatalf("authorized PostgreSQL request: %d %s", response.Code, response.Body.String())
	}
}

func integrationJWT(t *testing.T, key *rsa.PrivateKey, features []string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "integration", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": "integration-issuer", "sub": "integration-user",
		"exp": time.Now().Add(time.Minute).Unix(), "features": features,
	})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
