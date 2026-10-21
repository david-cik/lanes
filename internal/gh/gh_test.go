package gh

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"
)

func runner(out string, err error) Runner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestFind(t *testing.T) {
	out := `{"number":41,"url":"https://github.com/acme/app/pull/41","state":"OPEN","isDraft":false,"reviewDecision":"APPROVED",
	 "statusCheckRollup":[
	  {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
	  {"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"FAILURE"},
	  {"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":""},
	  {"__typename":"CheckRun","name":"docs","status":"COMPLETED","conclusion":"SKIPPED"},
	  {"__typename":"StatusContext","context":"ci/legacy","state":"ERROR"},
	  {"__typename":"StatusContext","context":"ci/ok","state":"SUCCESS"}]}`
	pr, err := Find(context.Background(), runner(out, nil), "/r", "abc-1/x")
	if err != nil {
		t.Fatal(err)
	}
	want := Checks{Pass: 2, Fail: 2, Pending: 1, Skipped: 1}
	if pr.Number != 41 || pr.Review != "APPROVED" || pr.Checks != want || len(pr.Failed) != 2 || pr.Failed[1] != "ci/legacy" {
		t.Fatalf("%+v", pr)
	}
}

func TestNoPRAndUnavailable(t *testing.T) {
	ctx := context.Background()
	if pr, err := Find(ctx, runner("", fmt.Errorf(`no pull requests found for branch "x"`)), "/r", "x"); pr != nil || err != nil {
		t.Fatalf("no PR: %v %v", pr, err)
	}
	var u Unavailable
	if _, err := Find(ctx, runner("", &exec.Error{Name: "gh", Err: exec.ErrNotFound}), "/r", "x"); !errors.As(err, &u) {
		t.Fatalf("missing gh: %v", err)
	}
	if _, err := Find(ctx, runner("", fmt.Errorf("To get started with GitHub CLI, please run:  gh auth login")), "/r", "x"); !errors.As(err, &u) {
		t.Fatalf("not signed in: %v", err)
	}
	if pr, err := Find(ctx, runner("", nil), "/r", ""); pr != nil || err != nil {
		t.Fatal("detached HEAD should mean no PR")
	}
}

func TestLookup(t *testing.T) {
	var got []string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		got = args
		return []byte(`{"number":12,"title":"Fix it","url":"https://github.com/acme/widget/pull/12"}`), nil
	}
	for _, ref := range []string{"acme/widget#12", "https://github.com/acme/widget/pull/12"} {
		r, err := Lookup(context.Background(), run, ref)
		if err != nil || r.Repo != "acme/widget" || r.Number != 12 || r.Title != "Fix it" {
			t.Fatalf("%s: %+v %v", ref, r, err)
		}
	}
	if !slices.Contains(got, "https://github.com/acme/widget/pull/12") {
		t.Fatalf("%q", got)
	}
	Lookup(context.Background(), run, "acme/widget#12")
	if !slices.Equal(got[:5], []string{"pr", "view", "12", "--repo", "acme/widget"}) {
		t.Fatalf("%q", got)
	}
	if _, err := Lookup(context.Background(), run, "fix the thing"); err == nil {
		t.Fatal("not a PR")
	}
}
