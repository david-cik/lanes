package linear

import (
	"testing"
	"time"
)

const issuesPage = `{"issues":[
 {"id":"ABC-1","title":"Open one","url":"https://linear.example/ABC-1","gitBranchName":"abc-1-open-one","status":"In Review","statusType":"started","team":"Alpha","updatedAt":"2026-01-02T03:04:05.000Z"},
 {"id":"ABC-2","title":"Done one","status":"Done","statusType":"completed","team":"Alpha","updatedAt":"2026-01-02T03:04:05.000Z"},
 {"id":"XYZ-9","title":"Dup","status":"Duplicate","statusType":"duplicate","team":"Xray","updatedAt":"2026-01-02T03:04:05.000Z"},
 {"id":"XYZ-3","title":"Nope","status":"Canceled","statusType":"canceled","team":"Xray","updatedAt":"2026-01-02T03:04:05.000Z"}
],"hasNextPage":true,"cursor":"c1"}`

func TestParseIssuesDropsClosedAndReturnsCursor(t *testing.T) {
	got, next, err := parseIssues([]byte(issuesPage))
	if err != nil {
		t.Fatal(err)
	}
	if next != "c1" || len(got) != 1 {
		t.Fatalf("next=%q issues=%+v", next, got)
	}
	i := got[0]
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if i.Key != "ABC-1" || i.State != "In Review" || i.StateType != "started" ||
		i.Team != "Alpha" || i.BranchName != "abc-1-open-one" || !i.UpdatedAt.Equal(want) {
		t.Fatalf("got %+v", i)
	}
}

func TestParseLastPageHasNoCursor(t *testing.T) {
	_, next, err := parseIssues([]byte(`{"issues":[],"hasNextPage":false,"cursor":"stale"}`))
	if err != nil || next != "" {
		t.Fatalf("next=%q err=%v", next, err)
	}
}

func TestParseUsersDropsInactive(t *testing.T) {
	got, _, err := parseUsers([]byte(`{"users":[
	 {"id":"u1","name":"Ada","email":"ada@example.com","isActive":true},
	 {"id":"u2","name":"Gone","email":"gone@example.com","isActive":false}],"hasNextPage":false}`))
	if err != nil || len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestParseIssueDetail(t *testing.T) {
	d, err := parseIssue([]byte(`{"id":"ABC-7","title":"T","status":"Todo","statusType":"unstarted","team":"Alpha",
	 "description":"Touches ` + "`widget-api`" + `.",
	 "attachments":[{"url":"https://github.com/acme/widget-api/pull/42"},{"url":"https://docs.example/x"},{"url":"https://github.com/acme/widget-api/issues/3"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Key != "ABC-7" || d.Description != "Touches `widget-api`." || len(d.PRURLs) != 1 || d.PRURLs[0] != "https://github.com/acme/widget-api/pull/42" {
		t.Fatalf("got %+v", d)
	}
}
