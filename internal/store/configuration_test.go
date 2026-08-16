package store

import (
	"context"
	"testing"
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
)

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

func now() time.Time { return time.Now() }
