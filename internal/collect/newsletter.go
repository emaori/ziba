package collect

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"

	"github.com/emaori/ziba/internal/domain"
)

// defaultMaxMessages caps how many emails one run reads when the source does
// not say.
const defaultMaxMessages = 50

// maxMessageBytes bounds how much of one email is read. Newsletters are text
// and links; anything larger is attachments, which are not what we are here for.
const maxMessageBytes = 4 << 20 // 4 MiB

// Newsletter collects from a mailbox.
//
// Newsletters are not articles: they are lists of links with short blurbs. So
// this collector returns the links, which then go through the same pipeline as
// everything else, plus the email itself marked as provenance — kept so that
// "where did this come from" has an answer, never shown as an article.
type Newsletter struct {
	log *slog.Logger
}

// NewNewsletter builds the mailbox collector.
func NewNewsletter(log *slog.Logger) *Newsletter {
	return &Newsletter{log: log}
}

// Type implements domain.Collector.
func (c *Newsletter) Type() domain.SourceType { return domain.SourceTypeNewsletter }

// Collect reads the mailbox and returns the editorial links it found, together
// with one provenance item per email.
func (c *Newsletter) Collect(ctx context.Context, src domain.Source) ([]domain.RawItem, error) {
	opts := src.Newsletter
	if opts == nil {
		return nil, fmt.Errorf("source %q has no newsletter settings", src.Name)
	}

	client, err := c.connect(src, opts)
	if err != nil {
		return nil, err
	}
	// Logout is the polite close; the deferred Close covers the case where it
	// fails and the connection has to go anyway.
	defer client.Close()

	if _, err := client.Select(opts.Folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("select folder %q: %w", opts.Folder, err)
	}

	criteria := &imap.SearchCriteria{}
	if opts.UnreadOnly {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}

	found, err := client.Search(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search mailbox: %w", err)
	}

	seqNums := found.AllSeqNums()
	if len(seqNums) == 0 {
		return nil, nil
	}

	limit := opts.MaxMessages
	if limit <= 0 {
		limit = defaultMaxMessages
	}
	// Newest first: a mailbox with a backlog should yield today's reading, not
	// the oldest thing in it.
	if len(seqNums) > limit {
		seqNums = seqNums[len(seqNums)-limit:]
	}

	var set imap.SeqSet
	set.AddNum(seqNums...)

	messages, err := client.Fetch(set, &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch messages: %w", err)
	}

	var items []domain.RawItem
	for _, message := range messages {
		if ctx.Err() != nil {
			break
		}
		found, err := c.itemsFromMessage(src, message)
		if err != nil {
			// One unreadable email must not cost the rest of the mailbox.
			c.log.Warn("skipping message", "source", src.Name, "error", err)
			continue
		}
		items = append(items, found...)
	}
	return items, nil
}

func (c *Newsletter) connect(src domain.Source, opts *domain.NewsletterOptions) (*imapclient.Client, error) {
	address, err := url.Parse(src.URL)
	if err != nil {
		return nil, fmt.Errorf("parse mailbox address: %w", err)
	}

	host := address.Host
	if address.Port() == "" {
		if address.Scheme == "imap" {
			host += ":143"
		} else {
			host += ":993"
		}
	}

	// imap:// is plaintext and exists for a local test server. A real mailbox is
	// imaps://, and the difference is not one to make implicitly.
	var client *imapclient.Client
	if address.Scheme == "imap" {
		client, err = imapclient.DialInsecure(host, nil)
	} else {
		client, err = imapclient.DialTLS(host, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", host, err)
	}

	username := os.Getenv(opts.UsernameEnv)
	password := os.Getenv(opts.PasswordEnv)
	if err := client.Login(username, password).Wait(); err != nil {
		client.Close()
		// Naming the variable, not the value: this error ends up in logs.
		return nil, fmt.Errorf("log in as %s (from %s): %w", username, opts.UsernameEnv, err)
	}
	return client, nil
}

// itemsFromMessage turns one email into a provenance item plus its links.
func (c *Newsletter) itemsFromMessage(src domain.Source, msg *imapclient.FetchMessageBuffer) ([]domain.RawItem, error) {
	var body []byte
	for _, section := range msg.BodySection {
		body = section.Bytes
		break
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("message has no body")
	}

	subject, sender, sent := envelopeOf(msg)
	text, links, err := readMessage(body)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// The email itself, kept for the record. Its address is synthetic because a
	// message has none: it exists to make the item unique, not to be followed.
	provenance := domain.RawItem{
		SourceID:    src.ID,
		Kind:        domain.ItemKindProvenance,
		Title:       subject,
		URL:         messageAddress(src, msg, subject, sent),
		Author:      sender,
		PublishedAt: sent,
		CollectedAt: now,
		Text:        text,
	}

	items := make([]domain.RawItem, 0, len(links)+1)
	items = append(items, provenance)

	for _, link := range links {
		items = append(items, domain.RawItem{
			SourceID:    src.ID,
			Kind:        domain.ItemKindArticle,
			Title:       link.text,
			URL:         link.url,
			PublishedAt: sent,
			CollectedAt: now,
		})
	}
	return items, nil
}

func envelopeOf(msg *imapclient.FetchMessageBuffer) (subject, sender string, sent time.Time) {
	if msg.Envelope == nil {
		return "", "", time.Time{}
	}
	subject = strings.TrimSpace(msg.Envelope.Subject)
	sent = msg.Envelope.Date.UTC()

	for _, from := range msg.Envelope.From {
		if from.Name != "" {
			sender = from.Name
		} else {
			sender = from.Addr()
		}
		break
	}
	return subject, sender, sent
}

// messageAddress builds the synthetic address that identifies one email.
// Message identifiers are the natural key, and the date and subject stand in
// for the senders that omit one.
func messageAddress(src domain.Source, msg *imapclient.FetchMessageBuffer, subject string, sent time.Time) string {
	id := ""
	if msg.Envelope != nil {
		id = strings.Trim(msg.Envelope.MessageID, "<>")
	}
	if id == "" {
		id = fmt.Sprintf("%d-%s", sent.Unix(), subject)
	}
	return "imap://" + url.PathEscape(src.Name) + "/" + url.PathEscape(id)
}

// readMessage walks a message's parts, returning its readable text and the
// editorial links it carries.
func readMessage(raw []byte) (string, []extractedLink, error) {
	reader, err := mail.CreateReader(io.LimitReader(strings.NewReader(string(raw)), maxMessageBytes))
	if err != nil {
		return "", nil, fmt.Errorf("read message: %w", err)
	}
	defer reader.Close()

	var html, plain strings.Builder
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("read message part: %w", err)
		}

		inline, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue // an attachment, not the newsletter
		}

		content, err := io.ReadAll(io.LimitReader(part.Body, maxMessageBytes))
		if err != nil {
			continue
		}

		contentType, _, _ := inline.ContentType()
		if strings.Contains(contentType, "html") {
			html.Write(content)
		} else {
			plain.Write(content)
		}
	}

	// The HTML part is the one that carries the links; the plain part is a
	// fallback for the senders that do not send one.
	if html.Len() > 0 {
		return plainText(html.String()), editorialLinks(html.String()), nil
	}
	return strings.TrimSpace(plain.String()), nil, nil
}
