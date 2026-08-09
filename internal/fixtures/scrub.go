package fixtures

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Scrubber rewrites captured material so that nothing identifying the reader
// reaches the repository.
//
// A newsletter is addressed to one person and knows it. The subscriber's
// address appears in the headers and again in the delivery records; every link
// is wrapped in a tracker carrying an identifier unique to that subscriber,
// sometimes to that send; the unsubscribe link is a bearer token that would let
// anyone holding it change the subscription. Committing that as a fixture
// publishes all of it.
//
// What the tests need is the *shape* — how many anchors, which are editorial,
// whether two links point at one article. None of that depends on the values,
// so they are replaced. Replacement is consistent within a capture: the same
// input always becomes the same output, and different inputs stay different,
// because "these two trackers are distinct" is itself under test.
//
// Everything here is matched tolerantly of quoted-printable, which is how mail
// arrives. A first attempt was not, and it let three things through: a subscriber
// uuid split across a soft line break, the sender's domain standing alone in a
// DKIM header, and the display name, which is not an address at all.
type Scrubber struct {
	seen map[string]string
}

func NewScrubber() *Scrubber { return &Scrubber{seen: make(map[string]string)} }

// identifying are the exact strings that must never survive, longest first so
// that an address is replaced before the domain inside it.
var identifying = []struct{ from, to string }{
	{"emanuele@origgi.com", "reader@fixture.test"},
	{"ziba.ns.emaori@gmail.com", "inbox@fixture.test"},
	{"origgi.com", "fixture.test"},
	{"ziba.ns.emaori", "inbox"},
	{"emanuele", "reader"}, // the display name, and the local part alone
	{"origgi", "fixture"},
	{"emaori", "inbox"},
}

// softBreak is quoted-printable's line continuation, which may sit between any
// two characters of anything.
const softBreak = `(?:=\r?\n)?`

// qpLiteral matches a literal even where soft line breaks have split it.
func qpLiteral(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 {
			b.WriteString(softBreak)
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	return b.String()
}

// qpRepeat matches n of a character class, soft breaks allowed between them.
func qpRepeat(class string, n int) string {
	return class + `(?:` + softBreak + class + `){` + strconv.Itoa(n-1) + `}`
}

const hex = `[0-9a-fA-F]`

var (
	identifyingPatterns = buildIdentifyingPatterns()

	// A uuid, which is how several publishers key a subscriber.
	uuidLike = regexp.MustCompile(
		qpRepeat(hex, 8) + softBreak + `-` + softBreak + qpRepeat(hex, 4) +
			softBreak + `-` + softBreak + qpRepeat(hex, 4) +
			softBreak + `-` + softBreak + qpRepeat(hex, 4) +
			softBreak + `-` + softBreak + qpRepeat(hex, 12))

	// A long opaque run of hex: a tracking identifier by any other name.
	tokenLike = regexp.MustCompile(qpRepeat(hex, 16) + `(?:` + softBreak + hex + `)*`)

	// Named parameters carrying a recipient even when the value looks harmless:
	// e=847feaba8a is ten characters and identifies a person. The separator may
	// itself be encoded, which is why "=3D" is an alternative to "=".
	trackingParam = regexp.MustCompile(
		`\b(e|u|uid|sid|c2id|lid|mid|cid|subscriber_id|recipient)(=3D|=)` +
			`([A-Za-z0-9._\-]` + `(?:` + softBreak + `[A-Za-z0-9._\-])*)`)

	// A subscriber identifier in a path segment rather than a query. Listing the
	// segment names it appears under was the first attempt and it was
	// whack-a-mole: /unsubscribe/u/… was caught and /fb/… was not. So the rule
	// is the shape of the value, not the name of the segment — sixteen or more
	// url-safe characters carrying both a digit and a capital, which is a token
	// and not the lowercase-and-hyphens of an article slug.
	opaqueSegment = regexp.MustCompile(
		`(/)([A-Za-z0-9_\-]` + `(?:` + softBreak + `[A-Za-z0-9_\-]){15,})`)

	// Whatever follows one of these is a bearer token by definition, however
	// short: blogtrottr's is six characters, which no shape rule can tell from
	// a word without wrecking everything else.
	bearerSegment = regexp.MustCompile(
		`(/(?:unsubscribe|subscriptions|subscribers|optout|opt-out)/)` +
			`([A-Za-z0-9_\-]` + `(?:` + softBreak + `[A-Za-z0-9_\-])*)`)

	anyAddress = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

func buildIdentifyingPatterns() []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(identifying))
	for _, pair := range identifying {
		patterns = append(patterns, regexp.MustCompile(`(?i)`+qpLiteral(pair.from)))
	}
	return patterns
}

// Text scrubs one captured document.
func (s *Scrubber) Text(in string) string {
	out := in
	for i, pattern := range identifyingPatterns {
		out = pattern.ReplaceAllLiteralString(out, identifying[i].to)
	}

	out = uuidLike.ReplaceAllStringFunc(out, func(match string) string {
		return s.standIn("uuid", unfold(match))
	})
	out = trackingParam.ReplaceAllStringFunc(out, func(match string) string {
		parts := trackingParam.FindStringSubmatch(match)
		return parts[1] + parts[2] + s.standIn("param", unfold(parts[3]))
	})
	out = opaqueSegment.ReplaceAllStringFunc(out, func(match string) string {
		parts := opaqueSegment.FindStringSubmatch(match)
		if !looksLikeToken(unfold(parts[2])) {
			return match
		}
		return parts[1] + s.standIn("param", unfold(parts[2]))
	})
	out = bearerSegment.ReplaceAllStringFunc(out, func(match string) string {
		parts := bearerSegment.FindStringSubmatch(match)
		return parts[1] + s.standIn("param", unfold(parts[2]))
	})
	out = tokenLike.ReplaceAllStringFunc(out, func(match string) string {
		return s.standIn("token", unfold(match))
	})

	// Last, because the rules above may have exposed one: any address still
	// standing belongs to somebody.
	out = anyAddress.ReplaceAllStringFunc(out, func(match string) string {
		if strings.HasSuffix(match, "@fixture.test") {
			return match
		}
		return s.standIn("person", match) + "@fixture.test"
	})
	return out
}

// looksLikeToken distinguishes an identifier from an article slug. A slug is
// words and hyphens; a token carries a digit and a capital because it was
// generated rather than written.
func looksLikeToken(s string) bool {
	var digit, upper bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'A' && r <= 'Z':
			upper = true
		}
	}
	return digit && upper
}

// unfold removes soft line breaks, so that one value split two ways is
// recognised as the same value and gets the same stand-in.
func unfold(s string) string {
	return strings.NewReplacer("=\r\n", "", "=\n", "").Replace(s)
}

// standIn returns a stable replacement for one value: the same every time that
// value is seen, and different for every other.
func (s *Scrubber) standIn(kind, value string) string {
	if existing, known := s.seen[value]; known {
		return existing
	}

	n := len(s.seen) + 1
	var replacement string
	switch kind {
	case "uuid":
		// The same shape, so anything parsing it still can.
		replacement = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	case "token":
		replacement = fmt.Sprintf("%s%04d", strings.Repeat("a", 12), n)
	case "person":
		replacement = fmt.Sprintf("person%02d", n)
	default:
		replacement = fmt.Sprintf("t%04d", n)
	}
	s.seen[value] = replacement
	return replacement
}

// Leaks reports anything identifying that survived, so a capture can refuse to
// write rather than quietly commit it.
func Leaks(text string) []string {
	var found []string
	for i, pattern := range identifyingPatterns {
		if pattern.MatchString(text) {
			found = append(found, identifying[i].from)
		}
	}
	for _, match := range anyAddress.FindAllString(text, -1) {
		if !strings.HasSuffix(match, "@fixture.test") && !strings.HasSuffix(match, "@example.com") {
			found = append(found, match)
		}
	}
	for _, match := range bearerSegment.FindAllStringSubmatch(text, -1) {
		if !strings.HasPrefix(unfold(match[2]), "t0") && unfold(match[2]) != "" {
			found = append(found, "bearer token: "+match[0])
		}
	}
	for _, match := range opaqueSegment.FindAllStringSubmatch(text, -1) {
		if looksLikeToken(unfold(match[2])) {
			found = append(found, "identifier in a path: "+match[0])
		}
	}
	return found
}
