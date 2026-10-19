package security

import "net/http"

// ProtectedRoute is the minimal contract-to-router mapping. Keep this mapping at
// route registration; handlers and the domain never need JWT/RBAC knowledge.
type ProtectedRoute struct {
	Method  string
	Pattern string
	Feature string
	Handler http.Handler
}

func RegisterProtectedRoutes(mux *http.ServeMux, validator TokenValidator, routes []ProtectedRoute) {
	for _, route := range routes {
		mux.Handle(route.Method+" "+route.Pattern, RequireFeature(validator, route.Feature, route.Handler))
	}
}
