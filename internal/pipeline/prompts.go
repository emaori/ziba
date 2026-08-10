package pipeline

import (
	"fmt"
	"strings"

	"github.com/emaori/ziba/internal/config"
)

// The instructions given to a model, shared by every provider.
//
// They live here rather than beside one implementation because the project runs
// two providers and will compare them on real articles. If each carried its own
// wording, the comparison would measure the prompts and not the models — and
// the wording would drift, so that improving one silently left the other behind.

// maxCategories is how many interests one article may be filed under.
//
// The number lives here because it now has to be said twice: once in the schema,
// where Anthropic enforces it, and once in the instructions, because OpenAI's
// strict mode refuses maxItems and strips it before the request is sent. The
// first real OpenAI call returned four categories for an article about CSS
// sanitisation — AI, Computer Science, Programming and Software Architecture —
// and nothing in the request had told it three was the limit.
//
// Three because the tabs are a reading order, not an index. An article filed
// under everything it touches on is findable in five places and useful in none.
const maxCategories = 3

// scoreBands anchor the number to something describable.
//
// Without them a model invents a scale, and a small one invents a different
// scale for every article. Naming the bands is the single change most likely to
// make the scores comparable across a day's collection — and comparable is all
// they need to be, since their only job is to order the page and clear a
// threshold.
const scoreBands = `  0      not relevant at all
  5-10   mentioned in passing
  15-30  a weak but real connection
  35-60  an important theme, but not what the piece is about
  65-100 what the piece is actually about`

// workedExample is one filled-in answer.
//
// A single example is worth more than another paragraph of instruction,
// especially to a smaller model: it settles the shape, the rounding and the
// tone of the reason in one go. Rounding to fives is deliberate — a model
// asked for 0 to 100 will produce 73 and 74 as though the difference meant
// something, and nothing downstream can use that precision.
const workedExample = `Round the score to a multiple of 5. Example answer:

{"categories":["Software Architecture"],
 "entities":["PostgreSQL","Amazon RDS"],
 "tone":"analysis",
 "score":70,
 "reason":"A detailed account of a database migration, which is squarely system design."}`

// assessSystemPrompt asks what an article is about and how much it is worth.
//
// declared changes the question rather than qualifying it. A source that states
// its own subject is not being classified, it is being judged: the reader
// decided the subject matters by subscribing, so what they need to know is
// whether this particular piece is worth their time.
func assessSystemPrompt(interests config.Interests, declared []string) string {
	if len(declared) > 0 {
		return fmt.Sprintf(`You rate articles for one specific reader.

This article comes from a source the reader follows deliberately, on these
subjects: %s. Do not question whether the subject is relevant — it is.

Rate from 0 to 100 how interesting and worth reading this particular piece is:

%s

Reward depth, originality, and something the reader would not already know.
Rate low a release note, a routine announcement, a rehash of common knowledge,
or a thinly disguised advertisement — even though the subject is right.

Answer only with the requested structure.`, strings.Join(declared, ", "), scoreBands)
	}

	return fmt.Sprintf(`You classify and rate articles for one specific reader.

First identify what the article is about, factually and without judging its
quality. Then rate how relevant it is to this reader.

The reader's interests, most important first:

%s

Choose at most %d of them, and only the ones the article is genuinely about. An
article that fits none is correctly given none. Listing every interest a piece
touches on is worse than listing one: it puts the article in front of the reader
in several places without telling them what it is.

Rate from 0 to 100, using these bands:

%s

Reward depth and usefulness to these interests; do not reward an article merely
for mentioning a keyword. An article outside every interest scores low even when
it is excellent in general.

%s

Answer only with the requested structure.`,
		interests.Describe(), maxCategories, scoreBands, workedExample)
}

// summarySystemPrompt asks for the summary shown under a headline.
func summarySystemPrompt(interests config.Interests) string {
	return fmt.Sprintf(`You write short summaries for one specific reader.

The reader's interests, most important first:

%s

Write 3 to 4 sentences saying what the article reports and why it matters to
this reader. Be concrete: name the finding, the number, the decision. Do not
open with "This article" and do not recommend reading it — the reader decides
that. Answer with the summary alone.`, interests.Describe())
}
