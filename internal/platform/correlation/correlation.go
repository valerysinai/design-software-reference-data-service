package correlation

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
)

type key struct{}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

func ID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(key{}).(string)
	return id, ok && strings.TrimSpace(id) != ""
}

// NewID is the only generator of correlation identifiers in the service. It is
// used as a last resort, when the caller sent no X-Request-Id and no valid
// trace id is available because no exporter is configured.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
