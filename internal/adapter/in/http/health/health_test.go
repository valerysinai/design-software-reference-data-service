package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakePinger struct {
	err error
}

func (f fakePinger) PingContext(context.Context) error { return f.err }

func TestLiveDoesNotRequireDatabase(t *testing.T) {
	h := NewHandler(nil, time.Second)
	r := httptest.NewRecorder()
	h.Live(r, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if r.Code != http.StatusOK || r.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected liveness response: status=%d body=%q", r.Code, r.Body.String())
	}
}

func TestReadyChecksDatabase(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "ready", want: http.StatusOK},
		{name: "database unavailable", err: errors.New("database down"), want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(fakePinger{err: tt.err}, time.Second)
			r := httptest.NewRecorder()
			h.Ready(r, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
			if r.Code != tt.want {
				t.Fatalf("unexpected readiness status: got=%d want=%d", r.Code, tt.want)
			}
		})
	}
}
