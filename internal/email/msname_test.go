package email

import "testing"

func TestMSFormatByteSize(t *testing.T) {
	for _, tc := range msByteSizeReference {
		if got := MSFormatByteSize(tc.n); got != tc.want {
			t.Errorf("%d: expected %q, got %q", tc.n, tc.want, got)
		}
	}
}

// Every character class Outlook was seen to drop or keep in an attached
// message's file name.
func TestMSAttachedMessageFileName(t *testing.T) {
	cases := []struct{ display, want string }{
		{" !\"#$%&'()*+,-./0123456789:;<=>?@AZ[\\]^_`az{|}~", " !#$%'()+-.0123456789;@AZ^_`az{}~.msg"},
		{"Caffè €5 – “ok” «x» tab\there", "Caffè €5  ok x tabhere.msg"},
		{"  ..nome.. con punti..  ", "  ..nome.. con punti..  .msg"},
		{"***UNCHECKED*** Revoche del 29/05/2026", "UNCHECKED Revoche del 29052026.msg"},
		{"postacert.eml (6,41 MB)", "postacert.eml (641 MB).msg"},
		{"CON", "CON.msg"},
		{"***///", ".msg"},
		{"", ".msg"},
		{"§ª²µ¶¿×à", "§ª²µà.msg"},
		{"、！←∀\ufeff�", "、！←∀�.msg"},
		{"\U0001F600 \U0001D400", "\U0001F600 \U0001D400.msg"},
	}
	for _, tc := range cases {
		if got := MSAttachedMessageFileName(tc.display); got != tc.want {
			t.Errorf("%q: expected %q, got %q", tc.display, tc.want, got)
		}
	}
}
