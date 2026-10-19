package hierarchy

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestHURef019ParametersWithRealIAMJWT exercises the live IAM and reference-data
// HTTP services. Credentials are supplied by the environment and never stored
// in the repository.
func TestHURef019ParametersWithRealIAMJWT(t *testing.T) {
	config, ok := huRef019E2EConfig(t)
	if !ok {
		return
	}

	admin := iamLogin(t, config.iamBaseURL, config.adminEmail, config.adminPassword)
	learner := iamLogin(t, config.iamBaseURL, config.learnerEmail, config.learnerPassword)
	assertContains(t, admin.User.Roles, "ADMIN_STAFF")
	assertContains(t, admin.User.Features, "REF_PARAMETER_VIEW")
	assertContains(t, admin.User.Features, "REF_PARAMETER_MANAGE")
	assertJWTClaims(t, admin.AccessToken, config.issuer, "ADMIN_STAFF", "REF_PARAMETER_VIEW:GLOBAL", "REF_PARAMETER_MANAGE:GLOBAL")
	assertContains(t, learner.User.Roles, "LEARNER")
	assertNotContains(t, learner.User.Features, "REF_PARAMETER_VIEW")
	assertNotContains(t, learner.User.Features, "REF_PARAMETER_MANAGE")
	assertJWTClaims(t, learner.AccessToken, config.issuer, "LEARNER")

	db, err := sql.Open("pgx", config.dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup callbacks run in LIFO order. Registering the close callback before
	// the parameter cleanup keeps the connection available to remove the row
	// created by this test.
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}

	referenceDataURL := strings.TrimRight(config.referenceDataBaseURL, "/")

	key := fmt.Sprintf("HU_REF_019_%d", time.Now().UnixNano())
	var parameter struct {
		ID string `json:"id"`
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM parameterization.parameter WHERE key = $1`, key)
	})

	if status := parameterStatus(t, referenceDataURL, http.MethodGet, "/parameters?page=1&page_size=1", admin.AccessToken, nil); status != http.StatusOK {
		t.Fatalf("admin list parameters: got %d, want 200", status)
	}
	create := parameterRequest(t, referenceDataURL, http.MethodPost, "/parameters", admin.AccessToken, map[string]any{
		"key": key, "value": "48", "value_type": "integer", "description": "Fixture E2E HU-REF-019",
	})
	if create.StatusCode != http.StatusCreated {
		_ = create.Body.Close()
		t.Fatalf("admin create parameter: %s", create.Status)
	}
	defer create.Body.Close()
	if err := json.NewDecoder(create.Body).Decode(&parameter); err != nil || parameter.ID == "" {
		t.Fatalf("decode created parameter: id=%q err=%v", parameter.ID, err)
	}
	if status := parameterStatus(t, referenceDataURL, http.MethodPut, "/parameters/"+parameter.ID, admin.AccessToken, map[string]any{
		"value": "49", "value_type": "integer", "description": "Fixture E2E actualizada",
	}); status != http.StatusOK {
		t.Fatalf("admin update parameter: got %d, want 200", status)
	}

	for _, operation := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/parameters", nil},
		{http.MethodPost, "/parameters", map[string]any{"key": key + "_forbidden", "value": "x", "value_type": "string"}},
		{http.MethodPut, "/parameters/" + parameter.ID, map[string]any{"value": "50", "value_type": "integer"}},
	} {
		if status := parameterStatus(t, referenceDataURL, operation.method, operation.path, learner.AccessToken, operation.body); status != http.StatusForbidden {
			t.Fatalf("learner %s %s: got %d, want 403", operation.method, operation.path, status)
		}
	}

	for _, token := range []string{"", "not-a-jwt"} {
		if status := parameterStatus(t, referenceDataURL, http.MethodGet, "/parameters", token, nil); status != http.StatusUnauthorized {
			t.Fatalf("invalid or missing token: got %d, want 401", status)
		}
	}
}

type huRef019Config struct {
	dsn, iamBaseURL, referenceDataBaseURL, issuer string
	adminEmail, adminPassword                     string
	learnerEmail, learnerPassword                 string
}

func huRef019E2EConfig(t *testing.T) (huRef019Config, bool) {
	t.Helper()
	config := huRef019Config{
		dsn:                   os.Getenv("REFERENCE_DATA_TEST_DSN"),
		iamBaseURL:            strings.TrimRight(os.Getenv("HU_REF_019_IAM_BASE_URL"), "/"),
		referenceDataBaseURL:  strings.TrimRight(os.Getenv("HU_REF_019_REFERENCE_DATA_BASE_URL"), "/"),
		issuer:                os.Getenv("IAM_JWT_ISSUER"),
		adminEmail:            os.Getenv("HU_REF_019_ADMIN_EMAIL"),
		adminPassword:         os.Getenv("HU_REF_019_ADMIN_PASSWORD"),
		learnerEmail:          os.Getenv("HU_REF_019_LEARNER_EMAIL"),
		learnerPassword:       os.Getenv("HU_REF_019_LEARNER_PASSWORD"),
	}
	for _, value := range []string{config.dsn, config.iamBaseURL, config.referenceDataBaseURL, config.issuer, config.adminEmail, config.adminPassword, config.learnerEmail, config.learnerPassword} {
		if value == "" {
			t.Skip("HU-REF-019 E2E requires REFERENCE_DATA_TEST_DSN, IAM_JWT_ISSUER, HU_REF_019_IAM_BASE_URL, HU_REF_019_REFERENCE_DATA_BASE_URL and external IAM credentials")
			return huRef019Config{}, false
		}
	}
	return config, true
}

type iamLoginResult struct {
	AccessToken string `json:"access_token"`
	User        struct {
		Roles    []string `json:"roles"`
		Features []string `json:"features"`
	} `json:"user"`
}

func iamLogin(t *testing.T, iamBaseURL, email, password string) iamLoginResult {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, iamBaseURL+"/api/v1/auth/login", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("IAM login for %s: %s", email, response.Status)
	}
	var result iamLoginResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.AccessToken == "" {
		t.Fatalf("decode IAM login response: access token present=%t err=%v", result.AccessToken != "", err)
	}
	return result
}

func assertContains(t *testing.T, values []string, expected string) {
	t.Helper()
	for _, value := range values {
		if value == expected {
			return
		}
	}
	t.Fatalf("expected %q in %v", expected, values)
}

func assertNotContains(t *testing.T, values []string, unexpected string) {
	t.Helper()
	for _, value := range values {
		if value == unexpected {
			t.Fatalf("did not expect %q in %v", unexpected, values)
		}
	}
}

func assertJWTClaims(t *testing.T, token, issuer string, expectedRolesAndFeatures ...string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("IAM access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims struct {
		Issuer   string   `json:"iss"`
		Subject  string   `json:"sub"`
		Roles    []string `json:"roles"`
		Features []string `json:"features"`
		Expires  int64    `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("parse JWT claims: %v", err)
	}
	if claims.Issuer != issuer || claims.Subject == "" || claims.Expires <= time.Now().Unix() {
		t.Fatalf("unexpected JWT standard claims: issuer=%q subject-present=%t unexpired=%t", claims.Issuer, claims.Subject != "", claims.Expires > time.Now().Unix())
	}
	for _, expected := range expectedRolesAndFeatures {
		if strings.Contains(expected, ":") {
			assertContains(t, claims.Features, expected)
		} else {
			assertContains(t, claims.Roles, expected)
		}
	}
}

func parameterRequest(t *testing.T, baseURL, method, path, token string, body any) *http.Response {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func parameterStatus(t *testing.T, baseURL, method, path, token string, body any) int {
	t.Helper()
	response := parameterRequest(t, baseURL, method, path, token, body)
	defer response.Body.Close()
	return response.StatusCode
}
