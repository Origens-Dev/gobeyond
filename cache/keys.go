package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Origens-Dev/gobeyond/internal/jsvalue"
)

// KeySchemaVersion identifies the key layout implemented by RouteKey and
// DataKey. Bump it (and branch on it in readers) if the layout ever changes
// shape rather than just namespace values.
const KeySchemaVersion = "gobeyond.cache.keys/v1alpha1"

// RouteKey builds the store key for one page route's cached response
// (Locked decision 12). Keys are stable strings intentionally kept
// human-readable for operability; they are not a security boundary by
// themselves - deployPrefix plus fail-closed privacy (IsPrivateRequest /
// IsPrivateResponse) are.
//
// Schema: {deployPrefix}/{buildId}/route/{routeId}?{normalizedPath}{rawQuery}@{publicOrigin}
func RouteKey(deployPrefix, buildID, routeID, path, rawQuery, publicOrigin string) (string, error) {
	if deployPrefix == "" {
		return "", errors.New("cache: route key requires a non-empty deployPrefix")
	}
	if buildID == "" {
		return "", errors.New("cache: route key requires a non-empty buildID")
	}
	if routeID == "" {
		return "", errors.New("cache: route key requires a non-empty routeID")
	}
	if publicOrigin == "" {
		return "", errors.New("cache: route key requires a non-empty publicOrigin")
	}
	normalizedPath, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	var key strings.Builder
	key.WriteString(deployPrefix)
	key.WriteByte('/')
	key.WriteString(buildID)
	key.WriteString("/route/")
	key.WriteString(routeID)
	key.WriteByte('?')
	key.WriteString(normalizedPath)
	if rawQuery != "" {
		key.WriteByte('&')
		key.WriteString(rawQuery)
	}
	key.WriteByte('@')
	key.WriteString(publicOrigin)
	return key.String(), nil
}

// DataKey builds the store key for one cache.Load entry (Locked decision
// 12). name is the caller-chosen, deploy-unique identifier passed as
// cache.Options.Name (e.g. "catalog.product"); args are canonically encoded
// so that equal argument values always produce the same key regardless of
// map key insertion order, and values that cannot be encoded deterministically
// are rejected rather than silently coerced.
//
// Schema: {deployPrefix}/{buildId}/data/{name}/{argsDigest}
func DataKey(deployPrefix, buildID, name string, args []any) (string, error) {
	if deployPrefix == "" {
		return "", errors.New("cache: data key requires a non-empty deployPrefix")
	}
	if buildID == "" {
		return "", errors.New("cache: data key requires a non-empty buildID")
	}
	if name == "" {
		return "", errors.New("cache: data key requires a non-empty name")
	}
	digest, err := canonicalArgsJSON(args)
	if err != nil {
		return "", fmt.Errorf("cache: data key %q: %w", name, err)
	}
	var key strings.Builder
	key.WriteString(deployPrefix)
	key.WriteByte('/')
	key.WriteString(buildID)
	key.WriteString("/data/")
	key.WriteString(name)
	key.WriteByte('/')
	key.Write(digest)
	return key.String(), nil
}

// canonicalArgsJSON encodes args as JSON, rejecting values that are not
// JavaScript/JSON-compatible (NaN/Inf floats, unsafe integers, nil
// collections, non-string map keys, funcs, chans, ...) via the same
// jsvalue.Validate rule the runtime already applies to page props and action
// results. encoding/json marshals map[string]any keys in sorted order, which
// is what makes the result canonical: two Args slices with equal content but
// differently-ordered map literals always encode identically.
func canonicalArgsJSON(args []any) ([]byte, error) {
	if args == nil {
		args = []any{}
	}
	if err := jsvalue.Validate(args); err != nil {
		return nil, fmt.Errorf("args are not canonically encodable: %w", err)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("args are not canonically encodable: %w", err)
	}
	return encoded, nil
}

// NormalizePath validates and normalizes an absolute request path for use in
// RouteKey: it requires a leading "/", rejects "." / ".." traversal
// segments, and collapses duplicate slashes and a single trailing slash
// (except the root path) so that equivalent paths always map to the same
// route key.
func NormalizePath(path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("cache: path must be absolute")
	}
	segments := strings.Split(path, "/")
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "":
			continue
		case ".", "..":
			return "", errors.New("cache: path must not contain traversal segments")
		default:
			clean = append(clean, segment)
		}
	}
	if len(clean) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(clean, "/"), nil
}
