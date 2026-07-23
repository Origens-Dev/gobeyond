// Package lambdaurl adapts an http.Handler to an AWS Lambda Function URL
// (payload format 2.0) entrypoint.
//
// Package as provided.al2023 with a bootstrap binary. Ship render plans and
// runtime manifests beside bootstrap; do not include dist/static in the zip—
// hashed assets and SSG HTML belong on S3 behind CloudFront.
package lambdaurl

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// Serve starts the Lambda runtime and dispatches Function URL requests to
// handler. It never returns on success.
func Serve(handler http.Handler) {
	if handler == nil {
		panic("lambdaurl: nil handler")
	}
	lambda.Start(func(ctx context.Context, req events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
		return Dispatch(ctx, handler, req)
	})
}

// Dispatch converts a Function URL request into an http.Request, serves it
// through handler, and returns a Function URL response. Exported for tests.
func Dispatch(ctx context.Context, handler http.Handler, req events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	httpReq, err := functionURLToRequest(ctx, req)
	if err != nil {
		return events.LambdaFunctionURLResponse{StatusCode: http.StatusBadRequest, Body: "bad request"}, nil
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httpReq)
	return recorderToFunctionURL(rec), nil
}

func functionURLToRequest(ctx context.Context, req events.LambdaFunctionURLRequest) (*http.Request, error) {
	method := req.RequestContext.HTTP.Method
	if method == "" {
		method = http.MethodGet
	}
	path := req.RawPath
	if path == "" {
		path = "/"
	}

	var body io.Reader = http.NoBody
	if req.Body != "" {
		raw := []byte(req.Body)
		if req.IsBase64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(req.Body)
			if err != nil {
				return nil, err
			}
			raw = decoded
		}
		body = bytes.NewReader(raw)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if len(req.QueryStringParameters) > 0 {
		q := url.Values{}
		for k, v := range req.QueryStringParameters {
			q.Set(k, v)
		}
		httpReq.URL.RawQuery = q.Encode()
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if host := httpReq.Header.Get("Host"); host != "" {
		httpReq.Host = host
	}
	// Edge may omit viewer Host toward origins (e.g. AllViewerExceptHostHeader)
	// and instead forward x-gobeyond-viewer-host for virtual-host routing.
	if viewer := httpReq.Header.Get("X-Gobeyond-Viewer-Host"); viewer != "" {
		httpReq.Host = viewer
		httpReq.Header.Set("Host", viewer)
	}
	return httpReq, nil
}

func recorderToFunctionURL(rec *httptest.ResponseRecorder) events.LambdaFunctionURLResponse {
	result := rec.Result()
	defer result.Body.Close()

	headers := map[string]string{}
	for k, vals := range result.Header {
		if len(vals) > 0 {
			headers[k] = vals[0]
		}
	}

	bodyBytes, _ := io.ReadAll(result.Body)
	if utf8.Valid(bodyBytes) {
		return events.LambdaFunctionURLResponse{
			StatusCode: result.StatusCode,
			Headers:    headers,
			Body:       string(bodyBytes),
		}
	}
	return events.LambdaFunctionURLResponse{
		StatusCode:      result.StatusCode,
		Headers:         headers,
		Body:            base64.StdEncoding.EncodeToString(bodyBytes),
		IsBase64Encoded: true,
	}
}
