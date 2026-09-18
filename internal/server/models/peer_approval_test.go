package models

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPeerApprovalDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Peer{}, &Workspace{}); err != nil {
		t.Fatal(err)
	}

	p := Peer{WorkspaceID: "ws", Name: "n"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if p.ApprovalStatus != ApprovalApproved {
		t.Fatalf("default approval status = %q, want %q", p.ApprovalStatus, ApprovalApproved)
	}

	w := Workspace{Slug: "s", Namespace: "wf-x", DisplayName: "d"}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	if w.RequirePeerApproval {
		t.Fatal("RequirePeerApproval must default to false")
	}
}
