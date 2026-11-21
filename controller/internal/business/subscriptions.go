package business

import (
	"context"
	"encoding/json"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
	"time"
)

type Choice struct {
	Name    string    `json:"name"`
	EntryID domain.ID `json:"selected_entry_id"`
}
type InitialChoice struct {
	Name    string    `json:"name"`
	RouteID domain.ID `json:"route_id"`
}
type DNS struct {
	Bootstrap    []string `json:"bootstrap"`
	ProxyServers []string `json:"proxy_servers"`
	Business     []string `json:"business"`
}
type Preferences struct {
	Mode           string   `json:"mode"`
	IPv6           bool     `json:"ipv6"`
	BusinessUDP    bool     `json:"business_udp"`
	AllRoutesGroup bool     `json:"all_routes_group"`
	GroupByExit    bool     `json:"group_by_exit"`
	Selections     []Choice `json:"selections"`
	DNS            DNS      `json:"dns"`
}

func (p Preferences) private() stage3.ClientPreferences {
	v := stage3.ClientPreferences{Mode: p.Mode, IPv6: p.IPv6, BusinessUDP: p.BusinessUDP, AllRoutesGroup: p.AllRoutesGroup, GroupByExit: p.GroupByExit, DNS: stage3.ClientDNS{Bootstrap: p.DNS.Bootstrap, ProxyServers: p.DNS.ProxyServers, Business: p.DNS.Business}}
	for _, c := range p.Selections {
		v.Selections = append(v.Selections, stage3.GroupChoice{Name: c.Name, SelectedEntryID: c.EntryID})
	}
	return v
}
func preferences(p stage3.ClientPreferences) Preferences {
	v := Preferences{Mode: p.Mode, IPv6: p.IPv6, BusinessUDP: p.BusinessUDP, AllRoutesGroup: p.AllRoutesGroup, GroupByExit: p.GroupByExit, Selections: []Choice{}, DNS: DNS{p.DNS.Bootstrap, p.DNS.ProxyServers, p.DNS.Business}}
	for _, c := range p.Selections {
		v.Selections = append(v.Selections, Choice{c.Name, c.SelectedEntryID})
	}
	return v
}

type subscriptionWrite struct {
	domain.Mutation
	Name              string          `json:"name"`
	ExpiresAt         time.Time       `json:"expires_at"`
	Profile           control.Pin     `json:"profile"`
	Routes            []control.Pin   `json:"routes"`
	Preferences       Preferences     `json:"preferences"`
	InitialSelections []InitialChoice `json:"initial_selections,omitempty"`
}
type Subscription struct {
	subscriptions.ManagementView
	Preferences       Preferences      `json:"preferences"`
	DesiredRevision   domain.Revision  `json:"desired_revision"`
	ConfirmedRevision *domain.Revision `json:"confirmed_revision"`
	Operations        []Operation      `json:"operations"`
}
type Operation struct {
	EntryID       domain.ID       `json:"entry_id"`
	Generation    domain.Revision `json:"generation"`
	State         string          `json:"state"`
	Cleanup       string          `json:"cleanup"`
	TaskID        *string         `json:"task_id"`
	CleanupTaskID *string         `json:"cleanup_task_id"`
}

func (s *Service) subscription(ctx context.Context, key domain.ID) (Subscription, error) {
	v, e := s.Subscriptions.Get(ctx, key)
	if e != nil {
		return Subscription{}, e
	}
	out := Subscription{ManagementView: subscriptions.ProjectManagement(v), Operations: []Operation{}}
	e = s.DB.Read(ctx, func(q *dbgen.Queries) error {
		h, e := q.B08Head(ctx, string(key))
		if e != nil {
			return e
		}
		out.DesiredRevision = domain.Revision(h.DesiredRevision)
		if h.ConfirmedRevision.Valid {
			v := domain.Revision(h.ConfirmedRevision.Int64)
			out.ConfirmedRevision = &v
		}
		rev, e := q.B08Revision(ctx, dbgen.B08RevisionParams{SubscriptionID: string(key), Revision: h.DesiredRevision})
		if e != nil {
			return e
		}
		var p stage3.ClientPreferences
		if e = json.Unmarshal([]byte(rev.PreferencesJson), &p); e != nil {
			return e
		}
		out.Preferences = preferences(p)
		ops, e := q.I03CSubscriptionWork(ctx, string(key))
		for _, o := range ops {
			v := Operation{EntryID: domain.ID(o.EntryKey), Generation: domain.Revision(o.Generation), State: o.State, Cleanup: o.CleanupState}
			if o.TaskID.Valid {
				t := o.TaskID.String
				v.TaskID = &t
			}
			if o.CleanupTaskID.Valid {
				t := o.CleanupTaskID.String
				v.CleanupTaskID = &t
			}
			out.Operations = append(out.Operations, v)
		}
		return e
	})
	return out, e
}
func (s *Service) listSubscriptions(_ http.ResponseWriter, r *http.Request) (any, error) {
	after, n, e := pageQuery(r, "subscriptions")
	if e != nil {
		return nil, e
	}
	var ids []string
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		var e error
		ids, e = q.I03CListSubscriptions(r.Context(), dbgen.I03CListSubscriptionsParams{AfterID: after, PageSize: int64(n + 1)})
		return e
	})
	if e != nil {
		return nil, e
	}
	out := []subscriptions.ManagementView{}
	for _, key := range ids {
		v, e := s.Subscriptions.Get(r.Context(), domain.ID(key))
		if e != nil {
			return nil, e
		}
		out = append(out, subscriptions.ProjectManagement(v))
	}
	return page(out, ids, n, "subscriptions"), nil
}
func (s *Service) getSubscription(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	return s.subscription(r.Context(), key)
}
func (s *Service) saveSubscription(w http.ResponseWriter, r *http.Request) (any, error) {
	var in subscriptionWrite
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	key := domain.ID(r.PathValue("id"))
	if e := mutation(in.Mutation, key == ""); e != nil {
		return nil, e
	}
	if e := s.active(r.Context(), "profile", in.Profile.ID); e != nil {
		return nil, e
	}
	for _, p := range in.Routes {
		if e := s.active(r.Context(), "route", p.ID); e != nil {
			return nil, e
		}
	}
	d := stage3.SubscriptionDraft{ID: key, Name: in.Name, ExpiresAt: in.ExpiresAt, Profile: in.Profile, Routes: in.Routes, Preferences: in.Preferences.private()}
	for _, c := range in.InitialSelections {
		d.InitialSelections = append(d.InitialSelections, stage3.InitialRouteChoice{Name: c.Name, RouteID: c.RouteID})
	}
	var v stage3.SubscriptionView
	var e error
	if key == "" {
		v, e = s.Subscriptions.Create(r.Context(), in.Mutation, d)
	} else {
		v, e = s.Subscriptions.UpdateConfig(r.Context(), in.Mutation, d)
	}
	if e != nil {
		return nil, e
	}
	return s.subscription(r.Context(), v.Entity.ID)
}
func (s *Service) subscriptionAction(w http.ResponseWriter, r *http.Request) (domain.ID, domain.Mutation, error) {
	var m domain.Mutation
	e := decode(w, r, &m)
	if e == nil {
		e = mutation(m, false)
	}
	key := domain.ID(r.PathValue("id"))
	if !key.Valid() {
		e = problem(domain.InvalidArgument)
	}
	return key, m, e
}
func (s *Service) resetToken(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	v, e := s.Subscriptions.ResetDownloadToken(r.Context(), m, key)
	return subscriptions.ProjectManagement(v), e
}
func (s *Service) deleteSubscription(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	v, e := s.Subscriptions.Delete(r.Context(), m, key)
	return subscriptions.ProjectManagement(v), e
}
func (s *Service) renewSubscription(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		domain.Mutation
		ExpiresAt time.Time `json:"expires_at"`
	}
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, false); e != nil {
		return nil, e
	}
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	v, e := s.Subscriptions.Renew(r.Context(), in.Mutation, key, in.ExpiresAt)
	return subscriptions.ProjectManagement(v), e
}

type Label struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	EntryID     domain.ID `json:"entry_id"`
}

func (s *Service) previewSubscription(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	v, e := s.Subscriptions.Get(r.Context(), key)
	if e != nil {
		return nil, e
	}
	if v.Entity.Revision != m.ExpectedRevision {
		return nil, conflict(v.Entity.Revision)
	}
	detail, e := s.subscription(r.Context(), key)
	if e != nil {
		return nil, e
	}
	profile, e := s.Rules.Profile(r.Context(), v.Profile)
	if e != nil {
		return nil, e
	}
	p, e := s.Client.Preview(stage3.ClientSnapshot{SubscriptionID: key, Revision: v.Entity.Revision, Entries: v.Entries, Profile: profile, Preferences: detail.Preferences.private()})
	if e != nil {
		return nil, e
	}
	labels := []Label{}
	for _, v := range p.ProxyLabels {
		labels = append(labels, Label{v.Name, v.DisplayName, v.EntryID})
	}
	choices := []Choice{}
	for _, c := range p.Groups {
		choices = append(choices, Choice{c.Name, c.SelectedEntryID})
	}
	return map[string]any{"proxy_labels": labels, "groups": choices, "issues": issues(p.Issues)}, nil
}

// Explicitly adopt a reviewed rule Pin, retaining each route Pin and entry identity.
func (s *Service) updateSubscriptionProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		domain.Mutation
		Profile control.Pin `json:"profile"`
	}
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, false); e != nil {
		return nil, e
	}
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	if e = s.active(r.Context(), "profile", in.Profile.ID); e != nil {
		return nil, e
	}
	v, e := s.subscription(r.Context(), key)
	if e != nil {
		return nil, e
	}
	d := stage3.SubscriptionDraft{ID: key, Name: v.Name, Profile: in.Profile, Preferences: v.Preferences.private()}
	for _, e := range v.Entries {
		d.Routes = append(d.Routes, e.Route)
	}
	out, e := s.Subscriptions.UpdateConfig(r.Context(), in.Mutation, d)
	return subscriptions.ProjectManagement(out), e
}
