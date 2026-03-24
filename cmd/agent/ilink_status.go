package main

import (
	"context"
	"io"
	"net/http"
)

func runIlinkStatus(ctx context.Context, stdout, stderr io.Writer, options cliOptions) int {
	return runIlinkEndpoint(ctx, stdout, stderr, options, http.MethodGet, "/api/v1/weixin/ilink/status")
}
