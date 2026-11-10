// Package gitlabtest is the in-memory adapter of the GitLab port. It implements the port directly and
// serves the GitLab REST subset the REST adapter uses (Handler), over the same state, so one contract
// suite runs against both adapters.
package gitlabtest

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	messageKey  = "message"
	notFoundMsg = "404 Not found"
)

type issue struct {
	path   string
	state  string
	labels []string
}

type mrKey struct{ projectID, iid int64 }

type mergeRequest struct {
	sourceProject int64
	sourceBranch  string
	state         string
	notes         []models.Note
}

// Fake is an in-memory GitLab: projects, issues, notes, merge requests, project members. A project exists once
// an issue of it was added; deleting the issue keeps the project. Each added or edited issue note gets a later
// UpdatedAt than the one before. Safe for concurrent use.
type Fake struct {
	mu        sync.Mutex
	bot       string
	projects  map[int64]bool
	issues    map[models.IssueKey]*issue
	mrs       map[mrKey]*mergeRequest
	notes     map[models.IssueKey][]models.Note
	members   map[int64]map[string]int
	nextID    int64
	ops       []string
	loadErr   []error
	writeErr  []error
	accessErr error
	accessQ   []error
	mrErr     []error
	projErr   []error
	accessN   int
	loadN     int
	refreshN  int
	blindN    int
	clock     time.Time
	onWrite   func(key models.IssueKey, w models.Writes)
	onLoad    func(key models.IssueKey)
}

// NewFake returns an empty fake; notes written through the port are authored by bot.
func NewFake(bot string) *Fake {
	return &Fake{
		bot:      bot,
		projects: map[int64]bool{},
		issues:   map[models.IssueKey]*issue{},
		mrs:      map[mrKey]*mergeRequest{},
		notes:    map[models.IssueKey][]models.Note{},
		members:  map[int64]map[string]int{},
		nextID:   100,
		clock:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// AddIssue adds an issue.
func (f *Fake) AddIssue(key models.IssueKey, path, state string, labels ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.projects[key.ProjectID] = true
	f.issues[key] = &issue{path: path, state: state, labels: slices.Clone(labels)}
}

// AddMergeRequest adds a merge request with a source branch and a state (opened, closed, merged).
func (f *Fake) AddMergeRequest(projectID, iid int64, sourceBranch, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.mrs[mrKey{projectID, iid}] = &mergeRequest{sourceProject: projectID, sourceBranch: sourceBranch, state: state}
}

// AddForkMergeRequest adds a merge request into projectID from a branch of another project (a fork).
func (f *Fake) AddForkMergeRequest(projectID, iid, sourceProjectID int64, sourceBranch, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.mrs[mrKey{projectID, iid}] = &mergeRequest{sourceProject: sourceProjectID, sourceBranch: sourceBranch, state: state}
}

// AddMRNote adds a note to a merge request; a zero ID gets the next id. It returns the note id.
func (f *Fake) AddMRNote(projectID, iid int64, n models.Note) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.addMRNoteLocked(mrKey{projectID, iid}, n)
}

func (f *Fake) addMRNoteLocked(k mrKey, n models.Note) int64 {
	if n.ID == 0 {
		f.nextID++
		n.ID = f.nextID
	}

	f.mrs[k].notes = append(f.mrs[k].notes, n)

	return n.ID
}

// MRNotes returns the notes of a merge request in note id order.
func (f *Fake) MRNotes(projectID, iid int64) []models.Note {
	f.mu.Lock()
	defer f.mu.Unlock()

	if mr := f.mrs[mrKey{projectID, iid}]; mr != nil {
		return slices.Clone(mr.notes)
	}

	return nil
}

// FailMR makes the next NoteMergeRequest calls return errs, one per call, before any change.
func (f *Fake) FailMR(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.mrErr = append(f.mrErr, errs...)
}

// FailProject makes the next CheckProject calls return errs, one per call.
func (f *Fake) FailProject(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.projErr = append(f.projErr, errs...)
}

// SetIssue changes the issue state and labels of an existing issue.
func (f *Fake) SetIssue(key models.IssueKey, state string, labels ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.issues[key].state = state
	f.issues[key].labels = slices.Clone(labels)
}

// DeleteIssue removes an issue and its notes.
func (f *Fake) DeleteIssue(key models.IssueKey) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.issues, key)
	delete(f.notes, key)
}

// DeleteNote removes one note of an issue.
func (f *Fake) DeleteNote(key models.IssueKey, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.notes[key] = slices.DeleteFunc(f.notes[key], func(n models.Note) bool { return n.ID == id })
}

// EditNote sets the body of one note of an issue.
func (f *Fake) EditNote(key models.IssueKey, id int64, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.notes[key] {
		if f.notes[key][i].ID == id {
			f.notes[key][i].Body = body
			f.notes[key][i].UpdatedAt = f.tickLocked()
		}
	}
}

// AddNote adds a note; a zero ID gets the next id and a zero UpdatedAt the next time. It returns the note id.
func (f *Fake) AddNote(key models.IssueKey, n models.Note) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.addNoteLocked(key, n)
}

func (f *Fake) addNoteLocked(key models.IssueKey, n models.Note) int64 {
	if n.ID == 0 {
		f.nextID++
		n.ID = f.nextID
	} else if n.ID > f.nextID {
		f.nextID = n.ID
	}

	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = f.tickLocked()
	}

	f.notes[key] = append(f.notes[key], n)

	return n.ID
}

func (f *Fake) tickLocked() time.Time {
	f.clock = f.clock.Add(time.Millisecond)

	return f.clock
}

// SetMember sets the access level of username on a project.
func (f *Fake) SetMember(projectID int64, username string, level int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.members[projectID] == nil {
		f.members[projectID] = map[string]int{}
	}

	f.members[projectID][username] = level
}

// FailLoad makes the next Load and Refresh calls return errs, one per call.
func (f *Fake) FailLoad(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.loadErr = append(f.loadErr, errs...)
}

// FailWrite makes the next Write calls return errs, one per call, before any change.
func (f *Fake) FailWrite(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.writeErr = append(f.writeErr, errs...)
}

// FailAccess makes every AccessLevel call return err; nil clears it.
func (f *Fake) FailAccess(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.accessErr = err
}

// FailAccessNext makes the next AccessLevel calls return errs, one per call.
func (f *Fake) FailAccessNext(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.accessQ = append(f.accessQ, errs...)
}

// OnLoad calls fn at the start of each Load through the port, outside the fake's lock.
func (f *Fake) OnLoad(fn func(key models.IssueKey)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onLoad = fn
}

// LoadCalls returns how many Load calls the port served.
func (f *Fake) LoadCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.loadN
}

// RefreshCalls returns how many Refresh calls the port served.
func (f *Fake) RefreshCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.refreshN
}

// BlindWrites returns how many Write calls of a record or a marker note the port served without a known view.
func (f *Fake) BlindWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.blindN
}

// OnWrite calls fn after each successful Write through the port, outside the fake's lock.
func (f *Fake) OnWrite(fn func(key models.IssueKey, w models.Writes)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onWrite = fn
}

// AccessCalls returns how many AccessLevel calls the port served.
func (f *Fake) AccessCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.accessN
}

// Labels returns the labels of an issue.
func (f *Fake) Labels(key models.IssueKey) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if is := f.issues[key]; is != nil {
		return slices.Clone(is.labels)
	}

	return nil
}

// Notes returns the notes of an issue in note id order.
func (f *Fake) Notes(key models.IssueKey) []models.Note {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.sortedNotesLocked(key)
}

// Record returns the record of the bot user's status note.
func (f *Fake) Record(key models.IssueKey) (models.Record, bool) {
	n, ok := models.StatusNote(f.Notes(key), f.bot)
	if !ok {
		return models.Record{}, false
	}

	return models.ParseRecord(n.Body)
}

// Ops returns the write operations in order: create-status, edit-status, "label <label>", note.
func (f *Fake) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.ops)
}

func (f *Fake) sortedNotesLocked(key models.IssueKey) []models.Note {
	out := slices.Clone(f.notes[key])
	slices.SortFunc(out, func(a, b models.Note) int { return cmp.Compare(a.ID, b.ID) })

	return out
}

func popErr(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}

	err := (*errs)[0]
	*errs = (*errs)[1:]

	return err
}

// Load implements the GitLab port.
func (f *Fake) Load(_ context.Context, key models.IssueKey) (models.IssueView, error) {
	f.mu.Lock()
	f.loadN++
	fn := f.onLoad
	f.mu.Unlock()

	if fn != nil {
		fn(key)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := popErr(&f.loadErr); err != nil {
		return models.IssueView{}, err
	}

	return f.viewLocked(key, f.notes[key])
}

// Refresh implements the GitLab port: prev with the notes added or edited since merged in. A note deleted since
// prev was read stays in the view.
func (f *Fake) Refresh(_ context.Context, prev models.IssueView) (models.IssueView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.refreshN++

	if err := popErr(&f.loadErr); err != nil {
		return models.IssueView{}, err
	}

	key := prev.Issue.Key
	since := models.LastUpdate(prev.Notes)

	var changed []models.Note

	for _, n := range f.notes[key] {
		if !n.UpdatedAt.Before(since) {
			changed = append(changed, n)
		}
	}

	return f.viewLocked(key, models.MergeNotes(prev.Notes, changed))
}

func (f *Fake) viewLocked(key models.IssueKey, notes []models.Note) (models.IssueView, error) {
	is := f.issues[key]
	if is == nil {
		return models.IssueView{}, fmt.Errorf("issue %s: %w", key, gitlab.ErrNotFound)
	}

	issue := models.Issue{Key: key, ProjectPath: is.path, IssueState: is.state, Labels: slices.Clone(is.labels)}

	return models.NewIssueView(issue, notes, f.bot), nil
}

// Write implements the GitLab port. The status note and the marker notes are found in the fake's own notes;
// known only counts.
func (f *Fake) Write(_ context.Context, key models.IssueKey, known *models.IssueView, w models.Writes) error {
	f.mu.Lock()

	marked := slices.ContainsFunc(w.Notes, func(n models.NewNote) bool { return n.Marker != "" })
	if known == nil && (w.Record != nil || marked) {
		f.blindN++
	}

	if err := popErr(&f.writeErr); err != nil {
		f.mu.Unlock()
		return err
	}

	if f.issues[key] == nil {
		f.mu.Unlock()
		return fmt.Errorf("issue %s: %w", key, gitlab.ErrNotFound)
	}

	if w.Record != nil {
		notes := f.sortedNotesLocked(key)
		prev, ok := models.StatusNote(notes, f.bot)
		view, _ := models.ParseStatusView(prev.Body)
		body := models.StatusBody(*w.Record, models.MergeStatus(view, *w.Record, w.Status))

		if ok {
			f.editNoteLocked(key, prev.ID, body)
		} else {
			f.createNoteLocked(key, body)
		}
	}

	if w.Label != "" {
		f.setLabelsLocked(key, []string{w.Label}, otherStateLabels(w.Label))
	}

	for _, n := range w.Notes {
		if n.Marker != "" && models.HasMarker(f.notes[key], f.bot, n.Marker) {
			continue
		}

		f.createNoteLocked(key, n.Body)
	}

	fn := f.onWrite
	f.mu.Unlock()

	if fn != nil {
		fn(key, w)
	}

	return nil
}

// AccessLevel implements the GitLab port.
func (f *Fake) AccessLevel(_ context.Context, projectID int64, username string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.accessN++

	if err := popErr(&f.accessQ); err != nil {
		return 0, err
	}

	if f.accessErr != nil {
		return 0, f.accessErr
	}

	return f.members[projectID][username], nil
}

// NoteMergeRequest implements the GitLab port.
func (f *Fake) NoteMergeRequest(_ context.Context, projectID int64, sourceBranch string, n models.NewNote) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := popErr(&f.mrErr); err != nil {
		return err
	}

	for _, k := range f.openMRsLocked(projectID, sourceBranch) {
		if n.Marker != "" && models.HasMarker(f.mrs[k].notes, f.bot, n.Marker) {
			continue
		}

		f.addMRNoteLocked(k, models.Note{Body: n.Body, Author: f.bot})
	}

	return nil
}

// CheckProject implements the GitLab port.
func (f *Fake) CheckProject(_ context.Context, projectID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := popErr(&f.projErr); err != nil {
		return err
	}

	if !f.projects[projectID] {
		return fmt.Errorf("project %d: %w", projectID, gitlab.ErrNotFound)
	}

	return nil
}

func (f *Fake) openMRsLocked(projectID int64, sourceBranch string) []mrKey {
	var out []mrKey

	for k, mr := range f.mrs {
		if k.projectID == projectID && mr.state == "opened" && mr.sourceProject == projectID &&
			mr.sourceBranch == sourceBranch {
			out = append(out, k)
		}
	}

	slices.SortFunc(out, func(a, b mrKey) int { return cmp.Compare(a.iid, b.iid) })

	return out
}

func otherStateLabels(label string) []string {
	return slices.DeleteFunc(models.StateLabels(), func(l string) bool { return l == label })
}

func (f *Fake) createNoteLocked(key models.IssueKey, body string) models.Note {
	op := "note"
	if _, ok := models.ParseRecord(body); ok {
		op = "create-status"
	}

	f.ops = append(f.ops, op)
	n := models.Note{Body: body, Author: f.bot}
	n.ID = f.addNoteLocked(key, n)

	return n
}

func (f *Fake) editNoteLocked(key models.IssueKey, id int64, body string) bool {
	for i := range f.notes[key] {
		if f.notes[key][i].ID == id {
			f.ops = append(f.ops, "edit-status")
			f.notes[key][i].Body = body
			f.notes[key][i].UpdatedAt = f.tickLocked()

			return true
		}
	}

	return false
}

func (f *Fake) setLabelsLocked(key models.IssueKey, add, remove []string) {
	is := f.issues[key]
	is.labels = slices.DeleteFunc(is.labels, func(l string) bool { return slices.Contains(remove, l) })

	for _, l := range add {
		if !slices.Contains(is.labels, l) {
			is.labels = append(is.labels, l)
		}
	}

	f.ops = append(f.ops, "label "+strings.Join(add, ","))
}

// Handler serves the GitLab REST subset used by the REST adapter. Requests need PRIVATE-TOKEN: token.
func (f *Fake) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{pid}/issues/{iid}", f.withIssue(f.getIssue))
	mux.HandleFunc("PUT /api/v4/projects/{pid}/issues/{iid}", f.withIssue(f.putIssue))
	mux.HandleFunc("GET /api/v4/projects/{pid}/issues/{iid}/notes", f.withIssue(f.listNotes))
	mux.HandleFunc("POST /api/v4/projects/{pid}/issues/{iid}/notes", f.withIssue(f.postNote))
	mux.HandleFunc("PUT /api/v4/projects/{pid}/issues/{iid}/notes/{nid}", f.withIssue(f.putNote))
	mux.HandleFunc("GET /api/v4/projects/{pid}/members/all", f.listMembers)
	mux.HandleFunc("GET /api/v4/projects/{pid}", f.getProject)
	mux.HandleFunc("GET /api/v4/projects/{pid}/merge_requests", f.listMRs)
	mux.HandleFunc("GET /api/v4/projects/{pid}/merge_requests/{mrid}/notes", f.withMR(f.listMRNotes))
	mux.HandleFunc("POST /api/v4/projects/{pid}/merge_requests/{mrid}/notes", f.withMR(f.postMRNote))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{messageKey: "401 Unauthorized"})
			return
		}

		mux.ServeHTTP(w, r)
	})
}

type issueHandler func(w http.ResponseWriter, r *http.Request, key models.IssueKey)

func (f *Fake) withIssue(h issueHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err1 := strconv.ParseInt(r.PathValue("pid"), 10, 64)
		iid, err2 := strconv.ParseInt(r.PathValue("iid"), 10, 64)
		key := models.IssueKey{ProjectID: pid, IID: iid}

		f.mu.Lock()
		missing := f.issues[key] == nil
		f.mu.Unlock()

		if err1 != nil || err2 != nil || missing {
			writeJSON(w, http.StatusNotFound, map[string]string{messageKey: notFoundMsg})
			return
		}

		h(w, r, key)
	}
}

type jsonNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	System    bool      `json:"system"`
	UpdatedAt time.Time `json:"updated_at"`
	Author    struct {
		Username string `json:"username"`
	} `json:"author"`
}

func toJSONNote(n models.Note) jsonNote {
	j := jsonNote{ID: n.ID, Body: n.Body, System: n.System, UpdatedAt: n.UpdatedAt}
	j.Author.Username = n.Author

	return j
}

func (f *Fake) getIssue(w http.ResponseWriter, _ *http.Request, key models.IssueKey) {
	f.mu.Lock()
	is := *f.issues[key]
	f.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"iid":        key.IID,
		"project_id": key.ProjectID,
		"state":      is.state,
		"labels":     is.labels,
		"references": map[string]string{"full": fmt.Sprintf("%s#%d", is.path, key.IID)},
	})
}

func (f *Fake) putIssue(w http.ResponseWriter, r *http.Request, key models.IssueKey) {
	var req struct {
		AddLabels    string `json:"add_labels"`
		RemoveLabels string `json:"remove_labels"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{messageKey: err.Error()})
		return
	}

	f.mu.Lock()
	f.setLabelsLocked(key, splitList(req.AddLabels), splitList(req.RemoveLabels))
	f.mu.Unlock()
	f.getIssue(w, r, key)
}

func (f *Fake) listNotes(w http.ResponseWriter, r *http.Request, key models.IssueKey) {
	f.mu.Lock()
	notes := f.sortedNotesLocked(key)
	f.mu.Unlock()

	if r.URL.Query().Get("order_by") == "updated_at" {
		slices.SortStableFunc(notes, func(a, b models.Note) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	}

	if r.URL.Query().Get("sort") != "asc" {
		slices.Reverse(notes)
	}

	items := paginate(w, r, len(notes))
	out := make([]jsonNote, 0, len(items))

	for _, i := range items {
		out = append(out, toJSONNote(notes[i]))
	}

	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) postNote(w http.ResponseWriter, r *http.Request, key models.IssueKey) {
	var req struct {
		Body string `json:"body"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{messageKey: "body is missing"})
		return
	}

	f.mu.Lock()
	n := f.createNoteLocked(key, req.Body)
	f.mu.Unlock()

	writeJSON(w, http.StatusCreated, toJSONNote(n))
}

func (f *Fake) putNote(w http.ResponseWriter, r *http.Request, key models.IssueKey) {
	var req struct {
		Body string `json:"body"`
	}

	nid, err := strconv.ParseInt(r.PathValue("nid"), 10, 64)
	if err != nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{messageKey: "bad request"})
		return
	}

	f.mu.Lock()
	ok := f.editNoteLocked(key, nid, req.Body)
	f.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{messageKey: notFoundMsg})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": nid, "body": req.Body})
}

func (f *Fake) getProject(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.ParseInt(r.PathValue("pid"), 10, 64)

	f.mu.Lock()
	ok := err == nil && f.projects[pid]
	f.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{messageKey: "404 Project Not Found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": pid})
}

func (f *Fake) listMRs(w http.ResponseWriter, r *http.Request) {
	pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)

	f.mu.Lock()
	type jsonMR struct {
		IID             int64  `json:"iid"`
		SourceProjectID int64  `json:"source_project_id"`
		SourceBranch    string `json:"source_branch"`
		State           string `json:"state"`
	}

	var all []jsonMR

	for k, mr := range f.mrs {
		q := r.URL.Query()
		if k.projectID != pid || (q.Get("state") != "" && q.Get("state") != mr.state) ||
			(q.Get("source_branch") != "" && q.Get("source_branch") != mr.sourceBranch) {
			continue
		}

		all = append(all, jsonMR{
			IID: k.iid, SourceProjectID: mr.sourceProject, SourceBranch: mr.sourceBranch, State: mr.state,
		})
	}
	f.mu.Unlock()

	slices.SortFunc(all, func(a, b jsonMR) int { return cmp.Compare(a.IID, b.IID) })

	items := paginate(w, r, len(all))
	out := make([]jsonMR, 0, len(items))

	for _, i := range items {
		out = append(out, all[i])
	}

	writeJSON(w, http.StatusOK, out)
}

type mrHandler func(w http.ResponseWriter, r *http.Request, k mrKey)

func (f *Fake) withMR(h mrHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err1 := strconv.ParseInt(r.PathValue("pid"), 10, 64)
		iid, err2 := strconv.ParseInt(r.PathValue("mrid"), 10, 64)
		k := mrKey{pid, iid}

		f.mu.Lock()
		missing := f.mrs[k] == nil
		f.mu.Unlock()

		if err1 != nil || err2 != nil || missing {
			writeJSON(w, http.StatusNotFound, map[string]string{messageKey: notFoundMsg})
			return
		}

		h(w, r, k)
	}
}

func (f *Fake) listMRNotes(w http.ResponseWriter, r *http.Request, k mrKey) {
	f.mu.Lock()
	notes := slices.Clone(f.mrs[k].notes)
	f.mu.Unlock()

	items := paginate(w, r, len(notes))
	out := make([]jsonNote, 0, len(items))

	for _, i := range items {
		out = append(out, toJSONNote(notes[i]))
	}

	writeJSON(w, http.StatusOK, out)
}

func (f *Fake) postMRNote(w http.ResponseWriter, r *http.Request, k mrKey) {
	var req struct {
		Body string `json:"body"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{messageKey: "body is missing"})
		return
	}

	f.mu.Lock()
	n := models.Note{Body: req.Body, Author: f.bot}
	n.ID = f.addMRNoteLocked(k, n)
	f.mu.Unlock()

	writeJSON(w, http.StatusCreated, toJSONNote(n))
}

func (f *Fake) listMembers(w http.ResponseWriter, r *http.Request) {
	pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	query := r.URL.Query().Get("query")

	f.mu.Lock()
	type member struct {
		Username    string `json:"username"`
		AccessLevel int    `json:"access_level"`
	}

	var all []member

	for u, lvl := range f.members[pid] {
		if strings.Contains(u, query) {
			all = append(all, member{Username: u, AccessLevel: lvl})
		}
	}
	f.mu.Unlock()

	slices.SortFunc(all, func(a, b member) int { return strings.Compare(a.Username, b.Username) })

	items := paginate(w, r, len(all))
	out := make([]member, 0, len(items))

	for _, i := range items {
		out = append(out, all[i])
	}

	writeJSON(w, http.StatusOK, out)
}

// paginate applies GitLab offset pagination (page, per_page ≤ 100, default 20), sets the X-Next-Page header
// and returns the item indexes on the page.
func paginate(w http.ResponseWriter, r *http.Request, total int) []int {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))

	if page < 1 {
		page = 1
	}

	if per < 1 {
		per = 20
	}

	per = min(per, 100)
	start := min((page-1)*per, total)
	end := min(start+per, total)

	next := ""
	if end < total {
		next = strconv.Itoa(page + 1)
	}

	w.Header().Set("X-Next-Page", next)
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", strconv.Itoa(per))

	idx := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		idx = append(idx, i)
	}

	return idx
}

func splitList(s string) []string {
	var out []string

	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
