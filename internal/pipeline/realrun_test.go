//go:build realrun

// One article, for real, against the configured provider.
//
//	make realrun                    # the longest article in the archive
//	ZIBA_ARTICLE_ID=762 make realrun
//
// The companion to the dry run: same article, same prompts, same schema, but
// the requests go out and the bill is real. It exists to answer the questions a
// dry run cannot — what the model actually says, and what it actually costs.
//
// Nothing is written back to the database. A single article judged by a model
// that has never run before is evidence, not a result, and it should not become
// one row that was analyzed differently from the other four hundred.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/store"
)

func TestRealRun(t *testing.T) {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load the configuration: %v", err)
	}
	if cfg.Provider != config.ProviderOpenAI {
		t.Skipf("ZIBA_AI_PROVIDER is %q; this harness is the OpenAI one", cfg.Provider)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()
	stored, err := db.Configuration(ctx)
	if err != nil {
		t.Fatalf("load the configuration: %v", err)
	}
	if !stored.Configured {
		t.Fatal("configuration is incomplete; finish web setup first")
	}
	interests := stored.Interests

	id := articleID(t, ctx, db)
	article, err := db.Article(ctx, id)
	if err != nil {
		t.Fatalf("read article %d: %v", id, err)
	}
	declared, err := db.DeclaredCategories(ctx)
	if err != nil {
		t.Fatalf("declared categories: %v", err)
	}

	meter := &meteringTransport{}
	provider := &OpenAI{
		client: openai.NewClient(
			option.WithAPIKey(cfg.OpenAIAPIKey),
			option.WithHTTPClient(&http.Client{Transport: meter, Timeout: 5 * time.Minute}),
		),
		fastModel:     cfg.FastModel,
		capableModel:  cfg.CapableModel,
		fastEffort:    cfg.FastEffort,
		capableEffort: cfg.CapableEffort,
		interests:     interests,
	}

	fmt.Printf("Article %d — %s\n", article.ID, article.Title)
	fmt.Printf("  %s\n", article.URL)
	fmt.Printf("  %d characters, %d sent\n\n",
		len([]rune(article.FullText)), min(len([]rune(article.FullText)), maxArticleRunes))
	fmt.Printf("  fast     %s (effort %s)\n", cfg.FastModel, orProviderDefault(cfg.FastEffort))
	fmt.Printf("  capable  %s (effort %s)\n\n", cfg.CapableModel, orProviderDefault(cfg.CapableEffort))

	// Emptied before the run so that anything printed below came from this run.
	// Analyze returns the article untouched when it scores below the threshold,
	// and the first version of this harness printed the summary already in the
	// database as though the model had just written it.
	article.Summary = ""

	started := time.Now()
	pipe := New(provider, interests.Threshold, realRunLogger())
	analyzed, err := pipe.Analyze(ctx, article, declared[article.SourceID])
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	elapsed := time.Since(started)

	fmt.Printf("── result ──────────────────────────────────────────────\n")
	fmt.Printf("categories   %s\n", strings.Join(analyzed.Categories, ", "))
	fmt.Printf("score        %d (threshold %d)\n", analyzed.Score, interests.Threshold)
	fmt.Printf("reason       %s\n", analyzed.ScoreReason)
	fmt.Printf("tone         %s\n", analyzed.Tone)
	fmt.Printf("entities     %s\n", strings.Join(analyzed.Entities, ", "))
	if analyzed.Summary == "" {
		fmt.Printf("\nsummary      none — it scored below the threshold, so it was never asked for\n\n")
	} else {
		fmt.Printf("\nsummary\n%s\n\n", wrap(analyzed.Summary, 72))
	}

	fmt.Printf("── tokens ──────────────────────────────────────────────\n")
	var inCost, outCost int64
	for _, call := range meter.calls {
		fmt.Printf("%-16s in %6d (cached %d)   out %5d (reasoning %d)   total %6d\n",
			call.Model, call.Usage.PromptTokens, call.Usage.PromptDetails.CachedTokens,
			call.Usage.CompletionTokens, call.Usage.CompletionDetails.ReasoningTokens,
			call.Usage.TotalTokens)
		inCost += call.Usage.PromptTokens
		outCost += call.Usage.CompletionTokens
	}
	fmt.Printf("%-16s in %6d                out %5d                total %6d\n",
		"TOTAL", inCost, outCost, inCost+outCost)
	fmt.Printf("\n%d calls in %s. Nothing was written to the database.\n", len(meter.calls), elapsed.Round(time.Millisecond))
}

func orProviderDefault(effort config.ReasoningEffort) string {
	if effort == "" {
		return "the provider's default"
	}
	return string(effort)
}

func realRunLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// articleID is the longest article unless one is named, because length is what
// makes a call expensive and the longest is the worst case for both the cost
// and the truncation.
func articleID(t *testing.T, ctx context.Context, db *store.Store) int64 {
	t.Helper()

	if id, err := strconv.ParseInt(os.Getenv("ZIBA_ARTICLE_ID"), 10, 64); err == nil {
		return id
	}
	var id int64
	if err := db.Pool().QueryRow(ctx,
		`SELECT id FROM articles ORDER BY length(full_text) DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("find the longest article: %v", err)
	}
	return id
}

// meteringTransport passes each request through to the network and keeps what
// the reply says it cost. The pipeline discards usage — it has no use for it —
// so reading it here is the only way to report a real number rather than the
// four-characters-to-a-token guess the dry run makes.
type meteringTransport struct {
	calls []metered
}

type metered struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
		PromptDetails    struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (m *meteringTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	var call metered
	if json.Unmarshal(body, &call) == nil && call.Usage.TotalTokens > 0 {
		m.calls = append(m.calls, call)
	}

	// Put the body back, so the SDK reads what it would have read.
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, nil
}

// wrap breaks a summary at a sensible width, since it is meant to be read here.
func wrap(text string, width int) string {
	var out strings.Builder
	column := 0
	for _, word := range strings.Fields(text) {
		if column > 0 && column+1+len(word) > width {
			out.WriteString("\n")
			column = 0
		} else if column > 0 {
			out.WriteString(" ")
			column++
		}
		out.WriteString(word)
		column += len(word)
	}
	return out.String()
}
