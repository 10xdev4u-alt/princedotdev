package db

import (
	"testing"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
)

func TestDBSmoke(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), StorageBudget: 5 * 1024 * 1024 * 1024}
	d, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	acct, err := d.CreateAccount("Tester", "t@t.dev")
	if err != nil {
		t.Fatal(err)
	}
	if acct == "" {
		t.Fatal("no account id")
	}

	_, token, err := d.CreateAPIKey(acct, "test")
	if err != nil {
		t.Fatal(err)
	}
	k, err := d.FindAPIKeyByToken(token)
	if err != nil || k == nil {
		t.Fatalf("key lookup: %v %v", k, err)
	}
	if k.AccountName != "Tester" {
		t.Fatalf("got %q", k.AccountName)
	}
	if got, _ := d.FindAPIKeyByToken("dd_wrong"); got != nil {
		t.Fatal("wrong token matched")
	}

	team, err := d.CreateTeam("Eng", acct)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.IsTeamMember(team.ID, acct); !ok {
		t.Fatal("owner not member")
	}

	draftID, err := d.CreateDraft(acct, "", "Plan", "", "unlisted")
	if err != nil {
		t.Fatal(err)
	}
	v := Version{DraftID: draftID, ObjectKey: "drafts/x/versions/1.html", ContentHash: "abc", FileSize: 123}
	added, err := d.AddVersion(v, AnonymousKeyID, "1.2.3.4", "curl")
	if err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || added.VersionNumber != 1 {
		t.Fatalf("bad add result: %+v", added)
	}
	if err := d.SetCurrentVersion(draftID, added.ID, "Plan", ""); err != nil {
		t.Fatal(err)
	}
	v2, err := d.GetCurrentVersion(draftID)
	if err != nil || v2 == nil {
		t.Fatalf("current version: %v %v", v2, err)
	}
	if v2.VersionNumber != 1 {
		t.Fatalf("got v%d", v2.VersionNumber)
	}

	if err := d.SetStatus(draftID, "approved"); err != nil {
		t.Fatal(err)
	}
	dr, _ := d.FindDraft(draftID)
	if dr.Status != "approved" {
		t.Fatalf("status %q", dr.Status)
	}

	c, err := d.AddComment(Comment{DraftID: draftID, VersionNumber: 1, Anchor: `{"selector":"#p"}`, Body: "clarify", Author: "Tester"})
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("no comment id")
	}
	cs, _ := d.ListComments(draftID)
	if len(cs) != 1 {
		t.Fatalf("got %d comments", len(cs))
	}

	total, err := d.SumStoredBytes()
	if err != nil {
		t.Fatal(err)
	}
	if total != 123 {
		t.Fatalf("stored bytes %d", total)
	}
}
