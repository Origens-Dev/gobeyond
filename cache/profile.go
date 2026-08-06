package cache

import "time"

// Profile is a named cache policy for application-owned values. Profiles are
// explicit so an omitted policy continues to mean "do not cache".
type Profile string

const (
	ProfileShortLived           Profile = "short_lived"
	ProfilePublicRoute          Profile = "public_route"
	ProfileStaleWhileRevalidate Profile = "stale_while_revalidate"
	ProfileUntilInvalidated     Profile = "until_invalidated"
	ProfileImmutable            Profile = "immutable"
)

const SafetyTTL = 31 * 24 * time.Hour

// Duration returns the fresh window for a profile. Even invalidated entries
// have a finite safety TTL so a lost invalidation cannot pin data forever.
func (p Profile) Duration() time.Duration {
	switch p {
	case ProfileShortLived:
		return time.Minute
	case ProfilePublicRoute:
		return 15 * time.Minute
	case ProfileStaleWhileRevalidate:
		return 5 * time.Minute
	case ProfileUntilInvalidated, ProfileImmutable:
		return SafetyTTL
	default:
		return 0
	}
}

func (p Profile) Valid() bool { return p.Duration() > 0 }
