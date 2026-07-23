// Package document renders the complete SEO and hydration document around a
// body produced by GoBeyond's portable renderer.
package document

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"io"
	"strings"

	gb "github.com/holbrookab/gobeyond"
)

type BodyHTML string

type Asset struct {
	URL       string
	Integrity string
}

type HydrationData struct {
	APIVersion string `json:"apiVersion"`
	BuildID    string `json:"buildId"`
	RouteID    string `json:"routeId"`
	Props      any    `json:"props"`
}

type Input struct {
	PublicOrigin   string
	Indexable      bool
	Metadata       gb.Metadata
	Body           BodyHTML
	Hydration      HydrationData
	Styles         []Asset
	ModulePreloads []Asset
	Scripts        []Asset
	Nonce          string
}

func Render(writer io.Writer, input Input) error {
	if input.Hydration.APIVersion == "" {
		input.Hydration.APIVersion = gb.RenderAPIVersion
	}
	if input.Hydration.BuildID == "" || input.Hydration.RouteID == "" {
		return errors.New("hydration build ID and route ID are required")
	}
	if !input.Indexable {
		input.Metadata.Robots = "noindex, nofollow"
	}
	if err := input.Metadata.Validate(input.PublicOrigin, input.Indexable); err != nil {
		return err
	}
	hydration, err := marshalScriptJSON(input.Hydration)
	if err != nil {
		return err
	}

	var output bytes.Buffer
	output.WriteString("<!doctype html><html lang=\"")
	output.WriteString(attribute(input.Metadata.Lang))
	output.WriteString("\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	writeTitle(&output, input.Metadata.Title)
	writeNamedMeta(&output, "description", input.Metadata.Description)
	writeNamedMeta(&output, "robots", input.Metadata.Robots)
	writeLink(&output, "canonical", "", input.Metadata.Canonical)
	for _, alternate := range input.Metadata.Alternates {
		writeLink(&output, "alternate", alternate.Language, alternate.URL)
	}
	writePropertyMeta(&output, "og:type", input.Metadata.OpenGraph.Type)
	writePropertyMeta(&output, "og:title", input.Metadata.OpenGraph.Title)
	writePropertyMeta(&output, "og:description", input.Metadata.OpenGraph.Description)
	writePropertyMeta(&output, "og:url", input.Metadata.OpenGraph.URL)
	for _, image := range input.Metadata.OpenGraph.Images {
		writePropertyMeta(&output, "og:image", image)
	}
	writeNamedMeta(&output, "twitter:card", input.Metadata.Twitter.Card)
	writeNamedMeta(&output, "twitter:title", input.Metadata.Twitter.Title)
	writeNamedMeta(&output, "twitter:description", input.Metadata.Twitter.Description)
	for _, image := range input.Metadata.Twitter.Images {
		writeNamedMeta(&output, "twitter:image", image)
	}
	for _, style := range input.Styles {
		output.WriteString("<link rel=\"stylesheet\" href=\"")
		output.WriteString(attribute(style.URL))
		output.WriteByte('"')
		writeIntegrity(&output, style.Integrity)
		output.WriteByte('>')
	}
	for _, module := range input.ModulePreloads {
		output.WriteString("<link rel=\"modulepreload\" href=\"")
		output.WriteString(attribute(module.URL))
		output.WriteByte('"')
		writeIntegrity(&output, module.Integrity)
		output.WriteByte('>')
	}
	for _, value := range input.Metadata.JSONLD {
		encoded, marshalErr := marshalScriptJSON(value)
		if marshalErr != nil {
			return errors.New("invalid JSON-LD: " + marshalErr.Error())
		}
		output.WriteString("<script type=\"application/ld+json\"")
		writeNonce(&output, input.Nonce)
		output.WriteByte('>')
		output.Write(encoded)
		output.WriteString("</script>")
	}
	output.WriteString("</head><body><div id=\"__gobeyond\">")
	output.WriteString(string(input.Body))
	output.WriteString("</div><script id=\"__GOBEYOND_DATA__\" type=\"application/json\"")
	writeNonce(&output, input.Nonce)
	output.WriteByte('>')
	output.Write(hydration)
	output.WriteString("</script>")
	for _, script := range input.Scripts {
		output.WriteString("<script type=\"module\" src=\"")
		output.WriteString(attribute(script.URL))
		output.WriteByte('"')
		writeNonce(&output, input.Nonce)
		writeIntegrity(&output, script.Integrity)
		output.WriteString("></script>")
	}
	output.WriteString("</body></html>")
	_, err = writer.Write(output.Bytes())
	return err
}

func marshalScriptJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded = bytes.ReplaceAll(encoded, []byte("\xe2\x80\xa8"), []byte(`\u2028`))
	encoded = bytes.ReplaceAll(encoded, []byte("\xe2\x80\xa9"), []byte(`\u2029`))
	return encoded, nil
}

func writeTitle(output *bytes.Buffer, value string) {
	output.WriteString("<title>")
	output.WriteString(html.EscapeString(value))
	output.WriteString("</title>")
}

func writeNamedMeta(output *bytes.Buffer, name, content string) {
	if content == "" {
		return
	}
	output.WriteString("<meta name=\"")
	output.WriteString(attribute(name))
	output.WriteString("\" content=\"")
	output.WriteString(attribute(content))
	output.WriteString("\">")
}

func writePropertyMeta(output *bytes.Buffer, property, content string) {
	if content == "" {
		return
	}
	output.WriteString("<meta property=\"")
	output.WriteString(attribute(property))
	output.WriteString("\" content=\"")
	output.WriteString(attribute(content))
	output.WriteString("\">")
}

func writeLink(output *bytes.Buffer, rel, language, href string) {
	if href == "" {
		return
	}
	output.WriteString("<link rel=\"")
	output.WriteString(attribute(rel))
	output.WriteByte('"')
	if language != "" {
		output.WriteString(" hreflang=\"")
		output.WriteString(attribute(language))
		output.WriteByte('"')
	}
	output.WriteString(" href=\"")
	output.WriteString(attribute(href))
	output.WriteString("\">")
}

func writeNonce(output *bytes.Buffer, nonce string) {
	if nonce == "" {
		return
	}
	output.WriteString(" nonce=\"")
	output.WriteString(attribute(nonce))
	output.WriteByte('"')
}

func writeIntegrity(output *bytes.Buffer, integrity string) {
	if integrity == "" {
		return
	}
	output.WriteString(" integrity=\"")
	output.WriteString(attribute(integrity))
	output.WriteString("\" crossorigin=\"anonymous\"")
}

func attribute(value string) string {
	return strings.ReplaceAll(html.EscapeString(value), "&#39;", "&#x27;")
}
