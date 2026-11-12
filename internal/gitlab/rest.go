// Package gitlab is the GitLab REST adapter of the dispatcher's GitLab port.
package gitlab

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	perPage        = 100
	bodyKey        = "body"
	requestTimeout = 30 * time.Second
)

// REST talks to the GitLab REST API v4 with a personal access token sent in the PRIVATE-TOKEN header.
type REST struct {
	client  *resty.Client
	bot     string
	trigger models.Role
	labels  models.AgentLabels
	log     *slog.Logger
}

// Option configures NewREST.
type Option func(*REST)

// WithTokenFile reads the token from path on each request, so a rotated Secret takes effect without a restart.
// It replaces the token argument of NewREST.
func WithTokenFile(path string) Option {
	return func(c *REST) {
		c.client.OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read GitLab token file: %w", err)
			}

			tok := strings.TrimSpace(string(b))
			if tok == "" {
				return errors.New("gitLab token file is empty")
			}

			r.SetHeader("PRIVATE-TOKEN", tok)

			return nil
		})
	}
}

// WithAgentLabels sets the agent label names; the default is the zero AgentLabels.
func WithAgentLabels(labels models.AgentLabels) Option {
	return func(c *REST) { c.labels = labels }
}

// WithLogger sets the logger; the default is slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(c *REST) { c.log = log }
}

// NewREST returns a REST adapter for baseURL. caFile, when set, adds a PEM CA bundle to the system roots.
// bot is the token's user: the author of the status note and of the notes it writes. trigger is the trigger role:
// developer, maintainer or owner.
func NewREST(baseURL, token, caFile, bot string, trigger models.Role, opts ...Option) (*REST, error) {
	if !trigger.IsTrigger() {
		return nil, fmt.Errorf("trigger role %s is not developer, maintainer or owner", trigger)
	}

	client := resty.New().
		SetBaseURL(strings.TrimRight(baseURL, "/")+"/api/v4").
		SetHeader("Accept", "application/json").
		SetTimeout(requestTimeout)

	if token != "" {
		client.SetHeader("PRIVATE-TOKEN", token)
	}

	c := &REST{client: client, bot: bot, trigger: trigger, log: slog.Default()}
	for _, o := range opts {
		o(c)
	}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read GitLab CA file: %w", err)
		}

		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}

		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no PEM certificate in %s", caFile)
		}

		client.SetTLSClientConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	}

	return c, nil
}

type apiIssue struct {
	IID        int64    `json:"iid"`
	State      string   `json:"state"`
	Labels     []string `json:"labels"`
	References struct {
		Full string `json:"full"`
	} `json:"references"`
}

type apiNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	System    bool      `json:"system"`
	UpdatedAt time.Time `json:"updated_at"`
	Author    struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (n apiNote) note() models.Note {
	return models.Note{ID: n.ID, Body: n.Body, Author: n.Author.Username, System: n.System, UpdatedAt: n.UpdatedAt}
}

type apiMember struct {
	Username    string `json:"username"`
	AccessLevel int    `json:"access_level"`
}

func issuePath(key models.IssueKey) string {
	return fmt.Sprintf("/projects/%d/issues/%d", key.ProjectID, key.IID)
}

// check turns a response into an error: models.ErrNotFound for 404, models.ErrForbidden for 403, a status
// error for other non-2xx codes.
func check(resp *resty.Response, err error, method, path string) error {
	if err != nil {
		return fmt.Errorf("gitlab %s %s: %w", method, path, err)
	}

	switch {
	case resp.StatusCode() == http.StatusNotFound:
		return fmt.Errorf("gitlab %s %s: %w", method, path, models.ErrNotFound)
	case resp.StatusCode() == http.StatusForbidden:
		return fmt.Errorf("gitlab %s %s: %w", method, path, models.ErrForbidden)
	case !resp.IsSuccess():
		return fmt.Errorf("gitlab %s %s: status %s", method, path, resp.Status())
	}

	return nil
}

// Load reads the issue and all its notes (every page), and parses the record of the status note.
func (c *REST) Load(ctx context.Context, key models.IssueKey) (models.IssueView, error) {
	is, err := c.issue(ctx, key)
	if err != nil {
		return models.IssueView{}, err
	}

	notes, err := c.notes(ctx, key)
	if err != nil {
		return models.IssueView{}, err
	}

	return models.NewIssueView(is, notes, c.bot), nil
}

// Refresh reads the issue again and the notes created or edited since prev was read, newest first until a note
// older than the latest of prev, and returns prev with them merged in. A note deleted since prev was read stays.
func (c *REST) Refresh(ctx context.Context, prev models.IssueView) (models.IssueView, error) {
	key := prev.Issue.Key

	is, err := c.issue(ctx, key)
	if err != nil {
		return models.IssueView{}, err
	}

	changed, err := c.notesSince(ctx, key, models.LastUpdate(prev.Notes))
	if err != nil {
		return models.IssueView{}, err
	}

	return models.NewIssueView(is, models.MergeNotes(prev.Notes, changed), c.bot), nil
}

func (c *REST) issue(ctx context.Context, key models.IssueKey) (models.Issue, error) {
	var is apiIssue

	path := issuePath(key)

	resp, err := c.client.R().SetContext(ctx).SetResult(&is).Get(path)
	if err := check(resp, err, http.MethodGet, path); err != nil {
		return models.Issue{}, err
	}

	return models.Issue{
		Key:         key,
		ProjectPath: strings.TrimSuffix(is.References.Full, "#"+strconv.FormatInt(key.IID, 10)),
		IssueState:  is.State,
		Labels:      is.Labels,
	}, nil
}

func (c *REST) notes(ctx context.Context, key models.IssueKey) ([]models.Note, error) {
	return c.notesAt(ctx, issuePath(key)+"/notes")
}

// notesAt reads every page of a notes list (issue or merge request) in note id order.
func (c *REST) notesAt(ctx context.Context, path string) ([]models.Note, error) {
	var out []models.Note

	err := pages(ctx, c.client, path, map[string]string{"sort": "asc", "order_by": "created_at"},
		func(resp *resty.Response) (bool, error) {
			var page []apiNote
			if err := json.Unmarshal(resp.Body(), &page); err != nil {
				return false, err
			}

			for _, n := range page {
				out = append(out, n.note())
			}

			return len(page) > 0, nil
		})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b models.Note) int { return cmp.Compare(a.ID, b.ID) })

	return out, nil
}

// notesSince reads the issue notes whose updated_at is since or later: pages ordered by updated_at, newest
// first, up to the first older note. A note with updated_at equal to since is read again.
func (c *REST) notesSince(ctx context.Context, key models.IssueKey, since time.Time) ([]models.Note, error) {
	var out []models.Note

	err := pages(ctx, c.client, issuePath(key)+"/notes", map[string]string{"sort": "desc", "order_by": "updated_at"},
		func(resp *resty.Response) (bool, error) {
			var page []apiNote
			if err := json.Unmarshal(resp.Body(), &page); err != nil {
				return false, err
			}

			for _, n := range page {
				if n.UpdatedAt.Before(since) {
					return false, nil
				}

				out = append(out, n.note())
			}

			return len(page) > 0, nil
		})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// pages walks GitLab offset pagination: it follows X-Next-Page and stops after the last page or when each
// returns false.
func pages(ctx context.Context, client *resty.Client, path string, query map[string]string,
	each func(resp *resty.Response) (bool, error),
) error {
	for page := 1; ; {
		resp, err := client.R().SetContext(ctx).SetQueryParams(query).
			SetQueryParam("per_page", strconv.Itoa(perPage)).SetQueryParam("page", strconv.Itoa(page)).
			Get(path)
		if err := check(resp, err, http.MethodGet, path); err != nil {
			return err
		}

		more, err := each(resp)
		if err != nil {
			return fmt.Errorf("gitlab GET %s: decode page %d: %w", path, page, err)
		}

		next, err := strconv.Atoi(resp.Header().Get("X-Next-Page"))
		if err != nil || next <= page || !more {
			return nil
		}

		page = next
	}
}

// Write applies w in order: record and status note, then the state label (the other state labels removed),
// then the notes. A note whose marker is already on the issue from the bot user is skipped. The status note and
// the marker notes are found in known; the notes are read when known is nil, or holds no status note for a
// record, or names a status note that GitLab no longer has.
func (c *REST) Write(ctx context.Context, key models.IssueKey, known *models.IssueView, w models.Writes) error {
	var notes []models.Note

	switch {
	case known != nil:
		notes = known.Notes
	case w.Record != nil || slices.ContainsFunc(w.Notes, func(n models.NewNote) bool { return n.Marker != "" }):
		var err error
		if notes, err = c.notes(ctx, key); err != nil {
			return err
		}
	}

	if w.Record != nil {
		var err error
		if notes, err = c.writeStatus(ctx, key, notes, known != nil, *w.Record, w.Status); err != nil {
			return err
		}
	}

	if w.Label != "" {
		if err := c.setStateLabel(ctx, key, w.Label); err != nil {
			return err
		}
	}

	for _, n := range w.Notes {
		if n.Marker != "" && models.HasMarker(notes, c.bot, n.Marker) {
			continue
		}

		posted, err := c.postNote(ctx, key, n.Body)
		if err != nil {
			return err
		}

		notes = append(notes, posted)
	}

	return nil
}

// writeStatus edits the status note of notes, or posts one. With stale notes (not read for this write) it reads
// the notes first when they hold no status note, and again when the edit returns 404. It returns the notes used.
func (c *REST) writeStatus(ctx context.Context, key models.IssueKey, notes []models.Note, stale bool, r models.Record,
	next *models.StatusView,
) ([]models.Note, error) {
	prev, ok := models.StatusNote(notes, c.bot)

	if !ok && stale {
		var err error
		if notes, err = c.notes(ctx, key); err != nil {
			return nil, err
		}

		prev, ok = models.StatusNote(notes, c.bot)
		stale = false
	}

	view, _ := models.ParseStatusView(prev.Body)
	body := models.StatusBody(r, models.MergeStatus(view, r, next))

	if !ok {
		posted, err := c.postNote(ctx, key, body)
		if err != nil {
			return nil, err
		}

		return append(slices.Clone(notes), posted), nil
	}

	path := fmt.Sprintf("%s/notes/%d", issuePath(key), prev.ID)
	resp, err := c.client.R().SetContext(ctx).SetBody(map[string]string{bodyKey: body}).Put(path)

	err = check(resp, err, http.MethodPut, path)
	if errors.Is(err, models.ErrNotFound) && stale {
		if notes, err = c.notes(ctx, key); err != nil {
			return nil, err
		}

		return c.writeStatus(ctx, key, notes, false, r, next)
	}

	return notes, err
}

func (c *REST) postNote(ctx context.Context, key models.IssueKey, body string) (models.Note, error) {
	var n apiNote

	path := issuePath(key) + "/notes"
	resp, err := c.client.R().SetContext(ctx).SetBody(map[string]string{bodyKey: body}).SetResult(&n).Post(path)

	if err := check(resp, err, http.MethodPost, path); err != nil {
		return models.Note{}, err
	}

	return models.Note{ID: n.ID, Body: body, Author: c.bot}, nil
}

func (c *REST) setStateLabel(ctx context.Context, key models.IssueKey, s models.AgentState) error {
	label := c.labels.State(s)
	remove := slices.DeleteFunc(c.labels.States(), func(l string) bool { return l == label })
	path := issuePath(key)

	resp, err := c.client.R().SetContext(ctx).
		SetBody(map[string]string{"add_labels": label, "remove_labels": strings.Join(remove, ",")}).Put(path)

	return check(resp, err, http.MethodPut, path)
}

// CanTrigger reports whether username, matched case-insensitively, has the trigger role or higher on the project,
// including inherited membership. An empty username, a non-member and a members list that GitLab does not show to
// the token (404, 403) mean false; the last is logged as a warning.
func (c *REST) CanTrigger(ctx context.Context, projectID int64, username string) (bool, error) {
	if username == "" {
		return false, nil
	}

	level, err := c.accessLevel(ctx, projectID, username)

	switch {
	case errors.Is(err, models.ErrNotFound), errors.Is(err, models.ErrForbidden):
		c.log.Warn("Project members not shown to the bot user; the user cannot trigger the agent",
			"project", projectID, "user", username, "error", err)

		return false, nil
	case err != nil:
		return false, err
	}

	return models.Role(level) >= c.trigger, nil
}

// accessLevel returns the highest access level of username on the project; 0 when the user is not a member.
func (c *REST) accessLevel(ctx context.Context, projectID int64, username string) (int, error) {
	level := 0
	path := fmt.Sprintf("/projects/%d/members/all", projectID)

	err := pages(ctx, c.client, path, map[string]string{"query": username}, func(resp *resty.Response) (bool, error) {
		var page []apiMember
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return false, err
		}

		for _, m := range page {
			if strings.EqualFold(m.Username, username) {
				level = max(level, m.AccessLevel)
			}
		}

		return len(page) > 0, nil
	})
	if err != nil {
		return 0, err
	}

	return level, nil
}

// NoteMergeRequest posts n on each opened merge request of the project from its own sourceBranch (not from a
// fork). A merge request that already has a bot user note with n.Marker is skipped. No such merge request is not
// an error.
func (c *REST) NoteMergeRequest(ctx context.Context, projectID int64, sourceBranch string, n models.NewNote) error {
	var iids []int64

	path := fmt.Sprintf("/projects/%d/merge_requests", projectID)

	err := pages(ctx, c.client, path, map[string]string{"state": "opened", "source_branch": sourceBranch},
		func(resp *resty.Response) (bool, error) {
			var page []struct {
				IID             int64  `json:"iid"`
				SourceProjectID int64  `json:"source_project_id"`
				SourceBranch    string `json:"source_branch"`
			}

			if err := json.Unmarshal(resp.Body(), &page); err != nil {
				return false, err
			}

			for _, mr := range page {
				if mr.SourceProjectID == projectID && mr.SourceBranch == sourceBranch {
					iids = append(iids, mr.IID)
				}
			}

			return len(page) > 0, nil
		})
	if err != nil {
		return err
	}

	for _, iid := range iids {
		notesPath := fmt.Sprintf("%s/%d/notes", path, iid)

		notes, err := c.notesAt(ctx, notesPath)
		if err != nil {
			return err
		}

		if n.Marker != "" && models.HasMarker(notes, c.bot, n.Marker) {
			continue
		}

		resp, err := c.client.R().SetContext(ctx).SetBody(map[string]string{bodyKey: n.Body}).Post(notesPath)
		if err := check(resp, err, http.MethodPost, notesPath); err != nil {
			return err
		}
	}

	return nil
}

// CheckProject returns nil when the project is readable with the token, models.ErrNotFound when GitLab
// returns 404, models.ErrForbidden when it returns 403.
func (c *REST) CheckProject(ctx context.Context, projectID int64) error {
	path := fmt.Sprintf("/projects/%d", projectID)
	resp, err := c.client.R().SetContext(ctx).Get(path)

	return check(resp, err, http.MethodGet, path)
}
