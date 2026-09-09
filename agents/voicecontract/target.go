package voicecontract

import (
	"errors"
	"strings"
)

// Validate checks the server-owned projection of a resolved destination. The
// projection deliberately contains no provider route or dial code.
func (t VoiceTarget) Validate() error {
	if !safeText(t.PublicLabel, 128) {
		return errors.New("invalid target label")
	}
	if (t.DestinationID == "") == (t.PhoneNumber == "") {
		return errors.New("target must contain one destination input")
	}
	if t.DestinationID != "" && !identifier(t.DestinationID) {
		return errors.New("invalid target destination")
	}
	if t.PhoneNumber != "" && !ValidE164(t.PhoneNumber) {
		return errors.New("invalid target phone number")
	}
	switch t.Kind {
	case "assistant":
		if t.Class != "assistant" || t.DestinationID == "" {
			return errors.New("invalid assistant target")
		}
	case "line":
		if t.Class != "line" && t.Class != "extension" || t.DestinationID == "" {
			return errors.New("invalid line target")
		}
	case "pstn":
		if t.Class != "pstn" && t.Class != "outside_pstn" || t.PhoneNumber == "" {
			return errors.New("invalid pstn target")
		}
	case "connected_line":
		if t.Class != "connected_line" || t.DestinationID == "" {
			return errors.New("invalid connected line target")
		}
	default:
		return errors.New("invalid target kind")
	}
	return nil
}

// ValidateLegacyTarget is the compatibility projection used by v1 operator
// transfers. It intentionally remains separate from the generic v2 target.
func ValidateLegacyTarget(destinationID, publicLabel, class string) error {
	if class != "extension" && class != "managed" && class != "pstn" {
		return errors.New("invalid legacy target class")
	}
	if !identifier(destinationID) || !safeText(publicLabel, 128) || strings.TrimSpace(publicLabel) == "" {
		return errors.New("invalid legacy target")
	}
	return nil
}

// ValidE164 accepts canonical E.164: a plus sign followed by 7–15 digits,
// with a non-zero first country-code digit.
func ValidE164(s string) bool {
	if len(s) < 8 || len(s) > 16 || s[0] != '+' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
