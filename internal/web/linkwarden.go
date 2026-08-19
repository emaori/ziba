package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/emaori/ziba/internal/domain"
	"github.com/emaori/ziba/internal/linkwarden"
)

func (s *Server) handleLinkwardenArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	article, err := s.reading.Article(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cfg, err := s.currentConfiguration(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !cfg.Linkwarden.Enabled {
		http.Redirect(w, r, "/settings/linkwarden", http.StatusSeeOther)
		return
	}
	fallback := "/article/" + strconv.FormatInt(id, 10)
	form := formFromArticle(article)
	form.returnTo = localReturnTo(r.URL.Query().Get("return_to"), fallback)
	s.linkwarden.Configure(cfg.Linkwarden)
	collections, err := s.linkwarden.Collections(r.Context())
	if err != nil {
		s.renderLinkwardenForm(w, r, article, nil, nil, form, err.Error())
		return
	}
	tags, err := s.linkwarden.Tags(r.Context())
	if err != nil {
		s.renderLinkwardenForm(w, r, article, collections, nil, form, err.Error())
		return
	}
	if len(collections) > 0 {
		form.collectionID = collections[0].ID
	}
	if r.Method == http.MethodPost {
		if !s.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		form = parseLinkwardenForm(r)
		form.returnTo = localReturnTo(form.returnTo, fallback)
		if err = validateLinkwardenForm(form, collections, tags); err == nil {
			err = s.linkwarden.CreateLink(r.Context(), linkwarden.Link{
				URL: article.URL, Name: form.name, Description: form.description,
				CollectionID: form.collectionID, Tags: selectedLinkwardenTags(form, tags),
			})
		}
		if err == nil {
			http.Redirect(w, r, form.returnTo, http.StatusSeeOther)
			return
		}
		s.renderLinkwardenForm(w, r, article, collections, tags, form, err.Error())
		return
	}
	s.render(w, r, "linkwarden_article.html", s.linkwardenPage(article, collections, tags, form))
}

type linkwardenForm struct {
	name, description, newTags string
	returnTo                   string
	tagNames                   []string
	collectionID               int64
	selected                   map[int64]bool
}

func formFromArticle(article domain.Article) linkwardenForm {
	description := article.Summary
	if article.LimitedOverview() {
		description = "Limited overview: " + description
	}
	return linkwardenForm{name: article.Title, description: description, selected: map[int64]bool{}}
}

func parseLinkwardenForm(r *http.Request) linkwardenForm {
	collectionID, _ := strconv.ParseInt(r.Form.Get("collection"), 10, 64)
	selected := make(map[int64]bool)
	for _, raw := range r.Form["tags"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			selected[id] = true
		}
	}
	return linkwardenForm{
		name: strings.TrimSpace(r.Form.Get("name")), description: strings.TrimSpace(r.Form.Get("description")),
		collectionID: collectionID, selected: selected, newTags: strings.TrimSpace(r.Form.Get("new_tags")),
		returnTo: strings.TrimSpace(r.Form.Get("return_to")), tagNames: splitTagValues(r.Form["tag_names"]),
	}
}

func validateLinkwardenForm(form linkwardenForm, collections []linkwarden.Collection, tags []linkwarden.Tag) error {
	if form.name == "" {
		return errors.New("name is required")
	}
	if len(form.name) > 2048 || len(form.description) > 2048 {
		return errors.New("name and description must be at most 2048 characters")
	}
	validCollection := false
	for _, collection := range collections {
		validCollection = validCollection || collection.ID == form.collectionID
	}
	if !validCollection {
		return errors.New("choose a Linkwarden collection")
	}
	validTags := make(map[int64]bool, len(tags))
	for _, tag := range tags {
		validTags[tag.ID] = true
	}
	for id := range form.selected {
		if !validTags[id] {
			return errors.New("one of the selected Linkwarden tags no longer exists")
		}
	}
	for _, name := range append(splitTagNames(form.newTags), form.tagNames...) {
		if len(name) > 50 {
			return errors.New("Linkwarden tag names must be at most 50 characters")
		}
	}
	return nil
}

func selectedLinkwardenTags(form linkwardenForm, existing []linkwarden.Tag) []linkwarden.Tag {
	out := make([]linkwarden.Tag, 0, len(form.selected)+len(splitTagNames(form.newTags))+len(form.tagNames))
	seen := make(map[string]bool)
	byName := make(map[string]linkwarden.Tag, len(existing))
	for _, tag := range existing {
		byName[strings.ToLower(tag.Name)] = tag
		if form.selected[tag.ID] {
			out = append(out, tag)
			seen[strings.ToLower(tag.Name)] = true
		}
	}
	for _, name := range append(splitTagNames(form.newTags), form.tagNames...) {
		key := strings.ToLower(name)
		if !seen[key] {
			if tag, ok := byName[key]; ok {
				out = append(out, tag)
			} else {
				out = append(out, linkwarden.Tag{Name: name})
			}
			seen[key] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func splitTagValues(values []string) []string {
	var out []string
	for _, value := range values {
		out = append(out, splitTagNames(value)...)
	}
	return out
}

func localReturnTo(raw, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return fallback
	}
	return u.RequestURI()
}

func splitTagNames(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (s *Server) renderLinkwardenForm(w http.ResponseWriter, r *http.Request, article domain.Article, collections []linkwarden.Collection, tags []linkwarden.Tag, form linkwardenForm, message string) {
	data := s.linkwardenPage(article, collections, tags, form)
	data.Error = message
	s.render(w, r, "linkwarden_article.html", data)
}

func (s *Server) linkwardenPage(article domain.Article, collections []linkwarden.Collection, tags []linkwarden.Tag, form linkwardenForm) *linkwardenPageData {
	return &linkwardenPageData{
		layoutData: layoutData{Title: "Save to Linkwarden", LinkwardenEnabled: true, ReturnTo: form.returnTo}, Article: article,
		LinkwardenCollections: collections, LinkwardenTags: tags,
		LinkwardenName: form.name, LinkwardenDescription: form.description,
		LinkwardenCollectionID: form.collectionID, LinkwardenSelectedTags: form.selected,
		LinkwardenNewTags: form.newTags, LinkwardenTagNames: form.tagNames,
	}
}
