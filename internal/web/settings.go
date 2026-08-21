package web

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/linkwarden"
	"github.com/emaori/ziba/internal/store"
)

func (s *Server) configStore() (configurationStore, bool) {
	return s.configuration, s.configuration != nil
}
func (s *Server) currentConfiguration(r *http.Request) (store.Configuration, error) {
	if cs, ok := s.configStore(); ok {
		return cs.Configuration(r.Context())
	}
	return store.Configuration{Configured: true, Interests: config.Interests{Threshold: int(s.threshold), Topics: interestsFromNames(s.interests)}}, nil
}
func interestsFromNames(names []string) []config.Interest {
	out := make([]config.Interest, len(names))
	for i, n := range names {
		out[i] = config.Interest{Topic: n, Priority: 1}
	}
	return out
}

func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		cfg, err := s.currentConfiguration(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		setup := strings.HasPrefix(r.URL.Path, "/setup")
		if !cfg.Configured && !setup {
			http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
			return
		}
		if cfg.Configured && setup {
			http.Redirect(w, r, "/settings/interests", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
}

func (s *Server) handleSetupInterests(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.currentConfiguration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if cfg.Interests.Threshold == 0 {
		cfg.Interests.Threshold = 60
	}
	s.render(w, r, "setup_interests.html", &settingsPage{layoutData: layoutData{Title: "Welcome", SetupMode: true}, Settings: cfg, InterestPresets: interestPresets})
}
func (s *Server) handleSetupSources(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(cfg.Interests.Topics) == 0 {
		http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if len(cfg.Sources) == 0 {
			s.render(w, r, "setup_sources.html", &settingsPage{layoutData: layoutData{Title: "Sources", SetupMode: true}, Settings: cfg, SourcePresets: sourcePresets, Error: "add at least one source"})
			return
		}
		http.Redirect(w, r, "/setup/schedule", http.StatusSeeOther)
		return
	}
	s.render(w, r, "setup_sources.html", &settingsPage{layoutData: layoutData{Title: "Sources", SetupMode: true}, Settings: cfg, SourcePresets: sourcePresets})
}

func (s *Server) handleSetupSchedule(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(cfg.Interests.Topics) == 0 {
		http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
		return
	}
	if len(cfg.Sources) == 0 {
		http.Redirect(w, r, "/setup/sources", http.StatusSeeOther)
		return
	}
	amount, unit := scheduleParts(cfg.Schedule.Every)
	data := &settingsPage{layoutData: layoutData{Title: "Schedule", SetupMode: true}, Settings: cfg, ScheduleAmount: amount, ScheduleUnit: unit, ScheduleAt: cfg.Schedule.At.String()}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		data.ScheduleAt, data.ScheduleUnit = r.Form.Get("collect_at"), r.Form.Get("collect_every_unit")
		data.ScheduleAmount, _ = strconv.Atoi(r.Form.Get("collect_every_amount"))
		schedule, saveErr := parseScheduleForm(r)
		if saveErr == nil {
			saveErr = cs.SaveSchedule(r.Context(), schedule)
		}
		if saveErr == nil {
			saveErr = cs.FinishSetup(r.Context(), cfg.Interests, cfg.Sources, r.Form.Get("collect_now") == "on")
		}
		if saveErr == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		data.Error = saveErr.Error()
	}
	s.render(w, r, "setup_schedule.html", data)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/settings" {
		http.Redirect(w, r, "/settings/interests", http.StatusSeeOther)
		return
	}
	cfg, err := s.currentConfiguration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	section := strings.TrimPrefix(r.URL.Path, "/settings/")
	tmpl := "settings_interests.html"
	if section == "sources" {
		tmpl = "settings_sources.html"
	} else if section == "schedule" {
		tmpl = "settings_schedule.html"
	} else if section == "linkwarden" {
		tmpl = "settings_linkwarden.html"
	} else if section == "scoring" {
		tmpl = "settings_scoring.html"
	}
	var feedback store.ScoreFeedbackSummary
	if section == "scoring" {
		feedback, err = s.feedback.ScoreFeedbackSummary(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	amount, unit := scheduleParts(cfg.Schedule.Every)
	form := cfg.Linkwarden
	form.Password, form.Token = "", ""
	s.render(w, r, tmpl, &settingsPage{layoutData: layoutData{Title: "Settings"}, Settings: cfg, SettingsSection: section, LinkwardenForm: form, InterestPresets: interestPresets, SourcePresets: sourcePresets, ScheduleAmount: amount, ScheduleUnit: unit, ScheduleAt: cfg.Schedule.At.String(), ScoreFeedbackSummary: feedback, Success: r.URL.Query().Get("success")})
}

func (s *Server) handleScoringReset(w http.ResponseWriter, r *http.Request) {
	summary, err := s.feedback.ScoreFeedbackSummary(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if err := s.feedback.ResetPersonalizedScoring(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		http.Redirect(w, r, "/settings/scoring?success=reset", http.StatusSeeOther)
		return
	}
	s.render(w, r, "reset_scoring.html", &settingsPage{layoutData: layoutData{Title: "Reset personalized scoring"}, SettingsSection: "scoring", ScoreFeedbackSummary: summary})
}

func (s *Server) handleSettingsLinkwarden(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	stored, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := linkwarden.Configuration{
		Enabled: r.Form.Get("enabled") == "on", URL: r.Form.Get("url"),
		Auth: linkwarden.AuthMethod(r.Form.Get("auth")), Username: r.Form.Get("username"),
		Password: r.Form.Get("password"), Token: r.Form.Get("token"),
	}
	if in.Password == "" {
		in.Password = stored.Linkwarden.Password
	}
	if in.Token == "" {
		in.Token = stored.Linkwarden.Token
	}
	if err = in.Validate(); err == nil && in.Enabled {
		s.linkwarden.Configure(in)
		err = s.linkwarden.Test(r.Context())
	}
	if err == nil {
		err = cs.SaveLinkwarden(r.Context(), in)
	}
	if err != nil {
		in.Password, in.Token = "", ""
		stored.Linkwarden = in
		s.render(w, r, "settings_linkwarden.html", &settingsPage{layoutData: layoutData{Title: "Settings"}, Settings: stored, SettingsSection: "linkwarden", LinkwardenForm: in, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/settings/linkwarden?saved=1", http.StatusSeeOther)
}

func (s *Server) handleSettingsSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	schedule, err := parseScheduleForm(r)
	if err == nil {
		err = cs.SaveSchedule(r.Context(), schedule)
	}
	if err != nil {
		amount, _ := strconv.Atoi(r.Form.Get("collect_every_amount"))
		s.render(w, r, "settings_schedule.html", &settingsPage{layoutData: layoutData{Title: "Settings"}, Settings: cfg, SettingsSection: "schedule", ScheduleAmount: amount, ScheduleUnit: r.Form.Get("collect_every_unit"), ScheduleAt: r.Form.Get("collect_at"), Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/settings/schedule", http.StatusSeeOther)
}

func (s *Server) handleInterestForm(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setup := strings.HasPrefix(r.URL.Path, "/setup/")
	id := -1
	if raw := r.PathValue("id"); raw != "" {
		id, err = strconv.Atoi(raw)
		if err != nil || id < 0 || id >= len(cfg.Interests.Topics) {
			http.NotFound(w, r)
			return
		}
	}
	interest := config.Interest{Priority: 1}
	if id >= 0 {
		interest = cfg.Interests.Topics[id]
	}
	data := &settingsPage{layoutData: layoutData{Title: "Interest", SetupMode: setup}, Settings: cfg, SettingsSection: "interests", InterestForm: interest, InterestIndex: id, EditingInterest: id >= 0, CancelURL: modeURL(setup, "/setup/interests", "/settings/interests")}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		interest, err = parseInterest(r)
		data.InterestForm = interest
		candidate := append([]config.Interest(nil), cfg.Interests.Topics...)
		if id >= 0 {
			candidate[id] = interest
		} else {
			candidate = append(candidate, interest)
		}
		updated := config.Interests{Threshold: cfg.Interests.Threshold, Topics: candidate}
		if updated.Threshold == 0 {
			updated.Threshold = 60
		}
		if err == nil {
			err = config.ValidateInterests(updated)
		}
		if err == nil {
			if setup {
				err = cs.SaveSetupInterests(r.Context(), updated)
			} else {
				err = cs.SaveConfiguration(r.Context(), updated, cfg.Sources)
			}
		}
		if err == nil {
			http.Redirect(w, r, data.CancelURL, http.StatusSeeOther)
			return
		}
		data.Error = err.Error()
	}
	s.render(w, r, "interest_form.html", data)
}

func (s *Server) handleInterestPreset(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var found *config.Interest
	for _, p := range interestPresets {
		if p.ID == r.PathValue("id") {
			v := p.Value
			found = &v
		}
	}
	if found == nil {
		http.NotFound(w, r)
		return
	}
	for _, v := range cfg.Interests.Topics {
		if strings.EqualFold(v.Topic, found.Topic) {
			http.Redirect(w, r, modeURL(strings.HasPrefix(r.URL.Path, "/setup/"), "/setup/interests", "/settings/interests"), http.StatusSeeOther)
			return
		}
	}
	cfg.Interests.Topics = append(cfg.Interests.Topics, *found)
	if cfg.Interests.Threshold == 0 {
		cfg.Interests.Threshold = 60
	}
	setup := strings.HasPrefix(r.URL.Path, "/setup/")
	if setup {
		err = cs.SaveSetupInterests(r.Context(), cfg.Interests)
	} else {
		err = cs.SaveConfiguration(r.Context(), cfg.Interests, cfg.Sources)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, modeURL(setup, "/setup/interests", "/settings/interests"), http.StatusSeeOther)
}

func (s *Server) handleSetupInterestRemove(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	id, parseErr := strconv.Atoi(r.PathValue("id"))
	if err != nil || parseErr != nil || id < 0 || id >= len(cfg.Interests.Topics) {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		s.render(w, r, "remove_setup.html", &settingsPage{layoutData: layoutData{Title: "Remove interest", SetupMode: true}, RemoveName: cfg.Interests.Topics[id].Topic, RemoveKind: "interest", RemoveAction: r.URL.Path, CancelURL: "/setup/interests"})
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if len(cfg.Interests.Topics) == 1 {
		s.render(w, r, "setup_interests.html", &settingsPage{layoutData: layoutData{Title: "Welcome", SetupMode: true}, Settings: cfg, InterestPresets: interestPresets, Error: "add another interest before removing the only one"})
		return
	}
	cfg.Interests.Topics = append(cfg.Interests.Topics[:id], cfg.Interests.Topics[id+1:]...)
	if err := cs.SaveSetupInterests(r.Context(), cfg.Interests); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
}

func (s *Server) handleSetupSourceRemove(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var name string
	for _, source := range cfg.Sources {
		if source.ID == id {
			name = source.Name
		}
	}
	if name == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		s.render(w, r, "remove_setup.html", &settingsPage{layoutData: layoutData{Title: "Remove source", SetupMode: true}, RemoveName: name, RemoveKind: "source", RemoveAction: r.URL.Path, CancelURL: "/setup/sources"})
		return
	}
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := cs.DeleteSetupSource(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/setup/sources", http.StatusSeeOther)
}

func (s *Server) handleThreshold(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err == nil {
		cfg.Interests.Threshold, err = strconv.Atoi(r.Form.Get("threshold"))
	}
	if err == nil {
		err = config.ValidateInterests(cfg.Interests)
	}
	if err == nil {
		err = cs.SaveConfiguration(r.Context(), cfg.Interests, cfg.Sources)
	}
	if err != nil {
		s.render(w, r, "settings_interests.html", &settingsPage{layoutData: layoutData{Title: "Settings"}, Settings: cfg, SettingsSection: "interests", InterestPresets: interestPresets, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/settings/interests", http.StatusSeeOther)
}

func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setup := strings.HasPrefix(r.URL.Path, "/setup/")
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var existing *domain.Source
	input := store.SourceInput{ID: id, Type: "rss", Enabled: true, Days: 1}
	for i := range cfg.Sources {
		if cfg.Sources[i].ID == id {
			existing = &cfg.Sources[i]
			input = sourceInput(cfg.Sources[i])
			break
		}
	}
	if id != 0 && existing == nil {
		http.NotFound(w, r)
		return
	}
	cancel := modeURL(setup, "/setup/sources", "/settings/sources")
	data := &settingsPage{layoutData: layoutData{Title: "Source", SetupMode: setup}, Settings: cfg, SettingsSection: "sources", Source: input, EditingSource: existing != nil, CancelURL: cancel}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		input = parseSourceInput(r, id)
		data.Source = input
		src, saveErr := store.ValidateSourceInput(input, cfg.Interests, existing)
		if saveErr == nil {
			if existing == nil {
				cfg.Sources = append(cfg.Sources, src)
			} else {
				for i := range cfg.Sources {
					if cfg.Sources[i].ID == id {
						cfg.Sources[i] = src
					}
				}
			}
			if setup {
				saveErr = cs.SaveSetupSources(r.Context(), cfg.Interests, cfg.Sources)
			} else {
				saveErr = cs.SaveConfiguration(r.Context(), cfg.Interests, cfg.Sources)
			}
		}
		if saveErr == nil {
			http.Redirect(w, r, cancel, http.StatusSeeOther)
			return
		}
		data.Error = saveErr.Error()
	}
	s.render(w, r, "source.html", data)
}

func (s *Server) handleSourcePreset(w http.ResponseWriter, r *http.Request) {
	if !s.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	cs, ok := s.configStore()
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := cs.Configuration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var input *store.SourceInput
	for _, p := range sourcePresets {
		if p.ID == r.PathValue("id") {
			v := p.Value
			input = &v
		}
	}
	if input == nil {
		http.NotFound(w, r)
		return
	}
	setup := strings.HasPrefix(r.URL.Path, "/setup/")
	redirect := modeURL(setup, "/setup/sources", "/settings/sources")
	for _, v := range cfg.Sources {
		if v.URL == input.URL {
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}
	}
	src, err := store.ValidateSourceInput(*input, cfg.Interests, nil)
	if err == nil {
		cfg.Sources = append(cfg.Sources, src)
		if setup {
			err = cs.SaveSetupSources(r.Context(), cfg.Interests, cfg.Sources)
		} else {
			err = cs.SaveConfiguration(r.Context(), cfg.Interests, cfg.Sources)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func modeURL(setup bool, a, b string) string {
	if setup {
		return a
	}
	return b
}

func scheduleParts(every time.Duration) (int, string) {
	if every%time.Hour == 0 {
		return int(every / time.Hour), "hours"
	}
	return int(every / time.Minute), "minutes"
}

func parseScheduleForm(r *http.Request) (config.CollectionSchedule, error) {
	amount, err := strconv.Atoi(r.Form.Get("collect_every_amount"))
	if err != nil || amount < 0 {
		return config.CollectionSchedule{}, fmt.Errorf("run every must be zero or a positive whole number")
	}
	unit := r.Form.Get("collect_every_unit")
	var every time.Duration
	switch unit {
	case "minutes":
		every = time.Duration(amount) * time.Minute
	case "hours":
		every = time.Duration(amount) * time.Hour
	default:
		return config.CollectionSchedule{}, fmt.Errorf("choose minutes or hours for run every")
	}
	return config.ParseCollectionSchedule(every.String(), r.Form.Get("collect_at"))
}
func parseInterest(r *http.Request) (config.Interest, error) {
	_ = r.ParseForm()
	p, err := strconv.Atoi(r.Form.Get("priority"))
	if err != nil {
		return config.Interest{}, fmt.Errorf("priority must be a number")
	}
	return config.Interest{Topic: strings.TrimSpace(r.Form.Get("topic")), Priority: p, Subtopics: splitComma(r.Form.Get("subtopics")), Note: strings.TrimSpace(r.Form.Get("note"))}, nil
}
func parseSourceInput(r *http.Request, id int64) store.SourceInput {
	_ = r.ParseForm()
	days, _ := strconv.Atoi(r.Form.Get("days"))
	maxMessages, _ := strconv.Atoi(r.Form.Get("max_messages"))
	return store.SourceInput{ID: id, Name: r.Form.Get("name"), Type: r.Form.Get("type"), URL: r.Form.Get("url"), Enabled: r.Form.Get("enabled") == "on", Roundup: r.Form.Get("roundup") == "on", BrowserFetch: r.Form.Get("browser_fetch") == "on", CollectFrom: r.Form.Get("collect_from"), Categories: splitComma(r.Form.Get("categories")), Folder: r.Form.Get("folder"), Username: r.Form.Get("username"), Password: r.Form.Get("password"), Days: days, MaxMessages: maxMessages}
}
func sourceInput(src domain.Source) store.SourceInput {
	in := store.SourceInput{ID: src.ID, Name: src.Name, Type: string(src.Type), URL: src.URL, Enabled: src.Enabled, Roundup: src.Roundup, BrowserFetch: src.BrowserFetch, Categories: src.Categories, CollectFrom: formatCollectFrom(src.CollectFrom)}
	if src.Newsletter != nil {
		in.Folder, in.Days, in.MaxMessages = src.Newsletter.Folder, src.Newsletter.LookBackDays, src.Newsletter.MaxMessages
		suffix := "/" + url.PathEscape(src.Newsletter.Folder)
		in.URL = strings.TrimSuffix(src.URL, suffix)
	}
	return in
}
func formatCollectFrom(v domain.CollectFrom) string {
	if v.All {
		return "all"
	}
	if !v.Date.IsZero() {
		return v.Date.Format("2006-01-02")
	}
	if v.Grace > 0 {
		return v.Grace.String()
	}
	return ""
}
func splitComma(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (s *Server) validCSRF(r *http.Request) bool {
	if err := parseRequestForm(r); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf_token")), []byte(s.csrfToken)) == 1
}
