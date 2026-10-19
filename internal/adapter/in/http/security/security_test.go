package security

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWKSValidatorRejectsInvalidTokensAndAcceptsIAMClaims(t *testing.T) {
	key := testKey(t)
	server := jwksServer(t, &key.PublicKey, "current")
	client, err := NewJWKSClient(server.URL, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewJWKSValidator("sena-iam-service", client)
	if err != nil {
		t.Fatal(err)
	}

	valid := signedToken(t, key, "RS256", "current", map[string]any{
		"iss": "sena-iam-service", "sub": "user-1", "exp": time.Now().Add(time.Minute).Unix(),
		"roles": []string{"LEARNER"}, "features": []string{"REF_CATALOG_VIEW:GLOBAL"},
	})
	principal, err := validator.Validate(context.Background(), valid)
	if err != nil || principal.Subject != "user-1" || !HasFeature(principal, "REF_CATALOG_VIEW") {
		t.Fatalf("valid token rejected: %v", err)
	}

	cases := []struct {
		name, token string
		want        error
	}{
		{"malformed", "not-a-jwt", ErrInvalid},
		{"wrong algorithm", signedToken(t, key, "HS256", "current", map[string]any{"iss": "sena-iam-service", "sub": "user", "exp": time.Now().Add(time.Minute).Unix()}), ErrInvalid},
		{"expired", signedToken(t, key, "RS256", "current", map[string]any{"iss": "sena-iam-service", "sub": "user", "exp": time.Now().Add(-time.Minute).Unix()}), ErrExpired},
		{"wrong issuer", signedToken(t, key, "RS256", "current", map[string]any{"iss": "other", "sub": "user", "exp": time.Now().Add(time.Minute).Unix()}), ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validator.Validate(context.Background(), tc.token); !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	other := testKey(t)
	badSignature := signedToken(t, other, "RS256", "current", map[string]any{"iss": "sena-iam-service", "sub": "user", "exp": time.Now().Add(time.Minute).Unix()})
	if _, err := validator.Validate(context.Background(), badSignature); !strings.Contains(err.Error(), ErrInvalid.Error()) {
		t.Fatalf("wrong signature accepted: %v", err)
	}
}

func TestRequireFeatureUsesSharedEnvelopeAndSeparates401From403(t *testing.T) {
	validator := tokenValidatorFunc(func(_ context.Context, raw string) (Principal, error) {
		if raw == "bad" {
			return Principal{}, ErrInvalid
		}
		return Principal{Subject: "user", Features: []string{"REF_CATALOG_VIEW:GLOBAL"}}, nil
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFromContext(r.Context()); !ok {
			t.Error("principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	})

	for _, tc := range []struct {
		name, authorization, feature string
		status                       int
		code                         string
	}{
		{"missing", "", "REF_CATALOG_VIEW", http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"malformed", "Basic abc", "REF_CATALOG_VIEW", http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"invalid", "Bearer bad", "REF_CATALOG_VIEW", http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"forbidden", "Bearer good", "REF_PARAMETER_VIEW", http.StatusForbidden, "FORBIDDEN"},
		{"allowed", "Bearer good", "REF_CATALOG_VIEW", http.StatusNoContent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/catalogs", nil)
			req.Header.Set("Authorization", tc.authorization)
			req.Header.Set("X-Request-Id", "trace-123")
			recorder := httptest.NewRecorder()
			RequireFeature(validator, tc.feature, next).ServeHTTP(recorder, req)
			if recorder.Code != tc.status {
				t.Fatalf("got status %d", recorder.Code)
			}
			if tc.code != "" && (!strings.Contains(recorder.Body.String(), `"code":"`+tc.code+`"`) || !strings.Contains(recorder.Body.String(), `"trace_id":"trace-123"`)) {
				t.Fatalf("unexpected envelope %s", recorder.Body.String())
			}
		})
	}
}

type tokenValidatorFunc func(context.Context, string) (Principal, error)

func (f tokenValidatorFunc) Validate(ctx context.Context, raw string) (Principal, error) {
	return f(ctx, raw)
}

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func jwksServer(t *testing.T, key *rsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": b64Int(key.N), "e": b64Int(big.NewInt(int64(key.E)))}}})
	}))
}
func b64Int(value *big.Int) string { return base64.RawURLEncoding.EncodeToString(value.Bytes()) }
func signedToken(t *testing.T, key *rsa.PrivateKey, algorithm, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": algorithm, "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
