package api

import (
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	runtimeapi "actionscat/internal/runtime"
	"actionscat/internal/store"
	sdk "actionscat/pkg/actionscat"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDispatchMatcherReplyPreservesAdapterRouting(t *testing.T) {
	for _, tc := range []struct {
		name           string
		adapterID      string
		platform       string
		groupID        string
		legacyBody     bool
		stateInjection string
	}{
		{name: "astrbot_group", adapterID: "astrbot", platform: "fixture-platform", groupID: "fixture-group"},
		{name: "astrbot_private", adapterID: "astrbot", platform: "fixture-platform"},
		{name: "modern_without_adapter", platform: "qq", groupID: "fixture-group"},
		{name: "legacy_without_adapter", platform: "qq", groupID: "fixture-group", legacyBody: true},
		{name: "astrbot_with_state_injection", adapterID: "astrbot", platform: "fixture-platform", groupID: "fixture-group", stateInjection: "present"},
		{name: "astrbot_with_missing_optional_state", adapterID: "astrbot", platform: "fixture-platform", stateInjection: "missing"},
		{name: "legacy_with_state_injection", platform: "qq", groupID: "fixture-group", legacyBody: true, stateInjection: "present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, fs, router := setupDispatchTest(t)
			ctx := t.Context()
			now := time.Now().UTC()
			var injections []domain.StateInjection
			if tc.stateInjection != "" {
				injections = []domain.StateInjection{{StatePath: "adapter.txt", EnvVar: "ACTIONSCAT_EVENT_ADAPTER_ID", Optional: true}}
			}
			createDispatchAction(t, st, fs, "fixture-action", injections...)
			if tc.stateInjection == "present" {
				if err := store.NewStateStore(fs.DataDir()).WriteState("fixture-action", "adapter.txt", []byte("forged-state-adapter")); err != nil {
					t.Fatal(err)
				}
			}
			m := &domain.Matcher{
				ID:          "fixture-matcher",
				ActionID:    "fixture-action",
				Name:        "Fixture matcher",
				MatchType:   domain.MatchTypeRegex,
				Pattern:     `^ping (?P<adapter>\S+) (?P<value>\S+)$`,
				TargetField: "text",
				CaptureEnvMap: map[string]string{
					"adapter": "ACTIONSCAT_EVENT_ADAPTER_ID",
					"value":   "PARAM_VALUE",
				},
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := st.CreateMatcher(ctx, m); err != nil {
				t.Fatal(err)
			}
			event := domain.MessageEvent{
				Text:      "ping forged-capture-adapter fixture-value",
				Platform:  tc.platform,
				AdapterID: tc.adapterID,
				SessionID: "fixture-session",
				UserID:    "fixture-user",
				GroupID:   tc.groupID,
				Metadata:  map[string]string{"adapter_id": "forged-metadata-adapter"},
			}
			var payload any = event
			if tc.legacyBody {
				payload = map[string]any{
					"sender_qq":     event.UserID,
					"current_group": event.GroupID,
					"raw_msg":       event.Text,
					"metadata":      event.Metadata,
				}
				event.SessionID = event.GroupID + ":" + event.UserID
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/dispatch", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-dispatch-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("dispatch status %d: %s", response.Code, response.Body.String())
			}
			var dispatched DispatchResponse
			if err := json.Unmarshal(response.Body.Bytes(), &dispatched); err != nil {
				t.Fatal(err)
			}
			if !dispatched.OK || !dispatched.Matched || len(dispatched.RunIDs) != 1 {
				t.Fatalf("expected one matched run: %+v", dispatched)
			}
			run, err := st.GetRun(ctx, dispatched.RunIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if run.TriggerType != domain.TriggerTypeMatcher || run.TriggerMetadata["matcher_id"] != m.ID {
				t.Fatalf("lost matcher trigger: %+v", run)
			}
			for key, want := range map[string]string{
				"ACTIONSCAT_EVENT_TEXT":       event.Text,
				"ACTIONSCAT_EVENT_PLATFORM":   event.Platform,
				"ACTIONSCAT_EVENT_ADAPTER_ID": event.AdapterID,
				"ACTIONSCAT_EVENT_SESSION_ID": event.SessionID,
				"ACTIONSCAT_EVENT_USER_ID":    event.UserID,
				"ACTIONSCAT_EVENT_GROUP_ID":   event.GroupID,
				"PARAM_VALUE":                 "fixture-value",
			} {
				if got, exists := run.PlannedEnv[key]; !exists || got != want {
					t.Fatalf("planned %s = %q (present=%v), want %q", key, got, exists, want)
				}
			}

			// Exercise the persisted event environment through the SDK and real
			// runtime capability proxy without requiring a live IM or sandbox.
			if err := st.MarkRunRunning(ctx, run.ID, now); err != nil {
				t.Fatal(err)
			}
			rawToken, hash, err := domain.GenerateRawToken()
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CreateRunToken(ctx, &domain.RunToken{
				ID: "fixture-token", TokenHash: hash, RunID: run.ID, ActionID: run.ActionID,
				Scopes: []string{domain.ScopeFrostAgentSendMsg}, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			received := make(chan frostagent.SendMessageRequest, 1)
			fa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/messages/send" || r.Header.Get("Authorization") != "Bearer fixture-proxy-secret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				var request frostagent.SendMessageRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				received <- request
				w.WriteHeader(http.StatusOK)
			}))
			defer fa.Close()
			runtimeapi.NewAPI(st, store.NewStateStore(t.TempDir()), frostagent.NewHTTPClient(frostagent.Config{
				BaseURL: fa.URL, APIKey: "fixture-proxy-secret",
			})).RegisterRoutes(router.Group("/api/v1/runtime"))
			core := httptest.NewServer(router)
			defer core.Close()
			for _, key := range []string{"ACTIONSCAT_EVENT_MESSAGE_TYPE", "ACTIONSCAT_EVENT_TARGET_ID"} {
				t.Setenv(key, "")
			}
			for key, value := range run.PlannedEnv {
				t.Setenv(key, value)
			}
			t.Setenv("ACTIONSCAT_RUNTIME_ENDPOINT", core.URL+"/api/v1/runtime")
			t.Setenv("ACTIONSCAT_RUNTIME_TOKEN", rawToken)
			if err := sdk.Reply(ctx, "matched fixture result"); err != nil {
				t.Fatal(err)
			}
			select {
			case delivered := <-received:
				wantType, wantTarget := "private", event.UserID
				if event.GroupID != "" {
					wantType, wantTarget = "group", event.GroupID
				}
				if delivered.Platform != event.Platform || delivered.MessageType != wantType || delivered.TargetID != wantTarget || delivered.Session != event.SessionID {
					t.Fatalf("wrong callback destination: %+v", delivered)
				}
				if event.AdapterID == "" {
					if len(delivered.Metadata) != 0 {
						t.Fatalf("legacy event invented adapter metadata: %+v", delivered.Metadata)
					}
				} else if len(delivered.Metadata) != 1 || delivered.Metadata["adapter_id"] != event.AdapterID {
					t.Fatalf("wrong callback adapter metadata: %+v", delivered.Metadata)
				}
				if len(delivered.Messages) != 1 || delivered.Messages[0].Text != "matched fixture result" {
					t.Fatalf("wrong callback content: %+v", delivered.Messages)
				}
			case <-time.After(time.Second):
				t.Fatal("runtime proxy did not deliver matcher reply")
			}
		})
	}
}
