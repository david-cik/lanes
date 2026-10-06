package claude

import (
	"context"
	"os/exec"
	"testing"

	"github.com/david-cik/lanes/internal/agent"
)

func TestListExternal(t *testing.T) {
	a := &Adapter{run: func(context.Context) ([]byte, error) {
		return []byte(`[
		 {"pid":1,"cwd":"/src/app","kind":"interactive","startedAt":1767225600000,"sessionId":"s1","name":"abc-12 work","status":"busy"},
		 {"pid":2,"cwd":"/src","kind":"background","startedAt":1767225600000,"sessionId":"s2","name":"bg","status":"idle","id":"x1","state":"working"},
		 {"pid":3,"cwd":"/src","kind":"interactive","startedAt":0,"sessionId":"s3","name":"odd","status":"weird"},
		 {"pid":4,"cwd":"/src","kind":"interactive","startedAt":0,"sessionId":"s4","name":"ask","status":"waiting"}]`), nil
	}}
	got, err := a.ListExternal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Status != agent.Working || got[1].Status != agent.Idle ||
		got[2].Status != agent.Unknown || got[3].Status != agent.Waiting {
		t.Fatalf("got %+v", got)
	}
	if !got[0].External || got[0].ID != "s1" || got[0].Since.UnixMilli() != 1767225600000 {
		t.Fatalf("got %+v", got[0])
	}
}

func TestMissingBinaryIsEmpty(t *testing.T) {
	a := &Adapter{run: func(context.Context) ([]byte, error) { return nil, exec.ErrNotFound }}
	got, err := a.ListExternal(context.Background())
	if err != nil || got != nil {
		t.Fatalf("got %+v err=%v", got, err)
	}
}
