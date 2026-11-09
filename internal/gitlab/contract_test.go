package gitlab_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	bot   = "krci-agent"
	token = "glpat-contract-secret"
)

var key = models.IssueKey{ProjectID: 12, IID: 7}

// port is the GitLab port as the dispatcher declares it.
type port interface {
	Load(ctx context.Context, key models.IssueKey) (models.IssueView, error)
	Write(ctx context.Context, key models.IssueKey, w models.Writes) error
	AccessLevel(ctx context.Context, projectID int64, username string) (int, error)
	NoteMergeRequest(ctx context.Context, projectID int64, sourceBranch string, n models.NewNote) error
	CheckProject(ctx context.Context, projectID int64) error
}

var stopped = models.NewNote{Body: models.StoppedMRText + "\n" + models.StoppedMRMarker, Marker: models.StoppedMRMarker}

// subjects returns each adapter under test, built over the fake's state.
func subjects() map[string]func(t *testing.T, f *gitlabtest.Fake) port {
	return map[string]func(t *testing.T, f *gitlabtest.Fake) port{
		"fake": func(_ *testing.T, f *gitlabtest.Fake) port { return f },
		"rest": func(t *testing.T, f *gitlabtest.Fake) port {
			srv := httptest.NewServer(f.Handler(token))
			t.Cleanup(srv.Close)

			c, err := gitlab.NewREST(srv.URL, token, "", bot)
			require.NoError(t, err)

			return c
		},
	}
}

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, f *gitlabtest.Fake, p port){
		"LoadReadsAllPages":               LoadReadsAllPages,
		"LoadParsesRecord":                LoadParsesRecord,
		"LoadNotFound":                    LoadNotFound,
		"LoadIssueFields":                 LoadIssueFields,
		"WriteOrderRecordLabelNotes":      WriteOrderRecordLabelNotes,
		"WriteSingleStateLabel":           WriteSingleStateLabel,
		"WriteSkipsExistingMarkerNote":    WriteSkipsExistingMarkerNote,
		"WriteCreatesThenEditsStatusNote": WriteCreatesThenEditsStatusNote,
		"WriteKeepsStatusRowsWithoutView": WriteKeepsStatusRowsWithoutView,
		"WriteNotFound":                   WriteNotFound,
		"AccessLevelMember":               AccessLevelMember,
		"AccessLevelNotMember":            AccessLevelNotMember,
		"NoteMergeRequestPostsOnce":       NoteMergeRequestPostsOnce,
		"NoteMergeRequestSkipsOtherMRs":   NoteMergeRequestSkipsOtherMRs,
		"NoteMergeRequestIgnoresPerson":   NoteMergeRequestIgnoresPersonMarker,
		"NoteMergeRequestReadsAllPages":   NoteMergeRequestReadsAllPages,
		"CheckProjectReadable":            CheckProjectReadable,
		"NoteMergeRequestSkipsForkMR":     NoteMergeRequestSkipsForkMR,
	}

	for subject, build := range subjects() {
		for name, run := range cases {
			t.Run(subject+"/"+name, func(t *testing.T) {
				f := gitlabtest.NewFake(bot)
				f.AddIssue(key, "krci/test-go-app", models.IssueOpened, "agent")
				run(t, f, build(t, f))
			})
		}
	}
}

func LoadReadsAllPages(t *testing.T, f *gitlabtest.Fake, p port) {
	for i := range 250 {
		f.AddNote(key, models.Note{Body: fmt.Sprintf("comment %d", i), Author: "owner"})
	}

	v, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	require.Len(t, v.Notes, 250)
	assert.Equal(t, "comment 0", v.Notes[0].Body)
	assert.Equal(t, "comment 249", v.Notes[249].Body)

	for i := 1; i < len(v.Notes); i++ {
		assert.Less(t, v.Notes[i-1].ID, v.Notes[i].ID)
	}
}

func LoadParsesRecord(t *testing.T, f *gitlabtest.Fake, p port) {
	r := models.Record{State: models.Error, Reason: models.ReasonTurnLimit}
	body := models.StatusBody(r, models.StatusView{State: models.Error, Session: "s1"})
	f.AddNote(key, models.Note{Body: body, Author: bot})
	f.AddNote(key, models.Note{Body: models.RecordLine(models.Record{State: models.Done}), Author: "owner"})

	v, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	assert.True(t, v.HasRecord)
	assert.Equal(t, r, v.Record)
	assert.Equal(t, "s1", v.Status.Session)
}

func LoadNotFound(t *testing.T, _ *gitlabtest.Fake, p port) {
	_, err := p.Load(context.Background(), models.IssueKey{ProjectID: 12, IID: 99})
	require.ErrorIs(t, err, gitlab.ErrNotFound)

	_, err = p.Load(context.Background(), models.IssueKey{ProjectID: 99, IID: 7})
	require.ErrorIs(t, err, gitlab.ErrNotFound)
}

func LoadIssueFields(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetIssue(key, models.IssueClosed, "agent", "agent::waiting")
	f.AddNote(key, models.Note{Body: "label changed", Author: "owner", System: true})

	v, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, models.Issue{Key: key, ProjectPath: "krci/test-go-app", IssueState: models.IssueClosed,
		Labels: []string{"agent", "agent::waiting"}}, v.Issue)
	assert.False(t, v.HasRecord)
	require.Len(t, v.Notes, 1)
	assert.True(t, v.Notes[0].System)
	assert.Equal(t, "owner", v.Notes[0].Author)
}

func WriteOrderRecordLabelNotes(t *testing.T, f *gitlabtest.Fake, p port) {
	err := p.Write(context.Background(), key, models.Writes{
		Notes:  []models.NewNote{{Body: models.NoRoomText}},
		Label:  models.StateLabel(models.Waiting),
		Record: &models.Record{State: models.Waiting},
		Status: &models.StatusView{Actor: key.ActorName()},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"create-status", "label agent::waiting", "note"}, f.Ops())
}

func WriteSingleStateLabel(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetIssue(key, models.IssueOpened, "agent", "bug", "agent::working", "agent::error")

	require.NoError(t, p.Write(context.Background(), key, models.Writes{Label: models.StateLabel(models.Done)}))
	assert.ElementsMatch(t, []string{"agent", "bug", "agent::done"}, f.Labels(key))
}

func WriteSkipsExistingMarkerNote(t *testing.T, f *gitlabtest.Fake, p port) {
	refused := models.NewNote{Body: models.RefusedText + "\n" + models.RefusedMarker, Marker: models.RefusedMarker}
	f.AddNote(key, models.Note{Body: refused.Body, Author: "owner"})

	require.NoError(t, p.Write(context.Background(), key, models.Writes{Notes: []models.NewNote{refused}}))
	require.NoError(t, p.Write(context.Background(), key, models.Writes{Notes: []models.NewNote{refused}}))

	bots := 0

	for _, n := range f.Notes(key) {
		if n.Author == bot {
			bots++
		}
	}

	assert.Equal(t, 1, bots, "posted once; a person's copy does not count")
}

func WriteCreatesThenEditsStatusNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	require.NoError(t, p.Write(ctx, key, models.Writes{Record: &models.Record{State: models.Waiting},
		Status: &models.StatusView{Session: "s1"}}))
	require.NoError(t, p.Write(ctx, key, models.Writes{Record: &models.Record{State: models.Working},
		Status: &models.StatusView{Session: "s1"}}))

	notes := f.Notes(key)
	require.Len(t, notes, 1)
	r, ok := models.ParseRecord(notes[0].Body)
	assert.True(t, ok)
	assert.Equal(t, models.Working, r.State)
	assert.Equal(t, bot, notes[0].Author)
	assert.Equal(t, []string{"create-status", "edit-status"}, f.Ops())
}

func WriteKeepsStatusRowsWithoutView(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	require.NoError(t, p.Write(ctx, key, models.Writes{Record: &models.Record{State: models.Working},
		Status: &models.StatusView{Actor: key.ActorName(), Session: "s1"}}))

	busy := models.Record{State: models.Error, Reason: models.ReasonActorBusy}
	require.NoError(t, p.Write(ctx, key, models.Writes{Record: &busy}))

	v, err := p.Load(ctx, key)
	require.NoError(t, err)

	want := models.StatusView{State: models.Error, Actor: key.ActorName(), Session: "s1", Reason: models.ReasonActorBusy}
	assert.Equal(t, want, v.Status)
}

func WriteNotFound(t *testing.T, _ *gitlabtest.Fake, p port) {
	missing := models.IssueKey{ProjectID: 12, IID: 99}
	err := p.Write(context.Background(), missing, models.Writes{Label: models.StateLabel(models.Done)})
	require.ErrorIs(t, err, gitlab.ErrNotFound)
}

func AccessLevelMember(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "dev", 30)
	f.SetMember(12, "developer", 40)
	f.SetMember(13, "dev", 50)

	lvl, err := p.AccessLevel(context.Background(), 12, "dev")
	require.NoError(t, err)
	assert.Equal(t, 30, lvl)
}

func AccessLevelNotMember(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "developer", 40)

	lvl, err := p.AccessLevel(context.Background(), 12, "dev")
	require.NoError(t, err)
	assert.Equal(t, 0, lvl)
}

func NoteMergeRequestPostsOnce(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddMergeRequest(12, 6, "agent/issue-7", "opened")

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))
	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))

	notes := f.MRNotes(12, 6)
	require.Len(t, notes, 1)
	assert.Equal(t, bot, notes[0].Author)
	assert.Equal(t, stopped.Body, notes[0].Body)
}

func NoteMergeRequestSkipsOtherMRs(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddMergeRequest(12, 5, "agent/issue-7", "merged")
	f.AddMergeRequest(12, 4, "agent/issue-7", "closed")
	f.AddMergeRequest(12, 3, "agent/issue-8", "opened")
	f.AddMergeRequest(13, 2, "agent/issue-7", "opened")

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))

	for _, mr := range []struct{ pid, iid int64 }{{12, 5}, {12, 4}, {12, 3}, {13, 2}} {
		assert.Empty(t, f.MRNotes(mr.pid, mr.iid), "MR %d!%d", mr.pid, mr.iid)
	}
}

func NoteMergeRequestIgnoresPersonMarker(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddMergeRequest(12, 6, "agent/issue-7", "opened")
	f.AddMRNote(12, 6, models.Note{Author: "owner", Body: models.StoppedMRMarker})

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))
	assert.Len(t, f.MRNotes(12, 6), 2, "a person's marker does not count")
}

func NoteMergeRequestReadsAllPages(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddMergeRequest(12, 6, "agent/issue-7", "opened")

	for i := range 150 {
		f.AddMRNote(12, 6, models.Note{Author: "owner", Body: fmt.Sprintf("comment %d", i)})
	}

	f.AddMRNote(12, 6, models.Note{Author: bot, Body: stopped.Body})

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))
	assert.Len(t, f.MRNotes(12, 6), 151, "the marker on page 2 is seen")
}

func CheckProjectReadable(t *testing.T, f *gitlabtest.Fake, p port) {
	f.DeleteIssue(key)

	require.NoError(t, p.CheckProject(context.Background(), 12), "a deleted issue keeps its project")
	require.ErrorIs(t, p.CheckProject(context.Background(), 99), gitlab.ErrNotFound)
}

func NoteMergeRequestSkipsForkMR(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddForkMergeRequest(12, 8, 40, "agent/issue-7", "opened")

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))
	assert.Empty(t, f.MRNotes(12, 8), "an MR from a fork branch of the same name is not the agent's")
}
