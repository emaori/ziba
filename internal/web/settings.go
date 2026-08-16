package web

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/emaori/ziba/internal/config"
	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/store"
)

func (s *Server) configStore() (configurationStore, bool) {
	cs, ok := s.store.(configurationStore)
	return cs, ok
}

func (s *Server) currentConfiguration(r *http.Request) (store.Configuration, error) {
	if cs, ok := s.configStore(); ok {
		return cs.Configuration(r.Context())
	}
	return store.Configuration{Configured: true, Interests: config.Interests{
		Threshold: int(s.threshold), Topics: interestsFromNames(s.interests),
	}}, nil
}

func interestsFromNames(names []string) []config.Interest {
	out := make([]config.Interest, len(names))
	for i, name := range names {
		out[i] = config.Interest{Topic: name, Priority: 1}
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
		setupPath := strings.HasPrefix(r.URL.Path, "/setup")
		if !cfg.Configured && !setupPath {
			http.Redirect(w, r, "/setup/interests", http.StatusSeeOther)
			return
		}
		if cfg.Configured && setupPath {
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
	if cfg.Interests.Threshold == 0 {
		cfg.Interests.Threshold = 60
	}
	if len(cfg.Interests.Topics) == 0 {
		cfg.Interests.Topics = []config.Interest{{Priority: 1}}
	}
	data := &page{Title: "Welcome", SetupMode: true, Settings: cfg}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		interests, err := parseInterests(r)
		if err == nil {
			err = cs.SaveSetupInterests(r.Context(), interests)
		}
		if err == nil {
			http.Redirect(w, r, "/setup/sources", http.StatusSeeOther)
			return
		}
		data.Error = err.Error()
		data.Settings.Interests = interests
	}
	s.render(w, r, "setup_interests.html", data)
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
	inputs := []store.SourceInput{{Type: "rss", Enabled: true, Days: 1}}
	data := &page{Title: "Sources", SetupMode: true, Settings: cfg, WizardSources: inputs}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		inputs = parseWizardSources(r)
		data.WizardSources = inputs
		if len(inputs) == 0 {
			data.Error = "add at least one source"
		} else {
			sources := make([]domain.Source, 0, len(inputs))
			for i, input := range inputs {
				src, inputErr := store.ValidateSourceInput(input, cfg.Interests, nil)
				if inputErr != nil {
					data.Error = fmt.Sprintf("source %d: %v", i+1, inputErr)
					break
				}
				sources = append(sources, src)
			}
			if data.Error == "" {
				if err := cs.SaveConfiguration(r.Context(), cfg.Interests, sources); err != nil {
					data.Error = err.Error()
				} else {
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}
			}
		}
	}
	s.render(w, r, "setup_sources.html", data)
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
	template := "settings_interests.html"
	if section == "sources" {
		template = "settings_sources.html"
	}
	s.render(w, r, template, &page{Title: "Settings", Settings: cfg, SettingsSection: section})
}

func (s *Server) handleSettingsInterests(w http.ResponseWriter, r *http.Request) {
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
	interests, err := parseInterests(r)
	if err == nil {
		err = cs.SaveConfiguration(r.Context(), interests, cfg.Sources)
	}
	if err != nil {
		cfg.Interests = interests
		s.render(w, r, "settings_interests.html", &page{Title: "Settings", Settings: cfg, SettingsSection: "interests", Error: err.Error()})
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
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		input = parseSourceInput(r, id)
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
			saveErr = cs.SaveConfiguration(r.Context(), cfg.Interests, cfg.Sources)
		}
		if saveErr == nil {
			http.Redirect(w, r, "/settings/sources", http.StatusSeeOther)
			return
		}
		s.render(w, r, "source.html", &page{Title: "Source", Settings: cfg, SettingsSection: "sources", Source: input, EditingSource: existing != nil, Error: saveErr.Error()})
		return
	}
	s.render(w, r, "source.html", &page{Title: "Source", Settings: cfg, SettingsSection: "sources", Source: input, EditingSource: existing != nil})
}

func parseInterests(r *http.Request) (config.Interests, error) {
	if err := r.ParseForm(); err != nil {
		return config.Interests{}, fmt.Errorf("invalid form")
	}
	threshold, err := strconv.Atoi(r.Form.Get("threshold"))
	if err != nil {
		return config.Interests{}, fmt.Errorf("threshold must be a number")
	}
	names, priorities := r.Form["interest_topic"], r.Form["interest_priority"]
	subtopics, notes := r.Form["interest_subtopics"], r.Form["interest_note"]
	out := config.Interests{Threshold: threshold}
	seen := map[string]bool{}
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seen[name] {
			return out, fmt.Errorf("interest %q is repeated", name)
		}
		seen[name] = true
		priority := 1
		if i < len(priorities) {
			priority, err = strconv.Atoi(priorities[i])
			if err != nil {
				return out, fmt.Errorf("priority for %q must be a number", name)
			}
		}
		interest := config.Interest{Topic: name, Priority: priority}
		if i < len(subtopics) {
			interest.Subtopics = splitComma(subtopics[i])
		}
		if i < len(notes) {
			interest.Note = strings.TrimSpace(notes[i])
		}
		out.Topics = append(out.Topics, interest)
	}
	return out, config.ValidateInterests(out)
}

func parseSourceInput(r *http.Request, id int64) store.SourceInput {
	_ = r.ParseForm()
	days, _ := strconv.Atoi(r.Form.Get("days"))
	maxMessages, _ := strconv.Atoi(r.Form.Get("max_messages"))
	return store.SourceInput{ID: id, Name: r.Form.Get("name"), Type: r.Form.Get("type"), URL: r.Form.Get("url"),
		Enabled: r.Form.Get("enabled") == "on", Roundup: r.Form.Get("roundup") == "on",
		CollectFrom: r.Form.Get("collect_from"), Categories: splitComma(r.Form.Get("categories")),
		Folder: r.Form.Get("folder"), Username: r.Form.Get("username"), Password: r.Form.Get("password"), Days: days, MaxMessages: maxMessages}
}

func parseWizardSources(r *http.Request) []store.SourceInput {
	_ = r.ParseForm()
	var out []store.SourceInput
	for i := 0; ; i++ {
		prefix := fmt.Sprintf("source_%d_", i)
		if _, exists := r.Form[prefix+"name"]; !exists {
			break
		}
		name := strings.TrimSpace(r.Form.Get(prefix + "name"))
		if name == "" && strings.TrimSpace(r.Form.Get(prefix+"url")) == "" {
			continue
		}
		days, _ := strconv.Atoi(r.Form.Get(prefix + "days"))
		maxMessages, _ := strconv.Atoi(r.Form.Get(prefix + "max_messages"))
		out = append(out, store.SourceInput{
			Name: name, Type: r.Form.Get(prefix + "type"), URL: r.Form.Get(prefix + "url"),
			Enabled: r.Form.Get(prefix+"enabled") == "on", Roundup: r.Form.Get(prefix+"roundup") == "on",
			CollectFrom: r.Form.Get(prefix + "collect_from"), Categories: splitComma(r.Form.Get(prefix + "categories")),
			Folder: r.Form.Get(prefix + "folder"), Username: r.Form.Get(prefix + "username"), Password: r.Form.Get(prefix + "password"),
			Days: days, MaxMessages: maxMessages,
		})
	}
	return out
}

func sourceInput(src domain.Source) store.SourceInput {
	in := store.SourceInput{ID: src.ID, Name: src.Name, Type: string(src.Type), URL: src.URL, Enabled: src.Enabled, Roundup: src.Roundup, Categories: src.Categories, CollectFrom: formatCollectFrom(src.CollectFrom)}
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
	_ = r.ParseForm()
	return subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf_token")), []byte(s.csrfToken)) == 1
}
