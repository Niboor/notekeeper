//go:build integration

package server_test

import (
	"strings"
	"testing"
)

func (s *stack) noticeTexts() []string {
	var out []string
	for _, it := range s.outboxItems("notice") {
		out = append(out, it["text"].(string))
	}
	return out
}

func (s *stack) securityNotifications(user string) int {
	return s.count(`select count(*) from notifications n join users u on u.id = n.user_id where u.username = $1 and n.kind = 'security'`, user)
}

func hasNotice(texts []string, part string) bool {
	for _, t := range texts {
		if strings.Contains(t, part) {
			return true
		}
	}
	return false
}

// Security-relevant events reach the user in the app and in their chats: a new sign-in, a password
// change, a chat linked or unlinked, a share link created. The first two of those that are about
// habit can be muted; the others cannot (AUTH-U11, SEC-AUD-4).
func TestSecurityNoticesReachTheAppAndTheChat(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	base := s.securityNotifications("alice") // linking the first chat already produced one

	// A sign-in from another browser.
	other := s.newClient()
	other.login("alice")
	if !hasNotice(s.noticeTexts(), "New sign-in") || s.securityNotifications("alice") != base+1 {
		t.Fatalf("sign-in notice: %v, %d notifications", s.noticeTexts(), s.securityNotifications("alice"))
	}
	var notes notificationsJSON
	ch.appUser.get("/api/v1/notifications?unread=true", &notes)
	found := false
	for _, n := range notes.Items {
		if n.Kind == "security" && strings.Contains(n.Payload["text"].(string), "New sign-in") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the app must show the notice at next login: %+v", notes)
	}
	// The notice goes to the chat the user linked, and nowhere else.
	for _, it := range s.outboxItems("notice") {
		if it["conversation_id"] != ch.conv || it["external_user_id"] != ch.ext {
			t.Fatalf("notice addressed to %v %v", it["external_user_id"], it["conversation_id"])
		}
	}

	// A share link.
	n := ch.note("", "shared", nil)
	ch.share(n.ID, "1d")
	if !hasNotice(s.noticeTexts(), "share link was created") {
		t.Fatalf("share notice: %v", s.noticeTexts())
	}

	// A password change (changes are always reported).
	if res := ch.c.do("POST", "/api/v1/me/password", map[string]any{"current_password": password, "new_password": "another long passphrase 42"}); res.Status != 204 {
		t.Fatalf("change password: %d %s", res.Status, res.Body)
	}
	if !hasNotice(s.noticeTexts(), "password was changed") {
		t.Fatalf("password notice: %v", s.noticeTexts())
	}

	// Muting the new-session and share notices silences those two, and nothing else.
	if res := ch.c.do("PATCH", "/api/v1/me", map[string]any{"settings": map[string]any{"muted_notices": []string{"session", "share", "password", "identity"}}}); res.Status != 200 {
		t.Fatalf("mute: %d %s", res.Status, res.Body)
	}
	before := len(s.noticeTexts())
	c2 := s.newClient()
	if res := c2.do("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": "another long passphrase 42"}); res.Status != 200 {
		t.Fatalf("login: %d", res.Status)
	}
	ch.share(n.ID, "1h")
	if len(s.noticeTexts()) != before {
		t.Fatalf("muted notices were sent: %v", s.noticeTexts()[before:])
	}
	if res := c2.do("POST", "/api/v1/me/password", map[string]any{"current_password": "another long passphrase 42", "new_password": "yet another long passphrase 43"}); res.Status != 204 {
		t.Fatalf("second change: %d", res.Status)
	}
	if len(s.noticeTexts()) != before+1 {
		t.Fatal("a password change cannot be muted")
	}

	// Chats linked and unlinked cannot be muted either (the mute list above named "identity" and it was ignored).
	var ids struct{ Items []struct{ ID string } }
	c2.do("GET", "/api/v1/me/identities", nil).JSON(t, &ids) // the first browser was signed out by the password change
	before = len(s.noticeTexts())
	if res := c2.do("DELETE", "/api/v1/me/identities/"+ids.Items[0].ID, nil); res.Status != 204 {
		t.Fatalf("unlink: %d", res.Status)
	}
	// The chat being unlinked no longer receives notices, but the app records that it happened.
	if s.count(`select count(*) from notifications where kind = 'security' and payload::text like '%unlinked%'`) != 1 {
		t.Fatal("unlinking must be recorded in the app")
	}
	if len(s.noticeTexts()) != before {
		t.Fatal("a chat that was just unlinked must not be told about it in the chat that is going away")
	}
}

// An activation link for an account that is already set up (a lost password, or a takeover attempt) is
// reported to the owner in the app and in their chats, and so is the reset that follows; a brand-new
// account, which has nobody to tell, produces nothing (AUTH-U11, SEC-AUD-4).
func TestActivationLinkNoticesAreSentForExistingAccountsOnly(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("frank", key)
	s.makeUser("root", true)
	// A brand-new account: created and activated without any notice.
	s.makeUser("newbie", false)
	if s.securityNotifications("newbie") != 0 {
		t.Fatalf("a new account was told about its own creation: %d", s.securityNotifications("newbie"))
	}
	if _, err := s.db.Admin.Exec(t.Context(), `delete from bot_outbox where kind = 'notice'; delete from notifications`); err != nil {
		t.Fatal(err)
	}
	var id string
	_ = s.db.Admin.QueryRow(t.Context(), `select id::text from users where username = 'frank'`).Scan(&id)

	admin := s.newClient()
	admin.login("root")
	res := admin.do("POST", "/api/v1/admin/users/"+id+"/activation-link", nil)
	if res.Status != 201 {
		t.Fatalf("issue: %d %s", res.Status, res.Body)
	}
	if !hasNotice(s.noticeTexts(), "link for setting a new password") || s.securityNotifications("frank") != 1 {
		t.Fatalf("no notice about the link: %v, %d notifications", s.noticeTexts(), s.securityNotifications("frank"))
	}
	for _, it := range s.outboxItems("notice") {
		if it["conversation_id"] != ch.conv {
			t.Fatalf("the notice went to %v", it["conversation_id"])
		}
	}
	var link struct{ Token string }
	res.JSON(t, &link)
	fresh := s.newClient()
	if r := fresh.do("POST", "/api/v1/auth/activate", map[string]any{"token": link.Token, "password": "a brand new long passphrase 7"}); r.Status != 200 {
		t.Fatalf("activate: %d %s", r.Status, r.Body)
	}
	if !hasNotice(s.noticeTexts(), "was reset with a link") || !hasNotice(s.noticeTexts(), "New sign-in") {
		t.Fatalf("no notice about the reset: %v", s.noticeTexts())
	}
}
