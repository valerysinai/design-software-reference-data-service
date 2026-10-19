package security

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidatorRejectsMissingClaimsAndFutureNBF(t *testing.T) {
	key := testKey(t)
	server := jwksServer(t, &key.PublicKey, "key-a")
	client, _ := NewJWKSClient(server.URL, time.Second, time.Minute)
	validator, _ := NewJWKSValidator("issuer", client)
	for _, claims := range []map[string]any{
		{"iss": "issuer", "sub": "user"},
		{"iss": "issuer", "exp": time.Now().Add(time.Minute).Unix()},
		{"iss": "issuer", "sub": "user", "exp": time.Now().Add(time.Minute).Unix(), "nbf": time.Now().Add(time.Minute).Unix()},
	} {
		token := signedToken(t, key, "RS256", "key-a", claims)
		if _, err := validator.Validate(context.Background(), token); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}
	noKid := signedToken(t, key, "RS256", "", map[string]any{"iss": "issuer", "sub": "user", "exp": time.Now().Add(time.Minute).Unix()})
	if _, err := validator.Validate(context.Background(), noKid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing kid: %v", err)
	}
}

func TestFeatureMatchingRequiresExactCodeAndKnownScope(t *testing.T) {
	for _, tc := range []struct {
		grant   string
		allowed bool
	}{
		{"REF_CATALOG_VIEW:GLOBAL", true}, {"REF_CATALOG_VIEW:TRAINING_CENTER", true}, {"REF_CATALOG_VIEW:AREA", true},
		{"REF_CATALOG_VIEW_OTHER:GLOBAL", false}, {"REF_CATALOG_VIEW:", false}, {"REF_CATALOG_VIEW:INVALID_SCOPE", false},
	} {
		if got := HasFeature(Principal{Features: []string{tc.grant}}, "REF_CATALOG_VIEW"); got != tc.allowed {
			t.Fatalf("%q: got %t", tc.grant, got)
		}
	}
	if HasFeature(Principal{}, "REF_CATALOG_VIEW") {
		t.Fatal("missing features allowed")
	}
}

func TestJWKSRefreshRotationCacheAndUnknownKidCooldown(t *testing.T) {
	keyA, keyB := testKey(t), testKey(t)
	var calls atomic.Int32
	var current atomic.Pointer[rsaPublic]
	current.Store(&rsaPublic{key: &keyA.PublicKey, kid: "a"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		p := current.Load()
		writeJWKS(w, p.key, p.kid)
	}))
	defer server.Close()
	client, err := NewJWKSClientWithPolicy(server.URL, time.Second, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Key(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Key(context.Background(), "a"); err != nil || calls.Load() != 1 {
		t.Fatalf("known key cache failed: %d %v", calls.Load(), err)
	}
	current.Store(&rsaPublic{key: &keyB.PublicKey, kid: "b"})
	if _, err := client.Key(context.Background(), "b"); err != nil || calls.Load() != 2 {
		t.Fatalf("rotation refresh failed: %d %v", calls.Load(), err)
	}
	before := calls.Load()
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() { defer group.Done(); _, _ = client.Key(context.Background(), "unknown") }()
	}
	group.Wait()
	if calls.Load() != before+1 {
		t.Fatalf("concurrent unknown kid did not single-flight: %d -> %d", before, calls.Load())
	}
	before = calls.Load()
	if _, err := client.Key(context.Background(), "unknown"); !errors.Is(err, errKeyNotFound) || calls.Load() != before {
		t.Fatalf("unknown kid cooldown failed: %v, %d -> %d", err, before, calls.Load())
	}
}

func TestJWKSInfrastructureErrorsAreUnavailableAndReturn503(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
	}{
		{"http-500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, time.Second},
		{"invalid-json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }, time.Second},
		{"timeout", func(w http.ResponseWriter, _ *http.Request) { time.Sleep(50 * time.Millisecond) }, time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			client, _ := NewJWKSClient(server.URL, tc.timeout, time.Minute)
			validator, _ := NewJWKSValidator("issuer", client)
			key := testKey(t)
			token := signedToken(t, key, "RS256", "missing", map[string]any{"iss": "issuer", "sub": "user", "exp": time.Now().Add(time.Minute).Unix()})
			if _, err := validator.Validate(context.Background(), token); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			RequireFeature(validator, "REF_CATALOG_VIEW", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler called") })).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusServiceUnavailable || !contains(recorder.Body.String(), "DEPENDENCY_UNAVAILABLE") {
				t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestJWKSURLPolicyAndTraceID(t *testing.T) {
	if _, err := NewJWKSClient("http://iam-service/jwks", time.Second, time.Minute); err == nil {
		t.Fatal("non-local HTTP accepted")
	}
	if _, err := NewJWKSClient("https://iam-service/jwks", time.Second, time.Minute); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	writeError(recorder, request, http.StatusUnauthorized, "UNAUTHENTICATED", "x")
	var envelope struct {
		Error struct {
			TraceID string `json:"trace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || !isUUIDv4(envelope.Error.TraceID) {
		t.Fatalf("invalid trace envelope: %s", recorder.Body.String())
	}
}

func isUUIDv4(value string) bool {
	return len(value) == 36 && value[8] == '-' && value[13] == '-' && value[14] == '4' &&
		value[18] == '-' && value[23] == '-' && strings.ContainsRune("89ab", rune(value[19]))
}

type rsaPublic struct {
	key *rsa.PublicKey
	kid string
}

func writeJWKS(w http.ResponseWriter, key *rsa.PublicKey, kid string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": b64Int(key.N), "e": b64Int(big.NewInt(int64(key.E)))}}})
}
func contains(value, part string) bool { return strings.Contains(value, part) }
