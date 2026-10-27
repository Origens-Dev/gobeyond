package v1

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrPromptUnavailable is returned when say or clarify names a prompt ID that
// is not in the frozen definition. The session fails closed; there is no
// free-text fallback and no say is emitted.
var ErrPromptUnavailable = errors.New("prompt_unavailable")

// Prompts flattens every compiled Graph.Messages variant into id/locale/text
// declarations. Locale is the variant key on each frozen message. A deploy
// step unmarshals the agent manifest and calls this; it does not run the
// agent. There is no separate prompts file.
func (definition Definition) Prompts() []Prompt {
	prompts := make([]Prompt, 0)
	for _, family := range definition.Graph.Messages {
		locales := make([]string, 0, len(family.Variants))
		for locale := range family.Variants {
			locales = append(locales, locale)
		}
		sort.Strings(locales)
		for _, locale := range locales {
			text := family.Variants[locale]
			if family.ID == "" || locale == "" || text == "" {
				continue
			}
			prompts = append(prompts, Prompt{ID: family.ID, Locale: locale, Text: text})
		}
	}
	return prompts
}

// RequirePrompt fails closed when id is not in the frozen prompt set. Say and
// clarify both use this check.
func (definition Definition) RequirePrompt(id string) error {
	if strings.TrimSpace(id) == "" || !isIdentifier(id) {
		return fmt.Errorf("%w: prompt id is required", ErrPromptUnavailable)
	}
	for _, family := range definition.Graph.Messages {
		if family.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrPromptUnavailable, id)
}
