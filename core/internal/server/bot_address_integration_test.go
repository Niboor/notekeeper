//go:build integration

package server_test

import "testing"

type botList struct {
	Items []struct {
		Name    string  `json:"name"`
		Address *string `json:"address"`
	} `json:"items"`
}

func (c *client) botAddress(t *testing.T) *string {
	t.Helper()
	var list botList
	res := c.do("GET", "/api/v1/bot-instances", nil)
	if res.Status != 200 {
		t.Fatalf("bot instances: %d", res.Status)
	}
	res.JSON(t, &list)
	if len(list.Items) != 1 {
		t.Fatalf("bot instances: %v", list.Items)
	}
	return list.Items[0].Address
}

// The bot reports the account users write to, so the pairing instructions can name it; an address outside the
// instance's identity domain is ignored (a stolen key cannot point users at another account).
func TestBotReportsItsChatAddress(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	key := s.makeBot("m", "example.org")

	if got := c.botAddress(t); got != nil {
		t.Fatalf("an address before any report: %q", *got)
	}
	if res := s.botDo(key, "POST", "/bot/v1/heartbeat", map[string]string{"address": "@notekeeper:example.org"}); res.Status != 204 {
		t.Fatalf("heartbeat with address: %d", res.Status)
	}
	if got := c.botAddress(t); got == nil || *got != "@notekeeper:example.org" {
		t.Fatalf("address after report: %v", got)
	}
	// A foreign address is not stored, and the heartbeat itself still counts.
	if res := s.botDo(key, "POST", "/bot/v1/heartbeat", map[string]string{"address": "@notekeeper:attacker.example"}); res.Status != 204 {
		t.Fatalf("heartbeat with foreign address: %d", res.Status)
	}
	if got := c.botAddress(t); got == nil || *got != "@notekeeper:example.org" {
		t.Fatalf("a foreign address replaced it: %v", got)
	}
	// A heartbeat without one keeps what is there.
	if res := s.botDo(key, "POST", "/bot/v1/heartbeat", nil); res.Status != 204 {
		t.Fatalf("plain heartbeat: %d", res.Status)
	}
	if got := c.botAddress(t); got == nil || *got != "@notekeeper:example.org" {
		t.Fatalf("a plain heartbeat cleared it: %v", got)
	}
}
