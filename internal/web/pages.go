package web

import (
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/linkwarden"
	"github.com/emaori/ziba/internal/store"
)

// layoutData contains only the state shared by the site chrome. Embedding it
// keeps the existing template field names while allowing each handler to use a
// page model that describes only its own content.
type layoutData struct {
	Title               string
	Interests           []string
	Active              string
	Threshold           domain.RelevanceScore
	SetupMode           bool
	CollectionRunning   bool
	CollectionCompleted uint64
	NextCollection      time.Time
	ScheduleDisabled    bool
	LinkwardenEnabled   bool
	ReturnTo            string
}

type templateData interface {
	layout() *layoutData
}

type readingPage struct {
	layoutData
	Digest   domain.Digest
	Article  domain.Article
	Articles []domain.Article
	Nav      store.DayNavigation
	Day      time.Time
	Interest string
	Offset   int
	PageSize int
}

func (p *readingPage) layout() *layoutData { return &p.layoutData }

type statisticsPage struct {
	layoutData
	BySource         []store.Tally
	ByDay            []store.DayTally
	Library          store.ArticleStats
	Unknown          int
	Tokens           store.TokenTally
	TokensByInterest []store.TokenTally
	TokensByDay      []store.TokenTally
	Backlogs         []store.Backlog
}

func (p *statisticsPage) layout() *layoutData { return &p.layoutData }

type settingsPage struct {
	layoutData
	Settings             store.Configuration
	Source               store.SourceInput
	InterestForm         config.Interest
	InterestIndex        int
	EditingInterest      bool
	EditingSource        bool
	Error                string
	SettingsSection      string
	FormAction           string
	CancelURL            string
	InterestPresets      []interestPreset
	SourcePresets        []sourcePreset
	RemoveName           string
	RemoveKind           string
	RemoveAction         string
	ScheduleEvery        string
	ScheduleAt           string
	ScheduleAmount       int
	ScheduleUnit         string
	LinkwardenForm       linkwarden.Configuration
	Success              string
	ScoreFeedbackSummary store.ScoreFeedbackSummary
}

func (p *settingsPage) layout() *layoutData { return &p.layoutData }

type linkwardenPageData struct {
	layoutData
	Article                domain.Article
	Error                  string
	Success                string
	LinkwardenForm         linkwarden.Configuration
	LinkwardenCollections  []linkwarden.Collection
	LinkwardenTags         []linkwarden.Tag
	LinkwardenName         string
	LinkwardenDescription  string
	LinkwardenCollectionID int64
	LinkwardenSelectedTags map[int64]bool
	LinkwardenNewTags      string
	LinkwardenTagNames     []string
}

func (p *linkwardenPageData) layout() *layoutData { return &p.layoutData }
