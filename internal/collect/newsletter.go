package collect

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net/url"
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

// MessageHistory reports whether a message's extraction was committed.
type MessageHistory interface {
	NewsletterMessageExtracted(ctx context.Context, sourceID int64, key string) (bool, error)
}

// Newsletter collects email messages and their article candidates. Digests
// yield editorial links; essays yield the email itself as reading material.
type Newsletter struct {
	history MessageHistory
	log     *slog.Logger
}

// NewNewsletter builds the mailbox collector.
func NewNewsletter(log *slog.Logger, history MessageHistory) *Newsletter {
	return &Newsletter{log: log, history: history}
}

// Type implements domain.Collector.
func (c *Newsletter) Type() domain.SourceType { return domain.SourceTypeNewsletter }

// Collect reads the mailbox and returns, for each email, either its editorial
// links or the essay itself, together with the retained email.
func (c *Newsletter) Collect(ctx context.Context, src domain.Source) (domain.Collection, error) {
	opts := src.Newsletter
	if opts == nil {
		return domain.Collection{}, fmt.Errorf("source %q has no newsletter settings", src.Name)
	}

	client, err := c.connect(src, opts)
	if err != nil {
		return domain.Collection{}, err
	}
	// Logout is the polite close; the deferred Close covers the case where it
	// fails and the connection has to go anyway.
	defer client.Close()

	if _, err := client.Select(opts.Folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return domain.Collection{}, fmt.Errorf("select folder %q: %w", opts.Folder, err)
	}

	// A recent window, the way a feed is read — not the unread flag. That flag
	// belongs to the reader's mail client: relying on it meant Ziba missed any
	// newsletter its owner opened first, and re-read the rest forever.
	//
	// Since matches on when the server received the message rather than the date
	// the sender claims, so a delayed delivery or a wrong clock cannot hide mail.
	// The protocol compares whole days, so the window is inclusive of its edge.
	days := opts.LookBackDays
	if days <= 0 {
		days = 1
	}
	criteria := &imap.SearchCriteria{Since: time.Now().AddDate(0, 0, -days)}

	found, err := client.Search(criteria, nil).Wait()
	if err != nil {
		return domain.Collection{}, fmt.Errorf("search mailbox: %w", err)
	}

	seqNums := found.AllSeqNums()
	if len(seqNums) == 0 {
		return domain.Collection{}, nil
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
		return domain.Collection{}, fmt.Errorf("fetch messages: %w", err)
	}

	var collected domain.Collection
	for _, message := range messages {
		if ctx.Err() != nil {
			break
		}
		found, err := c.collectMessage(ctx, src, message)
		if err != nil {
			// One unreadable email must not cost the rest of the mailbox.
			c.log.Warn("skipping message", "source", src.Name, "error", err)
			continue
		}
		if found != nil {
			collected.Messages = append(collected.Messages, *found)
		}
	}
	return collected, ctx.Err()
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

	if err := client.Login(opts.Username, opts.Password).Wait(); err != nil {
		client.Close()
		return nil, fmt.Errorf("log in as %s: %w", opts.Username, err)
	}
	return client, nil
}

// collectMessage skips extraction only after the complete result was committed.
func (c *Newsletter) collectMessage(ctx context.Context, src domain.Source, msg *imapclient.FetchMessageBuffer) (*domain.NewsletterMessage, error) {
	key := messageKey(msg)
	if c.history != nil {
		extracted, err := c.history.NewsletterMessageExtracted(ctx, src.ID, key)
		if err != nil {
			return nil, fmt.Errorf("check newsletter message: %w", err)
		}
		if extracted {
			return nil, nil
		}
	}
	message, err := c.itemsFromMessage(src, msg)
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// itemsFromMessage retains the email and extracts article candidates from it.
func (c *Newsletter) itemsFromMessage(src domain.Source, msg *imapclient.FetchMessageBuffer) (domain.NewsletterMessage, error) {
	body := messageBody(msg)
	if len(body) == 0 {
		return domain.NewsletterMessage{}, fmt.Errorf("message has no body")
	}
	subject, sender, sent := envelopeOf(msg)
	text, links, err := readMessage(body)
	if err != nil {
		return domain.NewsletterMessage{}, err
	}
	now := time.Now().UTC()
	message := domain.NewsletterMessage{
		SourceID: src.ID, Key: messageKey(msg), Title: subject, Author: sender,
		PublishedAt: sent, CollectedAt: now, Text: text,
	}
	if isEssay(text, links) {
		message.Items = []domain.RawItem{{
			SourceID: src.ID, Kind: domain.ItemKindArticle, Title: subject,
			URL:    fmt.Sprintf("imap://source/%d/%s", src.ID, url.PathEscape(message.Key)),
			Author: sender, PublishedAt: sent, CollectedAt: now, Text: text,
		}}
	} else {
		for _, link := range links {
			message.Items = append(message.Items, domain.RawItem{
				SourceID: src.ID, Kind: domain.ItemKindArticle, Title: link.text,
				URL: link.url, PublishedAt: sent, CollectedAt: now,
			})
		}
	}
	return message, nil
}

func messageBody(msg *imapclient.FetchMessageBuffer) []byte {
	for _, section := range msg.BodySection {
		return section.Bytes
	}
	return nil
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

// messageKey prefers Message-ID; a raw-message hash identifies mail that omits it.
func messageKey(msg *imapclient.FetchMessageBuffer) string {
	if msg.Envelope != nil {
		if id := strings.Trim(strings.TrimSpace(msg.Envelope.MessageID), "<>"); id != "" {
			return "id:" + id
		}
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(messageBody(msg)))
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
