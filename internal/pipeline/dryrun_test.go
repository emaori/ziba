//go:build dryrun

// Showing what would be sent, without sending it.
//
//	make dryrun            # the newest article with enough text
//	ZIBA_ARTICLE_ID=775 make dryrun
//
// Not part of any suite. It builds the requests through the real provider, the
// real prompts and the real schema, then intercepts them at the transport and
// prints them instead of putting them on the wire. Nothing reaches OpenAI and
// nothing is billed, which is the point: the first real run costs money and
// there is no way to un-send a bad prompt.
//
// It answers with a canned reply so that the second call happens too — the
// summary is only asked for once an assessment exists, and its request is worth
// seeing before it is paid for.
package pipeline

import (
	"bytes"
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

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

func TestDryRun(t *testing.T) {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load the configuration: %v", err)
	}
	interests, err := config.LoadInterests(cfg.InterestsPath)
	if err != nil {
		t.Fatalf("load the interests: %v", err)
	}

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()

	article, declared := pickArticle(t, ctx, db)

	// The provider, built exactly as main builds it, but with a transport that
	// answers from here. Constructed field by field rather than through
	// NewOpenAI only because the client has to be replaced.
	capture := &capturingTransport{}
	provider := &OpenAI{
		client: openai.NewClient(
			option.WithAPIKey(cfg.OpenAIAPIKey),
			option.WithHTTPClient(&http.Client{Transport: capture}),
		),
		fastModel:    cfg.FastModel,
		capableModel: cfg.CapableModel,
		interests:    interests,
	}

	fmt.Printf("Article %d: %q\n", article.ID, article.Title)
	fmt.Printf("  source        %s\n", article.SourceName)
	fmt.Printf("  full text     %d characters", len([]rune(article.FullText)))
	if len([]rune(article.FullText)) > maxArticleRunes {
		fmt.Printf("  (truncated to %d before sending)", maxArticleRunes)
	}
	fmt.Println()
	if len(declared) > 0 {
		fmt.Printf("  declared      %s — the categories are given, not asked for\n",
			strings.Join(declared, ", "))
	} else {
		fmt.Printf("  declared      none — the model is asked to classify\n")
	}
	fmt.Printf("  threshold     %d (below it, no second call is made at all)\n\n", interests.Threshold)

	// The whole stage, so the decision to summarize is the real one.
	pipe := New(provider, interests.Threshold, discardLogger())
	if _, err := pipe.Analyze(ctx, article, declared); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	for i, req := range capture.requests {
		fmt.Printf("──────────────────────────────────────────────────────────────\n")
		fmt.Printf("CALL %d of %d — %s\n", i+1, len(capture.requests), req.label)
		fmt.Printf("──────────────────────────────────────────────────────────────\n")
		fmt.Printf("%s %s\n", req.method, req.url)
		// Four characters to a token is the usual rule of thumb for English
		// prose. It is an estimate and it is stated as one: the count that gets
		// billed is the provider's, not this one.
		fmt.Printf("body: %d bytes, roughly %d input tokens\n", req.size, req.size/4)
		for _, header := range req.headers {
			fmt.Printf("%s\n", header)
		}
		fmt.Printf("\n%s\n\n", req.body)
	}
	fmt.Printf("%d requests, %d bytes of request body in total. Nothing was sent.\n",
		len(capture.requests), capture.bytes)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// pickArticle returns the article to preview and its source's declared
// categories, so the preview shows whichever of the two prompts applies.
func pickArticle(t *testing.T, ctx context.Context, db *store.Store) (domain.Article, []string) {
	t.Helper()

	id, err := strconv.ParseInt(os.Getenv("ZIBA_ARTICLE_ID"), 10, 64)
	if err != nil {
		if id, err = newestArticle(ctx, db); err != nil {
			t.Fatalf("find an article: %v", err)
		}
	}
	article, err := db.Article(ctx, id)
	if err != nil {
		t.Fatalf("read article %d: %v", id, err)
	}
	if strings.TrimSpace(article.FullText) == "" {
		t.Fatalf("article %d has no text; the summary call would refuse it", id)
	}

	declared, err := db.DeclaredCategories(ctx)
	if err != nil {
		t.Fatalf("declared categories: %v", err)
	}
	return article, declared[article.SourceID]
}

func newestArticle(ctx context.Context, db *store.Store) (int64, error) {
	var id int64
	err := db.Pool().QueryRow(ctx,
		`SELECT id FROM articles WHERE length(full_text) > 2000 ORDER BY collected_at DESC LIMIT 1`).Scan(&id)
	return id, err
}

// capturingTransport records each request and answers it locally.
type capturingTransport struct {
	requests []captured
	bytes    int
}

type captured struct {
	label       string
	method, url string
	headers     []string
	body        string
	size        int
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		pretty.Write(body)
	}

	// Which call this is, told from the request itself rather than from a
	// counter: the schema is only sent on the assessment.
	label := "summary — the capable model"
	if bytes.Contains(body, []byte(`"response_format"`)) {
		label = "assessment — the fast model"
	}

	var headers []string
	for name, values := range req.Header {
		value := strings.Join(values, ", ")
		if strings.EqualFold(name, "Authorization") {
			value = mask(value)
		}
		headers = append(headers, name+": "+value)
	}
	sortStrings(headers)

	c.requests = append(c.requests, captured{
		label:   label,
		method:  req.Method,
		url:     req.URL.String(),
		headers: headers,
		body:    pretty.String(),
		size:    len(body),
	})
	c.bytes += len(body)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(cannedReply(label))),
		Request:    req,
	}, nil
}

// cannedReply is a plausible answer of the right shape, so the stage carries on
// to the second call. The values are invented and are not a prediction.
func cannedReply(label string) string {
	content := "A stand-in summary, so the run continues past this call."
	if strings.HasPrefix(label, "assessment") {
		content = `{"categories":["Software Architecture"],"entities":["Shopify","Redis","MySQL"],` +
			`"tone":"analysis","score":75,"reason":"A stand-in assessment."}`
	}
	reply := map[string]any{
		"id":     "chatcmpl-dryrun",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}
	encoded, _ := json.Marshal(reply)
	return string(encoded)
}

func mask(value string) string {
	if len(value) < 18 {
		return "Bearer …"
	}
	return value[:14] + "…" + value[len(value)-4:]
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
