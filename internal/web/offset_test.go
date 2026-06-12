package web

import (
	"encoding/json"
	"testing"
)

// The offset helpers must round-trip a Qdrant scroll offset through a URL
// query parameter without changing its JSON type (string UUID vs integer ID).
func TestScrollOffsetRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		next json.RawMessage
		want string // JSON sent back to Qdrant after the round-trip
	}{
		{"uuid string ID", json.RawMessage(`"6ba7b810-9dad-11d1-80b4-00c04fd430c8"`), `"6ba7b810-9dad-11d1-80b4-00c04fd430c8"`},
		{"integer ID", json.RawMessage(`123`), `123`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			param := offsetParam(c.next)
			got := parseScrollOffset(param)
			if string(got) != c.want {
				t.Errorf("round-trip %s → %q → %s, want %s", c.next, param, got, c.want)
			}
		})
	}
}

func TestScrollOffsetEmpty(t *testing.T) {
	if offsetParam(nil) != "" {
		t.Error("nil offset should produce empty param")
	}
	if parseScrollOffset("") != nil {
		t.Error("empty param should produce nil offset")
	}
}
