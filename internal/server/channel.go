package server

import (
	"context"
	"net/http"
)

type Channel interface {
	Name() string
	Enabled() bool
	RegisterRoutes(mux *http.ServeMux)
	Start(ctx context.Context)
}
