// Package apitime owns the fixture's public /api/time endpoint.
package apitime

import (
	"encoding/json"
	"net/http"
	"time"

	gb "github.com/holbrookab/gobeyond"
)

func GET(_ *gb.RequestContext) (gb.Response, error) {
	body, _ := json.Marshal(map[string]string{"time": time.Now().UTC().Format(time.RFC3339)})
	return gb.Response{
		Status:  http.StatusOK,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    body,
	}, nil
}
