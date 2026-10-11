package build

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestEmbeddedSDKReplyUsesScheduleRouting(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/frostagent/send" || r.Header.Get("Authorization") != "Bearer fixture-runtime-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- request
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dir := t.TempDir()
	files := InjectSDK(map[string][]byte{
		"go.mod":  []byte("module fixture\n\ngo 1.25.3\n"),
		"main.go": []byte("package main\nimport(\"context\";\"actionscat/pkg/actionscat\")\nfunc main(){if err:=actionscat.Reply(context.Background(),\"fixture result\");err!=nil{panic(err)}}\n"),
	})
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off", "GO111MODULE=on", "ACTIONSCAT_RUNTIME_ENDPOINT="+server.URL, "ACTIONSCAT_RUNTIME_TOKEN=fixture-runtime-token", "ACTIONSCAT_EVENT_PLATFORM=fixture-platform", "ACTIONSCAT_EVENT_ADAPTER_ID=astrbot", "ACTIONSCAT_EVENT_MESSAGE_TYPE=private", "ACTIONSCAT_EVENT_TARGET_ID=fixture-private", "ACTIONSCAT_EVENT_USER_ID=ambient-other-user", "ACTIONSCAT_EVENT_GROUP_ID=ambient-other-group", "ACTIONSCAT_EVENT_SESSION_ID=fixture-session")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("embedded SDK execution: %v\n%s", err, out)
	}
	select {
	case req := <-received:
		metadata, ok := req["metadata"].(map[string]any)
		if !ok || metadata["adapter_id"] != "astrbot" || req["platform"] != "fixture-platform" || req["message_type"] != "private" || req["target_id"] != "fixture-private" || req["session"] != "fixture-session" {
			t.Fatalf("embedded SDK ignored schedule: %+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("embedded SDK made no request")
	}
}
