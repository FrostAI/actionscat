package api_test

import (
	"actionscat/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestManagementScheduleDeliveryOptionalAndValidated(t *testing.T) {
	srv, router := setupTestServer(t)
	now := time.Now().UTC()
	act := &domain.Action{ID: "delivery-api-fixture", Name: "fixture", Enabled: true, MaxConcurrency: 1, CreatedAt: now, UpdatedAt: now}
	if err := srv.Store.CreateAction(context.Background(), act); err != nil {
		t.Fatal(err)
	}
	valid := &domain.DeliveryContext{Platform: "fixture-platform", MessageType: "private", TargetID: "fixture-user", UserID: "fixture-user"}
	cases := []struct {
		name     string
		delivery *domain.DeliveryContext
		status   int
	}{
		{"legacy", nil, http.StatusCreated},
		{"private", valid, http.StatusCreated},
		{"missing platform", &domain.DeliveryContext{MessageType: "group", TargetID: "fixture-group"}, http.StatusBadRequest},
		{"inconsistent private", &domain.DeliveryContext{Platform: "fixture-platform", MessageType: "private", TargetID: "fixture-user", GroupID: "fixture-group"}, http.StatusBadRequest},
		{"invalid type", &domain.DeliveryContext{Platform: "fixture-platform", MessageType: "broadcast", TargetID: "fixture-target"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"cron_expr": "* * * * *", "timezone": "UTC", "enabled": true, "delivery_context": tc.delivery})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/"+act.ID+"/schedules", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			authReq(req)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == http.StatusCreated {
				var created domain.Schedule
				if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				persisted, err := srv.Store.GetSchedule(context.Background(), created.ID)
				if err != nil || !reflect.DeepEqual(persisted.DeliveryContext, tc.delivery) {
					t.Fatalf("API lost delivery: %v %+v", err, persisted)
				}
			}
		})
	}
	list, err := srv.Store.ListSchedules(context.Background(), act.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("invalid requests persisted schedules: %v %d", err, len(list))
	}
}
