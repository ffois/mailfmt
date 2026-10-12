package eml

import (
	"strings"
	"testing"

	"github.com/ffois/mailfmt/internal/email"
)

// Every value Outlook's priority handler accepts, and a few it rejects.
func TestMSParseImportance(t *testing.T) {
	cases := []struct {
		value string
		want  email.MSImportance
		ok    bool
	}{
		{"1", email.MSImportanceHigh, true},
		{"2", email.MSImportanceHigh, true},
		{"3", email.MSImportanceNormal, true},
		{"4", email.MSImportanceLow, true},
		{"5", email.MSImportanceLow, true},
		{"1 (Highest)", email.MSImportanceHigh, true},
		{"5 (Lowest)", email.MSImportanceLow, true},
		{"high", email.MSImportanceHigh, true},
		{"Normal", email.MSImportanceNormal, true},
		{"LOW", email.MSImportanceLow, true},
		{"10", 0, false},
		{"0", 0, false},
		{"Urgent", 0, false},
		{"High priority", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := msParseImportance(tc.value)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%q: expected (%v, %v), got (%v, %v)", tc.value, tc.want, tc.ok, got, ok)
		}
	}
}

// A valid Importance wins over the other two headers whatever its position;
// without one, a valid X-Priority is used; without any, the message is Normal.
func TestMSImportancePrecedence(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    email.MSImportance
	}{
		{"none", "", email.MSImportanceNormal},
		{"x-priority only", "X-Priority: 1 (Highest)\r\n", email.MSImportanceHigh},
		{"x-msmail-priority only", "X-MSMail-Priority: Low\r\n", email.MSImportanceLow},
		{"importance after x-priority", "X-Priority: 1\r\nImportance: Low\r\n", email.MSImportanceLow},
		{"importance before x-priority", "Importance: Low\r\nX-Priority: 1\r\n", email.MSImportanceLow},
		{"invalid importance falls through", "Importance: Urgent\r\nX-Priority: 5\r\n", email.MSImportanceLow},
		{"invalid x-priority falls through", "X-Priority: 9\r\nX-MSMail-Priority: High\r\n", email.MSImportanceHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "From: a@example.it\r\nTo: b@example.it\r\nSubject: Priorità\r\n" +
				tc.headers + "\r\ncorpo\r\n"
			data, err := MS_ReadFile(writeEML(t, raw))
			if err != nil {
				t.Fatalf("MS_ReadFile: %v", err)
			}
			if data.Importance != tc.want {
				t.Errorf("expected importance %v, got %v", tc.want, data.Importance)
			}
		})
	}
}

// The conversation topic drops a short prefix ending in a colon and keeps
// everything else verbatim.
func TestMSConversationTopic(t *testing.T) {
	cases := []struct{ subject, want string }{
		{"RE: Preventivo", "Preventivo"},
		{"Fw:   Preventivo", "Preventivo"},
		{"R: Preventivo", "Preventivo"},
		{"AW : Preventivo", "Preventivo"},
		{"  RE: Preventivo", "Preventivo"},
		{"RE: RE: Preventivo", "RE: Preventivo"},
		{"Preventivo", "Preventivo"},
		{"Nota: leggere", "Nota: leggere"},
		{"Q3: risultati", "Q3: risultati"},
		{"R E: Preventivo", "R E: Preventivo"},
		{": Preventivo", ": Preventivo"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := msConversationTopic(tc.subject); got != tc.want {
			t.Errorf("%q: expected %q, got %q", tc.subject, tc.want, got)
		}
	}
}

// X-Unsent is read as a number: non-zero marks a draft, anything else does not.
func TestMSUnsent(t *testing.T) {
	cases := []struct {
		name    string
		headers string
		want    bool
	}{
		{"absent", "", false},
		{"one", "X-Unsent: 1\r\n", true},
		{"zero", "X-Unsent: 0\r\n", false},
		{"not a number", "X-Unsent: yes\r\n", false},
		{"last one wins", "X-Unsent: 1\r\nX-Unsent: 0\r\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "From: a@example.it\r\nTo: b@example.it\r\nSubject: Bozza\r\n" +
				tc.headers + "\r\ncorpo\r\n"
			data, err := MS_ReadFile(writeEML(t, raw))
			if err != nil {
				t.Fatalf("MS_ReadFile: %v", err)
			}
			if data.Unsent != tc.want {
				t.Errorf("expected unsent=%v, got %v", tc.want, data.Unsent)
			}
		})
	}
}

// MS_Build keeps everything Build reports: the Outlook fields are additions.
func TestMSReadFileKeepsPlainFields(t *testing.T) {
	raw := "From: =?utf-8?B?TWFyaW8gUm9zc2k=?= <mario@example.it>\r\n" +
		"To: b@example.it\r\n" +
		"Subject: RE: =?iso-8859-1?Q?Perch=E9?=\r\n" +
		"Date: Mon, 12 Oct 2026 09:30:00 +0200\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo\r\n"
	path := writeEML(t, raw)

	plain, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	ms, err := MS_ReadFile(path)
	if err != nil {
		t.Fatalf("MS_ReadFile: %v", err)
	}
	if ms.Subject != plain.Subject || ms.From != plain.From || ms.Date != plain.Date ||
		strings.TrimSpace(ms.Body) != strings.TrimSpace(plain.Body) {
		t.Errorf("plain fields differ:\n plain=%+v\n    ms=%+v", *plain, ms.Data)
	}
	if ms.ConversationTopic != "Perché" {
		t.Errorf("expected topic %q, got %q", "Perché", ms.ConversationTopic)
	}
}
