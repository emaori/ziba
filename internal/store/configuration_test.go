package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

func TestStringSliceStoresOmittedValuesAsEmptyArray(t *testing.T) {
	if got := stringSlice(nil); got == nil || len(got) != 0 {
		t.Fatalf("stringSlice(nil) = %#v, want non-nil empty slice", got)
	}
	want := []string{"LLMs", "agents"}
	if got := stringSlice(want); !reflect.DeepEqual(got, want) {
		t.Fatalf("stringSlice(values) = %#v, want %#v", got, want)
	}
}

func TestConfigurationPreservesExistingSourceData(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	sourceID := seedSource(t, db, "existing", nil)
	articleID := seedArticle(t, db, sourceID, "https://example.com/article", 80, []string{"AI"}, now())

	interests := config.Interests{Threshold: 65, Topics: []config.Interest{{Topic: "AI", Priority: 1, Note: "Practical work"}}}
	sources := []domain.Source{{ID: sourceID, Name: "Renamed", Type: domain.SourceTypeRSS, URL: "https://example.com/existing", Enabled: true}}
	if err := db.SaveConfiguration(ctx, interests, sources); err != nil {
		t.Fatalf("SaveConfiguration: %v", err)
	}

	got, err := db.Configuration(ctx)
	if err != nil {
		t.Fatalf("Configuration: %v", err)
	}
	if !got.Configured || got.Interests.Threshold != 65 || len(got.Interests.Topics) != 1 {
		t.Errorf("configuration = %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0].ID != sourceID || got.Sources[0].Name != "Renamed" {
		t.Errorf("sources = %+v", got.Sources)
	}
	var storedSource int64
	if err := db.pool.QueryRow(ctx, `SELECT source_id FROM articles WHERE id=$1`, articleID).Scan(&storedSource); err != nil {
		t.Fatalf("read article: %v", err)
	}
	if storedSource != sourceID {
		t.Errorf("article source = %d, want preserved %d", storedSource, sourceID)
	}
}

func TestNewsletterCredentialsRoundTripInternally(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}}
	sources := []domain.Source{{Name: "Mail", Type: domain.SourceTypeNewsletter, URL: "imaps://mail.example/INBOX", Enabled: true, Newsletter: &domain.NewsletterOptions{Folder: "INBOX", Username: "reader", Password: "secret", LookBackDays: 2}}}
	if err := db.SaveConfiguration(ctx, interests, sources); err != nil {
		t.Fatalf("SaveConfiguration: %v", err)
	}
	got, err := db.Configuration(ctx)
	if err != nil {
		t.Fatalf("Configuration: %v", err)
	}
	if got.Sources[0].Newsletter.Username != "reader" || got.Sources[0].Newsletter.Password != "secret" {
		t.Error("newsletter credentials were not retained for collection")
	}
}

func TestScheduleImportsOnceThenBelongsToDatabase(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `UPDATE app_settings SET collect_every=NULL, collect_at=NULL WHERE singleton`); err != nil {
		t.Fatalf("clear schedule: %v", err)
	}
	if err := db.InitializeSchedule(ctx, "3h", "05:30"); err != nil {
		t.Fatalf("InitializeSchedule: %v", err)
	}
	if err := db.InitializeSchedule(ctx, "not-a-duration", "not-a-time"); err != nil {
		t.Fatalf("second InitializeSchedule: %v", err)
	}
	got, err := db.Configuration(ctx)
	if err != nil {
		t.Fatalf("Configuration: %v", err)
	}
	if got.Schedule.Every != 3*time.Hour || got.Schedule.At.String() != "05:30" {
		t.Fatalf("imported schedule = %v at %v, want 3h at 05:30", got.Schedule.Every, got.Schedule.At)
	}

	want, _ := config.ParseCollectionSchedule("8h", "06:15")
	if err := db.SaveSchedule(ctx, want); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	got, err = db.Configuration(ctx)
	if err != nil {
		t.Fatalf("Configuration after save: %v", err)
	}
	if got.Schedule != want {
		t.Errorf("saved schedule = %+v, want %+v", got.Schedule, want)
	}
}

func TestFinishSetupQueuesFirstCollectionOnce(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	interests := config.Interests{Threshold: 60, Topics: []config.Interest{{Topic: "AI", Priority: 1}}}
	if err := db.FinishSetup(ctx, interests, nil, true); err != nil {
		t.Fatalf("FinishSetup: %v", err)
	}
	running, _, err := db.CollectionState(ctx)
	if err != nil || !running {
		t.Fatalf("state before scheduler claim = %v, %v; want collecting", running, err)
	}
	requested, err := db.ClaimCollectionRequest(ctx)
	if err != nil || !requested {
		t.Fatalf("first claim = %v, %v; want true", requested, err)
	}
	requested, err = db.ClaimCollectionRequest(ctx)
	if err != nil || requested {
		t.Fatalf("second claim = %v, %v; want false", requested, err)
	}
	running, _, err = db.CollectionState(ctx)
	if err != nil || !running {
		t.Fatalf("state after request is claimed = %v, %v; want collecting", running, err)
	}
	db.EndCollection()
	running, _, err = db.CollectionState(ctx)
	if err != nil || running {
		t.Fatalf("state after collection ends = %v, %v; want idle", running, err)
	}
}

func now() time.Time { return time.Now() }
