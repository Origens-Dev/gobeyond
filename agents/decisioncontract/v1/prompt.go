package v1

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrPromptUnavailable is returned when a say names a prompt ID that is not
// in the frozen definition. The session fails closed; there is no free-text
// fallback.
var ErrPromptUnavailable = errors.New("prompt_unavailable")

// Prompt returns the id/locale/text declaration for one compiled variant.
func (family MessageFamily) Prompt(locale string) (Prompt, bool) {
	if family.ID == "" || locale == "" || family.Variants == nil {
		return Prompt{}, false
	}
	text, ok := family.Variants[locale]
	if !ok {
		return Prompt{}, false
	}
	return Prompt{ID: family.ID, Locale: locale, Text: text}, true
}

// HasPrompt reports whether id is a compiled prompt identity in the frozen
// definition. The host preloads this set; a missing ID is prompt_unavailable.
func (definition Definition) HasPrompt(id string) bool {
	_, ok := definition.messageFamily(id)
	return ok
}

// Prompt returns the compiled id/locale/text declaration. Missing IDs are
// not found rather than substituted with free text.
func (definition Definition) Prompt(id, locale string) (Prompt, bool) {
	family, ok := definition.messageFamily(id)
	if !ok {
		return Prompt{}, false
	}
	return family.Prompt(locale)
}

// Prompts flattens every compiled message variant into id/locale/text
// declarations. Authors declare these as route prompts; the compiler writes
// them into the agent manifest. There is no separate prompts file.
func (definition Definition) Prompts() []Prompt {
	prompts := make([]Prompt, 0)
	for _, family := range definition.Graph.Messages {
		locales := make([]string, 0, len(family.Variants))
		for locale := range family.Variants {
			locales = append(locales, locale)
		}
		sort.Strings(locales)
		for _, locale := range locales {
			prompts = append(prompts, Prompt{ID: family.ID, Locale: locale, Text: family.Variants[locale]})
		}
	}
	return prompts
}

// RequirePrompt fails closed when id is not in the frozen prompt set.
func (definition Definition) RequirePrompt(id string) error {
	if strings.TrimSpace(id) == "" || !isIdentifier(id) {
		return fmt.Errorf("%w: prompt id is required", ErrPromptUnavailable)
	}
	if !definition.HasPrompt(id) {
		return fmt.Errorf("%w: %s", ErrPromptUnavailable, id)
	}
	return nil
}

func (definition Definition) messageFamily(id string) (MessageFamily, bool) {
	for _, family := range definition.Graph.Messages {
		if family.ID == id {
			return family, true
		}
	}
	return MessageFamily{}, false
}
