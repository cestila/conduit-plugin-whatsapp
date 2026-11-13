package main

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// El JID canónico de usuario (@s.whatsapp.net) expone el número de teléfono
// internacional directamente en su parte de usuario. Un LID o un grupo no.
func TestPhoneFromCanonicalJID(t *testing.T) {
	tests := []struct {
		jid  types.JID
		want string
	}{
		{
			jid:  types.NewJID("5491100008888", types.DefaultUserServer),
			want: "+5491100008888",
		},
		{
			jid:  types.NewJID("123456", types.HiddenUserServer),
			want: "",
		},
		{
			jid:  types.NewJID("123456-789", types.GroupServer),
			want: "",
		},
		{
			jid:  types.EmptyJID,
			want: "",
		},
	}

	for _, tt := range tests {
		if got := phoneFromCanonicalJID(tt.jid); got != tt.want {
			t.Errorf("phoneFromCanonicalJID(%s) = %q, want %q", tt.jid, got, tt.want)
		}
	}
}
