package qoder

import (
	"testing"
)

func TestLoadDBSessions(t *testing.T) {
	ad := New()
	dbSessions := ad.loadDBSessions()
	if len(dbSessions) == 0 {
		t.Log("No Qoder desktop DB discovered on host (normal in CI/headless)")
		return
	}

	t.Logf("Discovered %d Qoder desktop sessions from DB", len(dbSessions))

	// Check if session e959378f has title "评价仓库设计"
	found := false
	for id, s := range dbSessions {
		if id == "e959378f-57da-496f-93e2-ff7092aa1e02" {
			found = true
			if s.Title != "评价仓库设计" {
				t.Errorf("expected title '评价仓库设计', got '%s'", s.Title)
			} else {
				t.Logf("Verified session %s title: %s", id, s.Title)
			}
			break
		}
	}

	if !found {
		t.Log("Session e959378f not found in DB")
	}
}
