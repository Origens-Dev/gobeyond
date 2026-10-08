package voicecontract

import "testing"

func TestMailboxPlaybackClaimHelpers(t *testing.T) {
	if (GrantClaims{MailboxPlayback: MailboxPlaybackOn}).AllowsNewMailboxPlayback() != true {
		t.Fatal("on claim must allow new playback")
	}
	for _, claim := range []string{"", MailboxPlaybackOff, "weird"} {
		if (GrantClaims{MailboxPlayback: claim}).AllowsNewMailboxPlayback() {
			t.Fatalf("claim %q must not allow new playback", claim)
		}
	}
	if !ValidMailboxPlaybackClaim("") || !ValidMailboxPlaybackClaim(MailboxPlaybackOff) || !ValidMailboxPlaybackClaim(MailboxPlaybackOn) {
		t.Fatal("recognized claims rejected")
	}
	if ValidMailboxPlaybackClaim("enabled") {
		t.Fatal("unknown claim accepted")
	}
}

func TestInheritMailboxPlaybackClaimCannotWiden(t *testing.T) {
	if got := InheritMailboxPlaybackClaim(MailboxPlaybackOff, MailboxPlaybackOn); got != MailboxPlaybackOff {
		t.Fatalf("off parent widened to %q", got)
	}
	if got := InheritMailboxPlaybackClaim(MailboxPlaybackOn, MailboxPlaybackOn); got != MailboxPlaybackOn {
		t.Fatalf("on parent+live got %q", got)
	}
	if got := InheritMailboxPlaybackClaim(MailboxPlaybackOn, MailboxPlaybackOff); got != MailboxPlaybackOff {
		t.Fatalf("on parent did not narrow: %q", got)
	}
	if got := InheritMailboxPlaybackClaim("", MailboxPlaybackOn); got != MailboxPlaybackOn {
		t.Fatalf("unspecified parent live-on got %q", got)
	}
	if got := InheritMailboxPlaybackClaim("", MailboxPlaybackOff); got != MailboxPlaybackOff {
		t.Fatalf("unspecified parent live-off got %q", got)
	}
}
