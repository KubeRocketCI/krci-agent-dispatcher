// Package gitlab is the GitLab REST adapter of the dispatcher's GitLab port.
package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// ErrNotFound is returned when the issue or project does not exist.
var ErrNotFound = errors.New("not found")

const (
	perPage        = 100
	bodyKey        = "body"
	requestTimeout = 30 * time.Second
)

// REST talks to the GitLab REST API v4 with a personal access token sent in the PRIVATE-TOKEN header.
type REST struct {
	client *resty.Client
	bot    string
}

// Option configures NewREST.
type Option func(*resty.Client)

// WithTokenFile reads the token from path on each request, so a rotated Secret takes effect without a restart.
// It replaces the token argument of NewREST.
func WithTokenFile(path string) Option {
	return func(c *resty.Client) {
		c.OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
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

// NewREST returns a REST adapter for baseURL. caFile, when set, adds a PEM CA bundle to the system roots.
// bot is the token's user: the author of the status note and of the notes it writes.
func NewREST(baseURL, token, caFile, bot string, opts ...Option) (*REST, error) {
	client := resty.New().
		SetBaseURL(strings.TrimRight(baseURL, "/")+"/api/v4").
		SetHeader("Accept", "application/json").
		SetTimeout(requestTimeout)

	if token != "" {
		client.SetHeader("PRIVATE-TOKEN", token)
	}

	for _, o := range opts {
		o(client)
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

	return &REST{client: client, bot: bot}, nil
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
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

type apiMember struct {
	Username    string `json:"username"`
	AccessLevel int    `json:"access_level"`
}

func issuePath(key models.IssueKey) string {
	return fmt.Sprintf("/projects/%d/issues/%d", key.ProjectID, key.IID)
}

// check turns a response into an error: ErrNotFound for 404, a status error for other non-2xx codes.
func check(resp *resty.Response, err error, method, path string) error {
	if err != nil {
		return fmt.Errorf("gitlab %s %s: %w", method, path, err)
	}

	switch {
	case resp.StatusCode() == http.StatusNotFound:
		return fmt.Errorf("gitlab %s %s: %w", method, path, ErrNotFound)
	case !resp.IsSuccess():
		return fmt.Errorf("gitlab %s %s: status %s", method, path, resp.Status())
	}

	return nil
}

// Load reads the issue and all its notes (every page), and parses the record of the status note.
func (c *REST) Load(ctx context.Context, key models.IssueKey) (models.IssueView, error) {
	var is apiIssue

	path := issuePath(key)

	resp, err := c.client.R().SetContext(ctx).SetResult(&is).Get(path)
	if err := check(resp, err, http.MethodGet, path); err != nil {
		return models.IssueView{}, err
	}

	notes, err := c.notes(ctx, key)
	if err != nil {
		return models.IssueView{}, err
	}

	v := models.IssueView{
		Issue: models.Issue{
			Key:         key,
			ProjectPath: strings.TrimSuffix(is.References.Full, "#"+strconv.FormatInt(key.IID, 10)),
			IssueState:  is.State,
			Labels:      is.Labels,
		},
		Notes: notes,
	}

	if n, ok := models.StatusNote(notes, c.bot); ok {
		v.Record, v.HasRecord = models.ParseRecord(n.Body)
		v.Status, _ = models.ParseStatusView(n.Body)
	}

	return v, nil
}

func (c *REST) notes(ctx context.Context, key models.IssueKey) ([]models.Note, error) {
	return c.notesAt(ctx, issuePath(key)+"/notes")
}

// notesAt reads every page of a notes list (issue or merge request) in note id order.
func (c *REST) notesAt(ctx context.Context, path string) ([]models.Note, error) {
	var out []models.Note

	err := pages(ctx, c.client, path, map[string]string{"sort": "asc", "order_by": "created_at"},
		func(resp *resty.Response) (int, error) {
			var page []apiNote
			if err := json.Unmarshal(resp.Body(), &page); err != nil {
				return 0, err
			}

			for _, n := range page {
				out = append(out, models.Note{ID: n.ID, Body: n.Body, Author: n.Author.Username, System: n.System})
			}

			return len(page), nil
		})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b models.Note) int { return int(a.ID - b.ID) })

	return out, nil
}

// pages walks GitLab offset pagination: it follows X-Next-Page and stops on an empty or short page.
func pages(ctx context.Context, client *resty.Client, path string, query map[string]string,
	each func(resp *resty.Response) (int, error),
) error {
	for page := 1; ; {
		resp, err := client.R().SetContext(ctx).SetQueryParams(query).
			SetQueryParam("per_page", strconv.Itoa(perPage)).SetQueryParam("page", strconv.Itoa(page)).
			Get(path)
		if err := check(resp, err, http.MethodGet, path); err != nil {
			return err
		}

		n, err := each(resp)
		if err != nil {
			return fmt.Errorf("gitlab GET %s: decode page %d: %w", path, page, err)
		}

		next, err := strconv.Atoi(resp.Header().Get("X-Next-Page"))
		if err != nil || next <= page || n == 0 {
			return nil
		}

		page = next
	}
}

// Write applies w in order: record and status note, then the state label (the other state labels removed),
// then the notes. A note whose marker is already on the issue from the bot user is skipped.
func (c *REST) Write(ctx context.Context, key models.IssueKey, w models.Writes) error {
	var notes []models.Note

	if w.Record != nil || len(w.Notes) > 0 {
		var err error
		if notes, err = c.notes(ctx, key); err != nil {
			return err
		}
	}

	if w.Record != nil {
		if err := c.writeStatus(ctx, key, notes, *w.Record, w.Status); err != nil {
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

func (c *REST) writeStatus(ctx context.Context, key models.IssueKey, notes []models.Note, r models.Record,
	next *models.StatusView,
) error {
	prev, ok := models.StatusNote(notes, c.bot)
	view, _ := models.ParseStatusView(prev.Body)
	body := models.StatusBody(r, models.MergeStatus(view, r, next))

	if !ok {
		_, err := c.postNote(ctx, key, body)
		return err
	}

	path := fmt.Sprintf("%s/notes/%d", issuePath(key), prev.ID)
	resp, err := c.client.R().SetContext(ctx).SetBody(map[string]string{bodyKey: body}).Put(path)

	return check(resp, err, http.MethodPut, path)
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

func (c *REST) setStateLabel(ctx context.Context, key models.IssueKey, label string) error {
	remove := slices.DeleteFunc(models.StateLabels(), func(l string) bool { return l == label })
	path := issuePath(key)

	resp, err := c.client.R().SetContext(ctx).
		SetBody(map[string]string{"add_labels": label, "remove_labels": strings.Join(remove, ",")}).Put(path)

	return check(resp, err, http.MethodPut, path)
}

// AccessLevel returns the access level of username on the project, including inherited membership; 0 when
// the user is not a member.
func (c *REST) AccessLevel(ctx context.Context, projectID int64, username string) (int, error) {
	level := 0
	path := fmt.Sprintf("/projects/%d/members/all", projectID)

	err := pages(ctx, c.client, path, map[string]string{"query": username}, func(resp *resty.Response) (int, error) {
		var page []apiMember
		if err := json.Unmarshal(resp.Body(), &page); err != nil {
			return 0, err
		}

		for _, m := range page {
			if strings.EqualFold(m.Username, username) {
				level = max(level, m.AccessLevel)
			}
		}

		return len(page), nil
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
		func(resp *resty.Response) (int, error) {
			var page []struct {
				IID             int64  `json:"iid"`
				SourceProjectID int64  `json:"source_project_id"`
				SourceBranch    string `json:"source_branch"`
			}

			if err := json.Unmarshal(resp.Body(), &page); err != nil {
				return 0, err
			}

			for _, mr := range page {
				if mr.SourceProjectID == projectID && mr.SourceBranch == sourceBranch {
					iids = append(iids, mr.IID)
				}
			}

			return len(page), nil
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

// CheckProject returns nil when the project is readable with the token, ErrNotFound when GitLab returns 404.
func (c *REST) CheckProject(ctx context.Context, projectID int64) error {
	path := fmt.Sprintf("/projects/%d", projectID)
	resp, err := c.client.R().SetContext(ctx).Get(path)

	return check(resp, err, http.MethodGet, path)
}
