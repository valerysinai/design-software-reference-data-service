// Package security contains the HTTP boundary authentication and authorization primitives.
package security

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/platform/correlation"
)

const traceHeader = "X-Request-Id"

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return correlation.WithID(ctx, id)
}

func CorrelationID(ctx context.Context) (string, bool) {
	return correlation.ID(ctx)
}

// RequestCorrelationID returns the correlation identifier already resolved for
// the request by the observability middleware, which owns that decision. HTTP
// adapters must use this instead of re-reading X-Request-Id, otherwise the
// response header, the error envelope and the published events can carry
// different identifiers when the caller sends no header. The header and UUID
// fallbacks only cover handlers exercised without the middleware chain.
func RequestCorrelationID(r *http.Request) string {
	if id, ok := correlation.ID(r.Context()); ok {
		return id
	}
	if id := strings.TrimSpace(r.Header.Get(traceHeader)); id != "" {
		return id
	}
	return correlation.NewID()
}

// Correlation publishes the request correlation identifier on the response and
// keeps it available in the context. It never overrides an identifier already
// resolved upstream.
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := RequestCorrelationID(r)
		w.Header().Set(traceHeader, id)
		ctx := WithCorrelationID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var (
	ErrMissingToken = errors.New("missing bearer token")
	ErrExpired      = errors.New("expired token")
	ErrInvalid      = errors.New("invalid token")
	ErrUnavailable  = errors.New("jwks unavailable")
	errKeyNotFound  = errors.New("kid not found in jwks")
)

type Principal struct {
	Subject, ActorType, ActorID, TrainingCenterID string
	Roles, Features                               []string
	FeatureGrants                                 []FeatureGrant
}

// FeatureGrant preserves both components of the FEATURE_CODE:SCOPE_TYPE
// value issued by IAM. Scope-specific data filtering is outside this HU.
type FeatureGrant struct {
	Code  string
	Scope string
}
type contextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

// TokenValidator is the seam for a future sena-platform-go implementation.
type TokenValidator interface {
	Validate(context.Context, string) (Principal, error)
}
type JWKSValidator struct {
	issuer string
	jwks   *JWKSClient
	now    func() time.Time
}

func NewJWKSValidator(issuer string, jwks *JWKSClient) (*JWKSValidator, error) {
	if strings.TrimSpace(issuer) == "" || jwks == nil {
		return nil, errors.New("jwt issuer and jwks client are required")
	}
	return &JWKSValidator{issuer: issuer, jwks: jwks, now: time.Now}, nil
}

func (v *JWKSValidator) Validate(ctx context.Context, raw string) (Principal, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || anyEmpty(parts) {
		return Principal{}, ErrInvalid
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if decodeJSON(parts[0], &header) != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return Principal{}, ErrInvalid
	}
	key, err := v.jwks.Key(ctx, header.KeyID)
	if err != nil {
		if errors.Is(err, errKeyNotFound) {
			return Principal{}, ErrInvalid
		}
		return Principal{}, fmt.Errorf("%w: signing key", ErrUnavailable)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, ErrInvalid
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) != nil {
		return Principal{}, ErrInvalid
	}
	var claims tokenClaims
	if decodeJSON(parts[1], &claims) != nil || claims.Issuer != v.issuer || strings.TrimSpace(claims.Subject) == "" {
		return Principal{}, ErrInvalid
	}
	if claims.ExpiresAt == nil || v.now().Unix() >= *claims.ExpiresAt {
		return Principal{}, ErrExpired
	}
	if claims.NotBefore != nil && v.now().Unix() < *claims.NotBefore {
		return Principal{}, ErrInvalid
	}
	grants, err := parseGrants(claims.Features)
	if err != nil {
		return Principal{}, ErrInvalid
	}
	return Principal{
		Subject:          claims.Subject,
		ActorType:        claims.ActorType,
		ActorID:          claims.ActorID,
		TrainingCenterID: claims.TrainingCenterID,
		Roles:            claims.Roles,
		Features:         append([]string(nil), claims.Features...),
		FeatureGrants:    grants,
	}, nil
}

type tokenClaims struct {
	Issuer           string   `json:"iss"`
	Subject          string   `json:"sub"`
	ExpiresAt        *int64   `json:"exp"`
	NotBefore        *int64   `json:"nbf"`
	ActorType        string   `json:"actor_type"`
	ActorID          string   `json:"actor_id"`
	TrainingCenterID string   `json:"training_center_id"`
	Roles            []string `json:"roles"`
	Features         []string `json:"features"`
}

func anyEmpty(parts []string) bool {
	for _, part := range parts {
		if part == "" {
			return true
		}
	}
	return false
}
func decodeJSON(encoded string, target any) error {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// JWKSClient caches known keys and negative-caches unknown kids. refreshMu gives concurrent cache misses one network request.
type JWKSClient struct {
	url                string
	client             *http.Client
	ttl, unknownKidTTL time.Duration
	mu                 sync.RWMutex
	refreshMu          sync.Mutex
	keys               map[string]*rsa.PublicKey
	unknownKids        map[string]time.Time
	expiry             time.Time
}

func NewJWKSClient(url string, timeout, ttl time.Duration) (*JWKSClient, error) {
	return NewJWKSClientWithPolicy(url, timeout, ttl, 30*time.Second)
}
func NewJWKSClientWithPolicy(rawURL string, timeout, ttl, unknownKidTTL time.Duration) (*JWKSClient, error) {
	if err := validateJWKSURL(rawURL); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if unknownKidTTL <= 0 {
		unknownKidTTL = 30 * time.Second
	}
	return &JWKSClient{url: rawURL, ttl: ttl, unknownKidTTL: unknownKidTTL, keys: map[string]*rsa.PublicKey{}, unknownKids: map[string]time.Time{}, client: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *JWKSClient) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, fresh, until := c.keys[kid], time.Now().Before(c.expiry), c.unknownKids[kid]
	c.mu.RUnlock()
	if key != nil && fresh {
		return key, nil
	}
	if key == nil && time.Now().Before(until) {
		return nil, errKeyNotFound
	}
	if err := c.refresh(ctx, kid, key == nil); err != nil {
		return nil, err
	}
	c.mu.RLock()
	key = c.keys[kid]
	c.mu.RUnlock()
	if key == nil {
		return nil, errKeyNotFound
	}
	return key, nil
}
func (c *JWKSClient) refresh(ctx context.Context, kid string, force bool) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	_, known := c.keys[kid]
	fresh, until := time.Now().Before(c.expiry), c.unknownKids[kid]
	c.mu.RUnlock()
	if (known && fresh) || (!force && fresh) {
		return nil
	}
	if force && time.Now().Before(until) {
		return errKeyNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return fmt.Errorf("%w: build request", ErrUnavailable)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request", ErrUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: jwks returned %s", ErrUnavailable, resp.Status)
	}
	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&document); err != nil {
		return fmt.Errorf("%w: invalid document", ErrUnavailable)
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, entry := range document.Keys {
		if entry.KTY == "RSA" && entry.KID != "" && (entry.Algorithm == "" || entry.Algorithm == "RS256") && (entry.Use == "" || entry.Use == "sig") {
			if key, err := entry.publicKey(); err == nil {
				keys[entry.KID] = key
			}
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: jwks has no usable RS256 keys", ErrUnavailable)
	}
	c.mu.Lock()
	c.keys, c.expiry = keys, time.Now().Add(c.ttl)
	if keys[kid] == nil {
		c.unknownKids[kid] = time.Now().Add(c.unknownKidTTL)
	} else {
		delete(c.unknownKids, kid)
	}
	c.mu.Unlock()
	if keys[kid] == nil {
		return errKeyNotFound
	}
	return nil
}
func validateJWKSURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("jwks url must be an absolute URL without credentials or fragment")
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(u.Scheme, "http") && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return errors.New("jwks url must use https outside local loopback")
}
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type jwk struct {
	KTY       string `json:"kty"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	KID       string `json:"kid"`
	N         string `json:"n"`
	E         string `json:"e"`
}

func (j jwk) publicKey() (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, err
	}
	if len(n) == 0 || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("invalid rsa jwk")
	}
	exponent := 0
	for _, b := range e {
		exponent = exponent<<8 | int(b)
	}
	if exponent < 3 || exponent%2 == 0 {
		return nil, errors.New("invalid rsa exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}, nil
}

// RequireFeature authenticates before exact feature comparison. Resource adapters remain responsible for scope-specific data filters.
func RequireFeature(validator TokenValidator, feature string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearer(r.Header.Get("Authorization"))
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "No se proporcionó un token de acceso válido.")
			return
		}
		if validator == nil {
			writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "No es posible validar el token en este momento.")
			return
		}
		principal, err := validator.Validate(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "No es posible validar el token en este momento.")
				return
			}
			message := "El token de acceso no es válido."
			if errors.Is(err, ErrExpired) {
				message = "El token de acceso expiró."
			}
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", message)
			return
		}

		if !HasFeature(principal, feature) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "No tienes el feature requerido para esta operación.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, principal)))
	})
}
func bearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrMissingToken
	}
	return parts[1], nil
}

// validScopes mirrors IAM's ScopeType enum. It is a grammar validation list,
// not a role-to-feature policy: IAM remains the authority that decides which
// FEATURE:SCOPE grants are emitted in a token.
var validScopes = map[string]struct{}{"GLOBAL": {}, "TRAINING_CENTER": {}, "AREA": {}, "OWN_FICHAS": {}, "OWN_SCHEDULE": {}, "OWN_PROFILE": {}, "OWN_FICHA_AS_LEARNER": {}}

func parseGrant(grant string) (string, string, bool) {
	code, scope, found := strings.Cut(grant, ":")
	if !found || code == "" || scope == "" || strings.Contains(scope, ":") {
		return "", "", false
	}
	_, valid := validScopes[scope]
	return code, scope, valid
}
func parseGrants(grants []string) ([]FeatureGrant, error) {
	parsed := make([]FeatureGrant, 0, len(grants))
	for _, grant := range grants {
		code, scope, ok := parseGrant(grant)
		if !ok {
			return nil, errors.New("invalid feature grant")
		}
		parsed = append(parsed, FeatureGrant{Code: code, Scope: scope})
	}
	return parsed, nil
}

func HasFeature(principal Principal, feature string) bool {
	if len(principal.FeatureGrants) > 0 {
		for _, grant := range principal.FeatureGrants {
			if grant.Code == feature {
				return true
			}
		}
		return false
	}
	for _, grant := range principal.Features {
		code, _, ok := parseGrant(grant)
		if ok && code == feature {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	traceID := RequestCorrelationID(r)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message, "trace_id": traceID}})
}

// WriteError writes the shared API error envelope. HTTP adapters use this so
// authentication, authorization and resource errors remain contract-compatible.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeError(w, r, status, code, message)
}
