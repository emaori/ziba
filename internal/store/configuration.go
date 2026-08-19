package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/linkwarden"
)

// Configuration is the complete user-owned configuration used by one run.
type Configuration struct {
	Configured bool
	Interests  config.Interests
	Sources    []domain.Source
	Schedule   config.CollectionSchedule
	Linkwarden linkwarden.Configuration
}

// InitializeSchedule imports the retired environment settings exactly once.
// Empty legacy values become the current defaults. Concurrent starts are safe:
// the first update fills both NULL columns and later calls leave them alone.
func (s *Store) InitializeSchedule(ctx context.Context, legacyEvery, legacyAt string) error {
	var initialized bool
	if err := s.pool.QueryRow(ctx, `
		SELECT collect_every IS NOT NULL AND collect_at IS NOT NULL
		FROM app_settings WHERE singleton`).Scan(&initialized); err != nil {
		return fmt.Errorf("check collection schedule: %w", err)
	}
	if initialized {
		return nil
	}
	schedule, err := config.ParseCollectionSchedule(legacyEvery, legacyAt)
	if err != nil {
		return fmt.Errorf("import legacy schedule: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE app_settings
		SET collect_every=COALESCE(collect_every, $1),
		    collect_at=COALESCE(collect_at, $2), updated_at=now()
		WHERE singleton`, schedule.Every.String(), schedule.At.String())
	if err != nil {
		return fmt.Errorf("initialize collection schedule: %w", err)
	}
	return nil
}

// SaveSchedule validates and stores the live collection schedule.
func (s *Store) SaveSchedule(ctx context.Context, schedule config.CollectionSchedule) error {
	validated, err := config.ParseCollectionSchedule(schedule.Every.String(), schedule.At.String())
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE app_settings SET collect_every=$1, collect_at=$2, updated_at=now()
		WHERE singleton`, validated.Every.String(), validated.At.String())
	if err != nil {
		return fmt.Errorf("save collection schedule: %w", err)
	}
	return nil
}

// SaveSetupInterests persists the first wizard step without enabling Ziba.
func (s *Store) SaveSetupInterests(ctx context.Context, interests config.Interests) error {
	if err := config.ValidateInterests(interests); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin setup interests: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM interests`); err != nil {
		return fmt.Errorf("clear setup interests: %w", err)
	}
	for position, interest := range interests.Topics {
		if _, err := tx.Exec(ctx, `INSERT INTO interests (topic, priority, subtopics, note, position) VALUES ($1,$2,$3,$4,$5)`,
			interest.Topic, interest.Priority, stringSlice(interest.Subtopics), interest.Note, position); err != nil {
			return fmt.Errorf("save setup interest %q: %w", interest.Topic, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE app_settings SET threshold=$1, updated_at=now() WHERE singleton`, interests.Threshold); err != nil {
		return fmt.Errorf("save setup threshold: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit setup interests: %w", err)
	}
	return nil
}

// SaveSetupSources persists wizard source drafts without enabling Ziba.
func (s *Store) SaveSetupSources(ctx context.Context, interests config.Interests, sources []domain.Source) error {
	return s.saveConfiguration(ctx, interests, sources, false, false)
}

// DeleteSetupSource removes a draft created before setup completed. Collection
// cannot run in setup mode, so a draft source cannot own collected articles.
// The configured guard keeps this operation unavailable after setup.
func (s *Store) DeleteSetupSource(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sources
		WHERE id=$1 AND NOT (SELECT configured FROM app_settings WHERE singleton)`, id)
	if err != nil {
		return fmt.Errorf("delete setup source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("setup source not found")
	}
	return nil
}

// Configuration returns one consistent snapshot from PostgreSQL.
func (s *Store) Configuration(ctx context.Context) (Configuration, error) {
	var out Configuration
	var every, at sql.NullString
	if err := s.pool.QueryRow(ctx, `
		SELECT configured, threshold, collect_every, collect_at,
		       linkwarden_enabled, linkwarden_url, linkwarden_auth,
		       linkwarden_username, linkwarden_password, linkwarden_token
		FROM app_settings WHERE singleton`).Scan(
		&out.Configured, &out.Interests.Threshold, &every, &at,
		&out.Linkwarden.Enabled, &out.Linkwarden.URL, &out.Linkwarden.Auth,
		&out.Linkwarden.Username, &out.Linkwarden.Password, &out.Linkwarden.Token); err != nil {
		return out, fmt.Errorf("read application settings: %w", err)
	}
	if every.Valid && at.Valid {
		var err error
		out.Schedule, err = config.ParseCollectionSchedule(every.String, at.String)
		if err != nil {
			return out, fmt.Errorf("read collection schedule: %w", err)
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT topic, priority, subtopics, note
		FROM interests ORDER BY position, id`)
	if err != nil {
		return out, fmt.Errorf("query interests: %w", err)
	}
	out.Interests.Topics, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (config.Interest, error) {
		var interest config.Interest
		err := row.Scan(&interest.Topic, &interest.Priority, &interest.Subtopics, &interest.Note)
		return interest, err
	})
	if err != nil {
		return out, fmt.Errorf("read interests: %w", err)
	}

	rows, err = s.pool.Query(ctx, `
		SELECT id, name, type, url, enabled, created_at, categories, roundup,
		       collect_from, newsletter_folder, newsletter_username,
		       newsletter_password, newsletter_days, newsletter_max
		FROM sources ORDER BY name, id`)
	if err != nil {
		return out, fmt.Errorf("query configured sources: %w", err)
	}
	out.Sources, err = pgx.CollectRows(rows, scanConfiguredSource)
	if err != nil {
		return out, fmt.Errorf("read configured sources: %w", err)
	}
	return out, nil
}

// SaveLinkwarden stores the integration independently from article collection
// configuration. Blank secret fields preserve the existing write-only values.
func (s *Store) SaveLinkwarden(ctx context.Context, in linkwarden.Configuration) error {
	var password, token string
	if err := s.pool.QueryRow(ctx, `
		SELECT linkwarden_password, linkwarden_token
		FROM app_settings WHERE singleton`).Scan(&password, &token); err != nil {
		return fmt.Errorf("read Linkwarden secrets: %w", err)
	}
	if in.Password == "" {
		in.Password = password
	}
	if in.Token == "" {
		in.Token = token
	}
	if err := in.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE app_settings
		SET linkwarden_enabled=$1, linkwarden_url=$2, linkwarden_auth=$3,
		    linkwarden_username=$4, linkwarden_password=$5,
		    linkwarden_token=$6, updated_at=now()
		WHERE singleton`, in.Enabled, strings.TrimRight(strings.TrimSpace(in.URL), "/"),
		in.Auth, strings.TrimSpace(in.Username), in.Password, strings.TrimSpace(in.Token))
	if err != nil {
		return fmt.Errorf("save Linkwarden configuration: %w", err)
	}
	return nil
}

func scanConfiguredSource(row pgx.CollectableRow) (domain.Source, error) {
	var src domain.Source
	var sourceType, collectFrom, folder, username, password string
	var days, maxMessages int
	err := row.Scan(&src.ID, &src.Name, &sourceType, &src.URL, &src.Enabled,
		&src.CreatedAt, &src.Categories, &src.Roundup, &collectFrom, &folder,
		&username, &password, &days, &maxMessages)
	if err != nil {
		return src, err
	}
	src.Type = domain.SourceType(sourceType)
	src.CollectFrom, err = config.ParseCollectFrom(collectFrom)
	if err != nil {
		return src, err
	}
	if src.Type == domain.SourceTypeNewsletter {
		src.Newsletter = &domain.NewsletterOptions{
			Folder: folder, Username: username, Password: password,
			LookBackDays: days, MaxMessages: maxMessages,
		}
	}
	return src, nil
}

// SaveConfiguration atomically replaces interests and upserts sources. Source
// IDs are retained by their natural key, preserving every existing article.
func (s *Store) SaveConfiguration(ctx context.Context, interests config.Interests, sources []domain.Source) error {
	return s.saveConfiguration(ctx, interests, sources, true, false)
}

// FinishSetup enables Ziba and optionally queues one immediate first run in the
// same transaction, so a restart between those actions cannot lose the request.
func (s *Store) FinishSetup(ctx context.Context, interests config.Interests, sources []domain.Source, collectNow bool) error {
	return s.saveConfiguration(ctx, interests, sources, true, collectNow)
}

func (s *Store) saveConfiguration(ctx context.Context, interests config.Interests, sources []domain.Source, configured, collectNow bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin configuration update: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM interests`); err != nil {
		return fmt.Errorf("clear interests: %w", err)
	}
	for position, interest := range interests.Topics {
		if _, err := tx.Exec(ctx, `
			INSERT INTO interests (topic, priority, subtopics, note, position)
			VALUES ($1, $2, $3, $4, $5)`, interest.Topic, interest.Priority,
			stringSlice(interest.Subtopics), interest.Note, position); err != nil {
			return fmt.Errorf("save interest %q: %w", interest.Topic, err)
		}
	}
	keep := make([]int64, 0, len(sources))
	for _, src := range sources {
		collectFrom := formatCollectFrom(src.CollectFrom)
		folder, username, password, days, maxMessages := "", "", "", 1, 0
		if src.Newsletter != nil {
			folder, username, password = src.Newsletter.Folder, src.Newsletter.Username, src.Newsletter.Password
			days, maxMessages = src.Newsletter.LookBackDays, src.Newsletter.MaxMessages
		}
		categories := src.Categories
		if categories == nil {
			categories = []string{}
		}
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO sources
			    (name, type, url, enabled, categories, roundup, collect_from,
			     newsletter_folder, newsletter_username, newsletter_password,
			     newsletter_days, newsletter_max)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (type, url) DO UPDATE SET
			    name=EXCLUDED.name, enabled=EXCLUDED.enabled,
			    categories=EXCLUDED.categories, roundup=EXCLUDED.roundup,
			    collect_from=EXCLUDED.collect_from,
			    newsletter_folder=EXCLUDED.newsletter_folder,
			    newsletter_username=EXCLUDED.newsletter_username,
			    newsletter_password=EXCLUDED.newsletter_password,
			    newsletter_days=EXCLUDED.newsletter_days,
			    newsletter_max=EXCLUDED.newsletter_max
			RETURNING id`, src.Name, string(src.Type), src.URL, src.Enabled,
			categories, src.Roundup, collectFrom, folder, username, password,
			days, maxMessages).Scan(&id)
		if err != nil {
			return fmt.Errorf("save source %q: %w", src.Name, err)
		}
		keep = append(keep, id)
	}
	if len(keep) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE sources SET enabled=FALSE WHERE NOT (id=ANY($1))`, keep); err != nil {
			return fmt.Errorf("disable removed sources: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE app_settings SET configured=$2, threshold=$1,
		collection_requested=collection_requested OR $3, updated_at=now()
		WHERE singleton`, interests.Threshold, configured, collectNow); err != nil {
		return fmt.Errorf("save application settings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit configuration: %w", err)
	}
	return nil
}

// stringSlice translates an omitted form list into PostgreSQL's empty array
// rather than SQL NULL.
func stringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// ClaimCollectionRequest atomically consumes the one-time request made by the
// setup wizard. Only one scheduler can receive true.
func (s *Store) ClaimCollectionRequest(ctx context.Context) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE app_settings SET collection_requested=FALSE
		WHERE singleton AND configured AND collection_requested`)
	if err != nil {
		return false, fmt.Errorf("claim collection request: %w", err)
	}
	claimed := tag.RowsAffected() == 1
	if claimed {
		s.collectionRunning.Store(true)
	}
	return claimed, nil
}

func formatCollectFrom(value domain.CollectFrom) string {
	switch {
	case value.All:
		return "all"
	case !value.Date.IsZero():
		return value.Date.Format(time.DateOnly)
	case value.Grace > 0 && value.Grace%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(value.Grace/(24*time.Hour)))
	case value.Grace > 0:
		return value.Grace.String()
	default:
		return ""
	}
}

// SourceInput is the write model used by setup and Settings forms.
type SourceInput struct {
	ID                           int64
	Name, Type, URL, CollectFrom string
	Enabled, Roundup             bool
	Categories                   []string
	Folder, Username, Password   string
	Days, MaxMessages            int
}

// ValidateSourceInput converts a form into the same domain model used by jobs.
func ValidateSourceInput(in SourceInput, interests config.Interests, existing *domain.Source) (domain.Source, error) {
	known := make(map[string]bool, len(interests.Topics))
	for _, interest := range interests.Topics {
		known[interest.Topic] = true
	}
	if strings.TrimSpace(in.Name) == "" {
		return domain.Source{}, fmt.Errorf("name is required")
	}
	typ := domain.SourceType(in.Type)
	if typ != domain.SourceTypeRSS && typ != domain.SourceTypeNewsletter {
		return domain.Source{}, fmt.Errorf("type must be RSS or newsletter")
	}
	address, err := config.SourceAddress(typ, in.URL)
	if err != nil {
		return domain.Source{}, err
	}
	var cf domain.CollectFrom
	if typ == domain.SourceTypeRSS {
		cf, err = config.ParseCollectFrom(in.CollectFrom)
		if err != nil {
			return domain.Source{}, fmt.Errorf("collect from: %w", err)
		}
	}
	for _, category := range in.Categories {
		if !known[category] {
			return domain.Source{}, fmt.Errorf("unknown interest %q", category)
		}
	}
	if in.Roundup && typ != domain.SourceTypeRSS {
		in.Roundup = false
	}
	src := domain.Source{ID: in.ID, Name: strings.TrimSpace(in.Name), Type: typ, URL: address, Enabled: in.Enabled, Roundup: in.Roundup, Categories: in.Categories, CollectFrom: cf}
	if typ == domain.SourceTypeNewsletter {
		folder := strings.TrimSpace(in.Folder)
		if folder == "" {
			folder = "INBOX"
		}
		src.URL = strings.TrimSuffix(address, "/") + "/" + url.PathEscape(folder)
		username, password := in.Username, in.Password
		if existing != nil && existing.Newsletter != nil {
			if username == "" {
				username = existing.Newsletter.Username
			}
			if password == "" {
				password = existing.Newsletter.Password
			}
		}
		if username == "" || password == "" {
			return domain.Source{}, fmt.Errorf("newsletter username and password are required")
		}
		days := in.Days
		if days == 0 {
			days = config.DefaultNewsletterDays
		}
		if days < 0 {
			return domain.Source{}, fmt.Errorf("newsletter days cannot be negative")
		}
		if in.MaxMessages < 0 {
			return domain.Source{}, fmt.Errorf("maximum messages cannot be negative")
		}
		src.Newsletter = &domain.NewsletterOptions{Folder: folder, Username: username, Password: password, LookBackDays: days, MaxMessages: in.MaxMessages}
	}
	return src, nil
}
