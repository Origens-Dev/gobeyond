package imageopt

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Variant is the normalized public optimizer request, shared by all loaders.
type Variant struct {
	Source         string
	Width, Quality int
	Format         string
	Negotiated     bool
}

type VariantError struct {
	Status        int
	Code, Message string
}

func (e *VariantError) Error() string { return e.Message }

// ParseVariant rejects ambiguous parameters before any source or cache access.
func ParseVariant(rawQuery, accept string) (Variant, error) {
	fail := func(code string) (Variant, error) {
		return Variant{}, &VariantError{http.StatusBadRequest, code, strings.ReplaceAll(code, "_", " ")}
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fail("invalid_query")
	}
	for _, key := range []string{"url", "w", "q", "f", "v"} {
		if len(query[key]) > 1 {
			return fail("duplicate_parameter")
		}
	}
	if versions, ok := query["v"]; ok && (versions[0] != "1" && versions[0] != "2") {
		return fail("invalid_version")
	}
	width, ok := positiveDecimal(query.Get("w"), 3840)
	if !ok {
		return fail("invalid_width")
	}
	quality := defaultQuality
	if values, exists := query["q"]; exists {
		quality, ok = positiveDecimal(values[0], 100)
		if !ok {
			return fail("invalid_quality")
		}
	}
	source := query.Get("url")
	if source == "" {
		return fail("invalid_source")
	}
	format := strings.ToLower(query.Get("f"))
	if format == "jpg" {
		format = "jpeg"
	}
	negotiated := format == "" || format == "auto"
	if negotiated {
		webp, jpeg := acceptQuality(accept, "image/webp"), acceptQuality(accept, "image/jpeg")
		if webp > 0 && webp >= jpeg {
			format = "webp"
		} else if jpeg > 0 {
			format = "jpeg"
		} else {
			return Variant{}, &VariantError{http.StatusNotAcceptable, "not_acceptable", "no acceptable image format"}
		}
	}
	if format != "jpeg" && format != "png" && format != "webp" {
		return fail("unsupported_format")
	}
	return Variant{source, width, quality, format, negotiated}, nil
}

func positiveDecimal(raw string, max int) (int, bool) {
	if raw == "" {
		return 0, false
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(raw)
	return number, err == nil && number >= 1 && number <= max
}

// More specific ranges override wildcards, including explicit q=0 exclusions.
func acceptQuality(header, target string) float64 {
	if strings.TrimSpace(header) == "" {
		if target == "image/jpeg" {
			return 1
		}
		return 0
	}
	bestSpecificity, bestQuality := -1, 0.0
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.ToLower(strings.TrimSpace(entry)), ";")
		media := strings.TrimSpace(parts[0])
		specificity := -1
		switch media {
		case target:
			specificity = 2
		case "image/*":
			specificity = 1
		case "*/*":
			specificity = 0
		}
		if specificity < 0 {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			parameter = strings.TrimSpace(parameter)
			if !strings.HasPrefix(parameter, "q=") {
				continue
			}
			raw := strings.TrimPrefix(parameter, "q=")
			if !acceptQ.MatchString(raw) {
				quality = 0
			} else {
				quality, _ = strconv.ParseFloat(raw, 64)
			}
		}
		if specificity > bestSpecificity || (specificity == bestSpecificity && quality > bestQuality) {
			bestSpecificity, bestQuality = specificity, quality
		}
	}
	return bestQuality
}

var acceptQ = regexp.MustCompile(`^(?:0(?:\.[0-9]{0,3})?|1(?:\.0{0,3})?)$`)
