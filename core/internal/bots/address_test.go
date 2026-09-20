package bots

import "testing"

// A bot may report the address users write to, but only one on its own identity domain: it cannot send users
// to an account elsewhere.
func TestUsableAddress(t *testing.T) {
	cases := []struct {
		address, domain string
		want            string // "" means not usable
	}{
		{"@notekeeper:example.org", "example.org", "@notekeeper:example.org"},
		{"  @notekeeper:example.org ", "example.org", "@notekeeper:example.org"},
		{"@notekeeper:Example.ORG", "example.org", "@notekeeper:Example.ORG"},
		{"@notekeeper:example.org:8448", "example.org:8448", "@notekeeper:example.org:8448"},
		{"@notekeeper:attacker.example", "example.org", ""},
		{"@notekeeper:example.org.attacker.example", "example.org", ""},
		{"@notekeeper:notexample.org", "example.org", ""},
		{"", "example.org", ""},
		{"@:example.org", "example.org", ""},
		{"notekeeper", "example.org", ""},
		{"@note keeper:example.org", "example.org", ""},
		{"@notekeeper:example.org\n", "example.org\n", ""},
		{"@notekeeper:example.org", "", ""},
	}
	for _, c := range cases {
		got := usableAddress(c.address, c.domain)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("usableAddress(%q, %q) = %q, want none", c.address, c.domain, *got)
		case c.want != "" && (got == nil || *got != c.want):
			t.Errorf("usableAddress(%q, %q) = %v, want %q", c.address, c.domain, got, c.want)
		}
	}
	long := "@" + string(make([]byte, 300)) + ":example.org"
	if usableAddress(long, "example.org") != nil {
		t.Error("an address longer than 255 bytes was accepted")
	}
}
