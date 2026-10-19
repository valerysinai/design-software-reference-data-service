package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequireFeatureWithRealJWTAndJWKS(t *testing.T) {
	key := testKey(t)
	server := jwksServer(t, &key.PublicKey, "current")
	defer server.Close()

	client, err := NewJWKSClient(server.URL, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewJWKSValidator("issuer", client)
	if err != nil {
		t.Fatal(err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.TrainingCenterID != "center-1" || len(principal.FeatureGrants) != 1 || principal.FeatureGrants[0].Scope != "GLOBAL" {
			t.Fatalf("principal claims were not preserved: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	tests := []struct {
		name      string
		claims    map[string]any
		status    int
		errorCode string
	}{
		{"missing token", nil, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"malformed bearer", map[string]any{"authorization": "Basic token"}, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"malformed jwt", map[string]any{"authorization": "Bearer a.b.c"}, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"expired", map[string]any{"token": true, "exp": time.Now().Add(-time.Minute).Unix()}, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"wrong issuer", map[string]any{"token": true, "iss": "other", "exp": time.Now().Add(time.Minute).Unix()}, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"missing feature", map[string]any{"token": true, "features": []string{"REF_CATALOG_VIEW:GLOBAL"}}, http.StatusForbidden, "FORBIDDEN"},
		{"authorized", map[string]any{"token": true, "features": []string{"REF_HIERARCHY_VIEW:GLOBAL"}}, http.StatusNoContent, ""},
		{"malformed grant", map[string]any{"token": true, "features": []string{"REF_HIERARCHY_VIEW"}}, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"unknown scope", map[string]any{"token": true, "features": []string{"REF_HIERARCHY_VIEW:UNKNOWN"}}, http.StatusUnauthorized, "UNAUTHENTICATED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/macroregions", nil)
			switch {
			case tc.claims == nil:
			case tc.claims["authorization"] != nil:
				req.Header.Set("Authorization", tc.claims["authorization"].(string))
			default:
				tokenClaims := map[string]any{
					"iss": "issuer", "sub": "user-1", "exp": time.Now().Add(time.Minute).Unix(),
					"training_center_id": "center-1", "features": []string{},
				}
				for key, value := range tc.claims {
					if key != "token" {
						tokenClaims[key] = value
					}
				}
				req.Header.Set("Authorization", "Bearer "+signedToken(t, key, "RS256", "current", tokenClaims))
			}
			recorder := httptest.NewRecorder()
			RequireFeature(validator, "REF_HIERARCHY_VIEW", next).ServeHTTP(recorder, req)
			if recorder.Code != tc.status {
				t.Fatalf("got status %d, want %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.errorCode != "" && !strings.Contains(recorder.Body.String(), `"code":"`+tc.errorCode+`"`) {
				t.Fatalf("missing error code in %s", recorder.Body.String())
			}
		})
	}
}

func TestJWKSValidatorRejectsInvalidSignatureAndUnknownKid(t *testing.T) {
	key := testKey(t)
	other := testKey(t)
	server := jwksServer(t, &key.PublicKey, "current")
	defer server.Close()
	client, _ := NewJWKSClient(server.URL, time.Second, time.Minute)
	validator, _ := NewJWKSValidator("issuer", client)
	claims := map[string]any{"iss": "issuer", "sub": "user-1", "exp": time.Now().Add(time.Minute).Unix()}

	if _, err := validator.Validate(context.Background(), signedToken(t, other, "RS256", "current", claims)); err == nil || !strings.Contains(err.Error(), ErrInvalid.Error()) {
		t.Fatalf("wrong signature accepted: %v", err)
	}
	if _, err := validator.Validate(context.Background(), signedToken(t, key, "RS256", "unknown", claims)); err == nil || !strings.Contains(err.Error(), ErrInvalid.Error()) {
		t.Fatalf("unknown kid accepted: %v", err)
	}
}
