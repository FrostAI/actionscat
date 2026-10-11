package scheduler

import (
	"actionscat/internal/domain"
	"actionscat/internal/frostagent"
	runtimeapi "actionscat/internal/runtime"
	sdk "actionscat/pkg/actionscat"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestScheduleDeliveryThroughRuntimeProxy(t *testing.T) {
	for _, messageType := range []string{"private", "group", "legacy"} {
		t.Run(messageType, func(t *testing.T) {
			st, fs, ss, _, s := setupTestScheduler(t)
			ctx := context.Background()
			act, ver, _ := createActionWithBuild(t, st, fs, "delivery-fixture")
			var delivery *domain.DeliveryContext
			if messageType != "legacy" {
				delivery = &domain.DeliveryContext{Platform: "fixture-platform", AdapterID: "astrbot", MessageType: messageType, TargetID: "fixture-user", UserID: "fixture-user", SessionID: "fixture-session"}
				if messageType == "group" {
					delivery.TargetID = "fixture-group"
					delivery.GroupID = "fixture-group"
				}
			}
			// An Action's optional state injection must not erase trusted schedule routing.
			ver.StateInjections = []domain.StateInjection{{StatePath: "missing.txt", EnvVar: "ACTIONSCAT_EVENT_TARGET_ID", Optional: true}}
			// A second version carries the injection declared before activation.
			ver.ID = "injected-version"
			ver.VersionNumber = 2
			if err := st.CreateVersion(ctx, ver); err != nil {
				t.Fatal(err)
			}
			active, err := st.GetAction(ctx, act.ID)
			if err != nil {
				t.Fatal(err)
			}
			bld, err := st.GetBuild(ctx, active.ActiveBuildID)
			if err != nil {
				t.Fatal(err)
			}
			bld.ID = "injected-build"
			bld.VersionID = ver.ID
			if err := st.CreateBuild(ctx, bld); err != nil {
				t.Fatal(err)
			}
			if err := st.SetActiveBuild(ctx, act.ID, ver.ID, bld.ID); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			sched := &domain.Schedule{ID: "fixture-schedule", ActionID: act.ID, CronExpr: "* * * * *", Timezone: "UTC", Enabled: true, NextRunAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now, DeliveryContext: delivery}
			if err := st.CreateSchedule(ctx, sched); err != nil {
				t.Fatal(err)
			}
			s.Tick(ctx, now)
			s.Tick(ctx, now)
			runs, err := st.ListRuns(ctx, act.ID, 10, 0)
			if err != nil || len(runs) != 1 {
				t.Fatalf("schedule did not persist one run: %v %+v", err, runs)
			}
			run := runs[0]
			if run.TriggerType != domain.TriggerTypeSchedule || run.TriggerMetadata["schedule_id"] != sched.ID {
				t.Fatalf("lost trigger metadata: %+v", run)
			}
			for k, v := range delivery.Environment() {
				if run.PlannedEnv[k] != v {
					t.Fatalf("%s changed: %q", k, run.PlannedEnv[k])
				}
			}
			if err := st.MarkRunRunning(ctx, run.ID, now); err != nil {
				t.Fatal(err)
			}
			raw, hash, err := domain.GenerateRawToken()
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CreateRunToken(ctx, &domain.RunToken{ID: "fixture-token", TokenHash: hash, RunID: run.ID, ActionID: act.ID, Scopes: []string{domain.ScopeFrostAgentSendMsg}, ExpiresAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			received := make(chan frostagent.SendMessageRequest, 1)
			fa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-proxy-secret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				var req frostagent.SendMessageRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				received <- req
				w.WriteHeader(http.StatusOK)
			}))
			defer fa.Close()
			gin.SetMode(gin.TestMode)
			router := gin.New()
			runtimeapi.NewAPI(st, ss, frostagent.NewHTTPClient(frostagent.Config{BaseURL: fa.URL, APIKey: "fixture-proxy-secret"})).RegisterRoutes(router.Group("/api/v1/runtime"))
			core := httptest.NewServer(router)
			defer core.Close()
			for _, k := range []string{"ACTIONSCAT_EVENT_PLATFORM", "ACTIONSCAT_EVENT_ADAPTER_ID", "ACTIONSCAT_EVENT_MESSAGE_TYPE", "ACTIONSCAT_EVENT_TARGET_ID", "ACTIONSCAT_EVENT_SESSION_ID", "ACTIONSCAT_EVENT_USER_ID", "ACTIONSCAT_EVENT_GROUP_ID"} {
				t.Setenv(k, "")
			}
			for k, v := range run.PlannedEnv {
				t.Setenv(k, v)
			}
			t.Setenv("ACTIONSCAT_RUNTIME_ENDPOINT", core.URL+"/api/v1/runtime")
			t.Setenv("ACTIONSCAT_RUNTIME_TOKEN", raw)
			err = sdk.Reply(ctx, "scheduled fixture result")
			if delivery == nil {
				if !errors.Is(err, sdk.ErrNoSession) {
					t.Fatalf("legacy schedule invented destination: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case req := <-received:
				if req.Platform != delivery.Platform || req.TargetID != delivery.TargetID || req.MessageType != delivery.MessageType || req.Session != delivery.SessionID || !reflect.DeepEqual(req.Metadata, map[string]any{"adapter_id": delivery.AdapterID}) || len(req.Messages) != 1 || req.Messages[0].Text != "scheduled fixture result" {
					t.Fatalf("wrong destination/content: %+v", req)
				}
			case <-time.After(time.Second):
				t.Fatal("proxy did not deliver")
			}
		})
	}
}
