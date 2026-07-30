package data

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
)

// SetIncidentField patches a single-value incident field — "state"
// (active/stable/resolved) or "severity" (SEV-1…SEV-5). Both share the same
// single-value attribute shape, so one method covers them. A write; UI-gated.
func (l *Live) SetIncidentField(ctx context.Context, id, field, value string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sv := datadogV2.NewIncidentFieldAttributesSingleValue()
	sv.SetValue(value)
	attrs := datadogV2.NewIncidentUpdateAttributes()
	attrs.SetFields(map[string]datadogV2.IncidentFieldAttributes{
		field: datadogV2.IncidentFieldAttributesSingleValueAsIncidentFieldAttributes(sv),
	})
	data := datadogV2.NewIncidentUpdateData(id, datadogV2.INCIDENTTYPE_INCIDENTS)
	data.SetAttributes(*attrs)
	body := datadogV2.NewIncidentUpdateRequest(*data)

	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncident(ctx, id, *body)
	l.track(resp)
	if err != nil {
		return apiErr("set incident "+field, err)
	}
	slog.Info("incident field changed", "id", id, "field", field, "value", value)
	return nil
}

// CurrentUser returns the acting user (GET /api/v2/current_user): its id drives
// commander assignment, its handle is the default to-do assignee.
func (l *Live) CurrentUser(ctx context.Context) (User, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, httpresp, err := datadogV2.NewUsersApi(l.client).GetCurrentUser(ctx)
	l.track(httpresp)
	if err != nil {
		return User{}, apiErr("current user", err)
	}
	u := resp.GetData()
	attrs := u.GetAttributes()
	handle := attrs.GetHandle()
	if handle == "" {
		handle = attrs.GetEmail()
	}
	return User{ID: u.GetId(), Handle: handle}, nil
}

// SetIncidentCommander assigns an incident's commander via the commander_user
// relationship on UpdateIncident (a write; UI-gated).
func (l *Live) SetIncidentCommander(ctx context.Context, incidentID, userID string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body := commanderUpdateBody(incidentID, userID)
	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncident(ctx, incidentID, body)
	l.track(resp)
	if err != nil {
		return apiErr("set incident commander", err)
	}
	slog.Info("incident commander assigned", "id", incidentID, "user", userID)
	return nil
}

// commanderUpdateBody builds the UpdateIncident request that sets commander_user
// to a user. Extracted so a test can assert its wire shape — the nested
// nullable-relationship construction is easy to get subtly wrong and can't be
// runtime-tested from the authoring sandbox.
func commanderUpdateBody(incidentID, userID string) datadogV2.IncidentUpdateRequest {
	relData := datadogV2.NewNullableRelationshipToUserData(userID, datadogV2.USERSTYPE_USERS)
	rel := datadogV2.NewNullableRelationshipToUser(*datadogV2.NewNullableNullableRelationshipToUserData(relData))
	rels := datadogV2.NewIncidentUpdateRelationships()
	rels.CommanderUser = *datadogV2.NewNullableNullableRelationshipToUser(rel)
	data := datadogV2.NewIncidentUpdateData(incidentID, datadogV2.INCIDENTTYPE_INCIDENTS)
	data.SetRelationships(*rels)
	return *datadogV2.NewIncidentUpdateRequest(*data)
}

// AddIncidentTodo adds a to-do (action item) to an incident, assigned to the
// given user handle (the API requires at least one assignee).
func (l *Live) AddIncidentTodo(ctx context.Context, incidentID, content, assigneeHandle string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	assignee := datadogV2.IncidentTodoAssigneeHandleAsIncidentTodoAssignee(&assigneeHandle)
	attrs := datadogV2.NewIncidentTodoAttributes([]datadogV2.IncidentTodoAssignee{assignee}, content)
	data := datadogV2.NewIncidentTodoCreateData(*attrs, datadogV2.INCIDENTTODOTYPE_INCIDENT_TODOS)
	body := datadogV2.NewIncidentTodoCreateRequest(*data)

	_, resp, err := datadogV2.NewIncidentsApi(l.client).CreateIncidentTodo(ctx, incidentID, *body)
	l.track(resp)
	if err != nil {
		return apiErr("add incident to-do", err)
	}
	slog.Info("incident to-do added", "id", incidentID, "assignee", assigneeHandle)
	return nil
}

// ListUsers searches active org users (GET /api/v2/users). The query is the
// server-side filter (name/email/handle); one bounded page so a picker never
// spends unbounded budget on an org with thousands of users.
func (l *Live) ListUsers(ctx context.Context, query string) ([]User, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	opts := datadogV2.NewListUsersOptionalParameters().
		WithFilterStatus("Active").
		WithPageSize(50)
	if query != "" {
		opts = opts.WithFilter(query)
	}
	resp, httpresp, err := datadogV2.NewUsersApi(l.client).ListUsers(ctx, *opts)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("list users", err)
	}
	data := resp.GetData()
	users := make([]User, 0, len(data))
	for _, u := range data {
		attrs := u.GetAttributes()
		handle := attrs.GetHandle()
		if handle == "" {
			handle = attrs.GetEmail()
		}
		users = append(users, User{ID: u.GetId(), Handle: handle, Name: attrs.GetName()})
	}
	return users, nil
}

// IncidentTodos lists an incident's to-dos (GET /api/v2/incidents/{id}/relationships/todos).
func (l *Live) IncidentTodos(ctx context.Context, incidentID string) ([]Todo, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewIncidentsApi(l.client).ListIncidentTodos(ctx, incidentID)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("list incident to-dos", err)
	}
	data := resp.GetData()
	todos := make([]Todo, 0, len(data))
	for _, td := range data {
		attrs := td.GetAttributes()
		todos = append(todos, Todo{
			ID:        td.GetId(),
			Content:   attrs.GetContent(),
			Assignees: todoAssigneeHandles(attrs.GetAssignees()),
			Completed: attrs.GetCompleted() != "", // non-empty completed timestamp = done
		})
	}
	return todos, nil
}

// SetIncidentTodoCompleted marks a to-do done (a completion timestamp) or
// undone (null). Content and assignees are re-sent from the loaded Todo so the
// PATCH doesn't blank them — the attributes constructor requires both.
func (l *Live) SetIncidentTodoCompleted(ctx context.Context, incidentID string, todo Todo, done bool) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body := todoCompletedPatchBody(todo, done, time.Now().UTC().Format(time.RFC3339))
	_, resp, err := datadogV2.NewIncidentsApi(l.client).UpdateIncidentTodo(ctx, incidentID, todo.ID, body)
	l.track(resp)
	if err != nil {
		return apiErr("update incident to-do", err)
	}
	slog.Info("incident to-do completion changed", "id", incidentID, "todo", todo.ID, "done", done)
	return nil
}

// todoCompletedPatchBody builds the PATCH request that flips a to-do's
// completion. Extracted so a test can assert its wire shape: content and
// assignees are re-sent (the attributes constructor requires them) so the PATCH
// never blanks them; completed is the timestamp (done) or null (reopened).
func todoCompletedPatchBody(todo Todo, done bool, now string) datadogV2.IncidentTodoPatchRequest {
	assignees := make([]datadogV2.IncidentTodoAssignee, 0, len(todo.Assignees))
	for i := range todo.Assignees {
		h := todo.Assignees[i]
		assignees = append(assignees, datadogV2.IncidentTodoAssigneeHandleAsIncidentTodoAssignee(&h))
	}
	attrs := datadogV2.NewIncidentTodoAttributes(assignees, todo.Content)
	if done {
		attrs.SetCompleted(now)
	} else {
		attrs.SetCompletedNil()
	}
	data := datadogV2.NewIncidentTodoPatchData(*attrs, datadogV2.INCIDENTTODOTYPE_INCIDENT_TODOS)
	return *datadogV2.NewIncidentTodoPatchRequest(*data)
}

// DeleteIncidentTodo removes a to-do from an incident.
func (l *Live) DeleteIncidentTodo(ctx context.Context, incidentID, todoID string) error {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := datadogV2.NewIncidentsApi(l.client).DeleteIncidentTodo(ctx, incidentID, todoID)
	l.track(resp)
	if err != nil {
		return apiErr("delete incident to-do", err)
	}
	slog.Info("incident to-do deleted", "id", incidentID, "todo", todoID)
	return nil
}

// todoAssigneeHandles pulls the handle from each to-do assignee (the handle
// arm of the assignee union); anonymous assignees are skipped.
func todoAssigneeHandles(as []datadogV2.IncidentTodoAssignee) []string {
	var out []string
	for i := range as {
		if h := as[i].IncidentTodoAssigneeHandle; h != nil && *h != "" {
			out = append(out, *h)
		}
	}
	return out
}

// incidentDetail builds the structured war-room summary from a fetched
// incident: title/severity/state/created/impact plus every non-empty field,
// alongside the resolved People and the raw object.
func incidentDetail(in datadogV2.IncidentResponse) *IncidentDetail {
	data := in.GetData()
	attrs := data.GetAttributes()
	fields := map[string]string{}
	for name := range attrs.GetFields() {
		if v := incidentField(attrs.GetFields(), name); v != "" {
			fields[name] = v
		}
	}
	return &IncidentDetail{
		Title:            attrs.GetTitle(),
		Severity:         string(attrs.GetSeverity()),
		State:            incidentField(attrs.GetFields(), "state"),
		Created:          attrs.GetCreated().Format(time.RFC3339),
		CustomerImpacted: attrs.GetCustomerImpacted(),
		ImpactScope:      attrs.GetCustomerImpactScope(),
		Fields:           fields,
		People:           incidentPeople(in),
		Incident:         in,
	}
}

// IncidentImpacts lists an incident's declared impacts, one line each.
func (l *Live) IncidentImpacts(ctx context.Context, incidentID string) ([]string, error) {
	ctx = l.authCtx(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, httpresp, err := datadogV2.NewIncidentsApi(l.client).ListIncidentImpacts(ctx, incidentID)
	l.track(httpresp)
	if err != nil {
		return nil, apiErr("incident impacts", err)
	}
	var out []string
	for _, d := range resp.GetData() {
		attrs := d.GetAttributes()
		line := attrs.GetDescription()
		if t := attrs.GetImpactType(); t != "" {
			line = t + ": " + line
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// incidentPeople resolves an incident's commander/created/declared/responders
// to handles using the response's included users. Commander and created/declared
// ids are user ids (resolve cleanly); responder ids are a distinct object type
// with no include support, so an unresolved one falls back to its raw id.
func incidentPeople(in datadogV2.IncidentResponse) IncidentPeople {
	users := map[string]string{}
	for _, item := range in.GetIncluded() {
		if u := item.IncidentUserData; u != nil {
			users[u.GetId()] = incUserHandle(u)
		}
	}
	resolve := func(id string) string {
		if id == "" {
			return ""
		}
		if h, ok := users[id]; ok {
			return h
		}
		return id
	}

	data := in.GetData()
	rels := data.GetRelationships()
	var p IncidentPeople

	cu := rels.GetCommanderUser()
	cud := cu.GetData()
	p.Commander = resolve(cud.GetId())

	cb := rels.GetCreatedByUser()
	cbd := cb.GetData()
	p.CreatedBy = resolve(cbd.GetId())

	db := rels.GetDeclaredByUser()
	dbd := db.GetData()
	p.DeclaredBy = resolve(dbd.GetId())

	rr := rels.GetResponders()
	for _, rd := range rr.GetData() {
		if h := resolve(rd.GetId()); h != "" {
			p.Responders = append(p.Responders, h)
		}
	}
	return p
}

// incUserHandle prefers handle, then name, then email, then id.
func incUserHandle(u *datadogV2.IncidentUserData) string {
	attrs := u.GetAttributes()
	if h := attrs.GetHandle(); h != "" {
		return h
	}
	if n := attrs.GetName(); n != "" {
		return n
	}
	if e := attrs.GetEmail(); e != "" {
		return e
	}
	return u.GetId()
}

func (l *Live) incidents(ctx context.Context) ([]Row, error) {
	api := datadogV2.NewIncidentsApi(l.client)
	var data []datadogV2.IncidentResponseData
	for page := int64(0); page < maxIncidentPage; page++ {
		resp, httpresp, err := api.ListIncidents(ctx,
			*datadogV2.NewListIncidentsOptionalParameters().
				WithPageSize(incidentPage).WithPageOffset(page * incidentPage))
		l.track(httpresp)
		if err != nil {
			return nil, apiErr("incidents", err)
		}
		got := resp.GetData()
		data = append(data, got...)
		if int64(len(got)) < incidentPage {
			break
		}
		if page == maxIncidentPage-1 {
			slog.Warn("incident list truncated", "cap", maxIncidentPage*incidentPage)
		}
	}
	rows := make([]Row, 0, len(data))
	for _, d := range data {
		a := d.GetAttributes()
		sev := incidentField(a.GetFields(), "severity")
		state := incidentField(a.GetFields(), "state")
		impact := ""
		if a.GetCustomerImpacted() {
			impact = "customer"
		}
		publicID := fmt.Sprintf("%d", a.GetPublicId())
		rows = append(rows, Row{
			ID:    d.GetId(),
			Cells: []string{"IR-" + publicID, sev, state, a.GetTitle(), impact, a.GetCreated().Local().Format("2006-01-02 15:04"), incidentTags(a.GetFields())},
			Raw:   d,
			URL:   l.web + "/incidents/" + publicID,
		})
	}
	return rows, nil
}

// incidentField reads an incident field value, handling both arms of the
// IncidentFieldAttributes union: single-value fields (state, severity) return
// their value; multi-value fields (multiselect custom fields) join their
// values. A missing field or an unparsed variant yields "" rather than
// breaking the row — real orgs carry custom fields of either shape.
func incidentField(fields map[string]datadogV2.IncidentFieldAttributes, key string) string {
	f, ok := fields[key]
	if !ok {
		return ""
	}
	if sv := f.IncidentFieldAttributesSingleValue; sv != nil {
		return sv.GetValue()
	}
	if mv := f.IncidentFieldAttributesMultipleValue; mv != nil {
		return strings.Join(mv.GetValue(), ", ")
	}
	return ""
}

// incidentTags renders an incident's fields as "k:v" tags. Incidents carry no
// tag list of their own — their routing metadata lives in fields (services,
// teams, root cause, custom multiselects) — so flattening them gives the
// TAGS column the same filter syntax as every other view.
func incidentTags(fields map[string]datadogV2.IncidentFieldAttributes) string {
	skip := map[string]bool{"state": true, "severity": true, "detection_method": true}
	out := make([]string, 0, len(fields))
	for k := range fields {
		if skip[k] { // already their own columns
			continue
		}
		v := incidentField(fields, k)
		if v == "" {
			continue
		}
		for _, part := range strings.Split(v, ", ") {
			if part != "" {
				out = append(out, k+":"+part)
			}
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}
