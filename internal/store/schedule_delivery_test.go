package store

import (
	"actionscat/internal/domain"
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScheduleDeliveryMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := strings.Replace(schemaDDL, ",\n\tdelivery_context_json TEXT NOT NULL DEFAULT 'null'", "", 1)
	if legacyDDL == schemaDDL {
		t.Fatal("legacy fixture still has delivery column")
	}
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	st := NewSQLiteStore(db)
	if err := st.CreateAction(ctx, &domain.Action{ID: "fixture-action", Name: "fixture", Enabled: true, MaxConcurrency: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schedules (id,action_id,cron_expr,timezone,next_run_at,enabled,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, "legacy-schedule", "fixture-action", "* * * * *", "UTC", now, true, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.DeliveryContext{Platform: "fixture-platform", AdapterID: "astrbot", MessageType: "group", TargetID: "fixture-group", SessionID: "fixture-session", UserID: "fixture-user", GroupID: "fixture-group"}
	for pass := range 3 {
		db, err = OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		st = NewSQLiteStore(db)
		sched, err := st.GetSchedule(ctx, "legacy-schedule")
		if err != nil {
			t.Fatal(err)
		}
		if sched.CronExpr != "* * * * *" || !sched.Enabled || !sched.NextRunAt.Equal(now) {
			t.Fatalf("migration changed legacy schedule: %+v", sched)
		}
		if pass == 0 {
			if sched.DeliveryContext != nil {
				t.Fatalf("legacy context should be absent: %+v", sched)
			}
			sched.DeliveryContext = delivery
			if err := st.UpdateSchedule(ctx, sched); err != nil {
				t.Fatal(err)
			}
		} else if !reflect.DeepEqual(sched.DeliveryContext, delivery) {
			t.Fatalf("context lost after reopen: %+v", sched)
		}
		all, err := st.ListSchedules(ctx, "fixture-action")
		if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0].DeliveryContext, delivery) {
			t.Fatalf("list context: %v %+v", err, all)
		}
		due, err := st.ListDueSchedules(ctx, now.Add(time.Second), 10)
		if err != nil || len(due) != 1 || !reflect.DeepEqual(due[0].DeliveryContext, delivery) {
			t.Fatalf("due context: %v %+v", err, due)
		}
		sched.DeliveryContext = &domain.DeliveryContext{Platform: "fixture-platform", MessageType: "private", TargetID: "fixture-user", GroupID: "wrong-group"}
		if err := st.UpdateSchedule(ctx, sched); err == nil {
			t.Fatal("inconsistent target accepted")
		}
		persisted, err := st.GetSchedule(ctx, sched.ID)
		if err != nil || !reflect.DeepEqual(persisted.DeliveryContext, delivery) {
			t.Fatalf("invalid update changed context: %v %+v", err, persisted)
		}
		sched.ID = "invalid-new"
		if err := st.CreateSchedule(ctx, sched); err == nil {
			t.Fatal("invalid create accepted")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
