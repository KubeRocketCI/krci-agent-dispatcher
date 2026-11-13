package gitlab_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/gitlab/gitlabtest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

const (
	bot   = "krci-agent"
	token = "glpat-contract-secret"
)

var (
	key         = models.IssueKey{ProjectID: 12, IID: 7}
	agentLabels models.AgentLabels
)

// port is the GitLab port as the dispatcher declares it.
type port = dispatcher.GitLab

// replyPort is the GitLab port plus the calls the agent harness makes.
type replyPort interface {
	port
	PostReply(ctx context.Context, key models.IssueKey, body string) (models.Note, error)
	DefaultBranch(ctx context.Context, projectID int64) (string, error)
}

var stopped = models.NewNote{Body: models.StoppedMRText + "\n" + models.StoppedMRMarker, Marker: models.StoppedMRMarker}

// subjects returns each adapter under test with trigger role trigger, built over the fake's state.
func subjects(trigger models.Role) map[string]func(t *testing.T, f *gitlabtest.Fake) replyPort {
	return map[string]func(t *testing.T, f *gitlabtest.Fake) replyPort{
		"fake": func(_ *testing.T, f *gitlabtest.Fake) replyPort {
			f.SetTriggerRole(trigger)

			return f
		},
		"rest": func(t *testing.T, f *gitlabtest.Fake) replyPort {
			srv := httptest.NewServer(f.Handler(token))
			t.Cleanup(srv.Close)

			c, err := gitlab.NewREST(srv.URL, token, "", bot, trigger, gitlab.WithLogger(slog.New(slog.DiscardHandler)))
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
		"LoadForbidden":                   LoadForbidden,
		"WriteForbidden":                  WriteForbidden,
		"CheckProjectForbidden":           CheckProjectForbidden,
		"CanTriggerDeveloperOrHigher":     CanTriggerDeveloperOrHigher,
		"CanTriggerBelowDeveloper":        CanTriggerBelowDeveloper,
		"CanTriggerNotMember":             CanTriggerNotMember,
		"CanTriggerCaseVariantUsername":   CanTriggerCaseVariantUsername,
		"CanTriggerEmptyUsername":         CanTriggerEmptyUsername,
		"CanTriggerMembersHidden":         CanTriggerMembersHidden,
		"CanTriggerLookupError":           CanTriggerLookupError,
		"NoteMergeRequestPostsOnce":       NoteMergeRequestPostsOnce,
		"NoteMergeRequestSkipsOtherMRs":   NoteMergeRequestSkipsOtherMRs,
		"NoteMergeRequestIgnoresPerson":   NoteMergeRequestIgnoresPersonMarker,
		"NoteMergeRequestReadsAllPages":   NoteMergeRequestReadsAllPages,
		"CheckProjectReadable":            CheckProjectReadable,
		"NoteMergeRequestSkipsForkMR":     NoteMergeRequestSkipsForkMR,
		"RefreshMergesNewAndEditedNotes":  RefreshMergesNewAndEditedNotes,
		"RefreshReadsIssueFields":         RefreshReadsIssueFields,
		"RefreshReadsSameTimeNote":        RefreshReadsSameTimeNote,
		"RefreshReadsAllNewPages":         RefreshReadsAllNewPages,
		"RefreshFromEmptyView":            RefreshFromEmptyView,
		"RefreshKeepsDeletedNote":         RefreshKeepsDeletedNote,
		"RefreshNotFound":                 RefreshNotFound,
		"WriteKnownEditsStatusNote":       WriteKnownEditsStatusNote,
		"WriteKnownWithoutStatusNote":     WriteKnownWithoutStatusNote,
		"WriteKnownStatusNoteDeleted":     WriteKnownStatusNoteDeleted,
		"WriteKnownSkipsMarkerNote":       WriteKnownSkipsMarkerNote,
		"WriteRetryAfterSavedMarkerNote":  WriteRetryAfterSavedMarkerNote,
		"WriteRetryAfterSavedStatusNote":  WriteRetryAfterSavedStatusNote,
		"WriteSavedStatusNoteEndsWrite":   WriteSavedStatusNoteEndsWrite,
		"WriteFailBeforeNote":             WriteFailBeforeNote,
	}

	for subject, build := range subjects(models.Developer) {
		for name, run := range cases {
			t.Run(subject+"/"+name, func(t *testing.T) {
				f := gitlabtest.NewFake(bot)
				f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())
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
	require.ErrorIs(t, err, models.ErrNotFound)

	_, err = p.Load(context.Background(), models.IssueKey{ProjectID: 99, IID: 7})
	require.ErrorIs(t, err, models.ErrNotFound)
}

func LoadIssueFields(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetIssue(key, models.IssueClosed, agentLabels.Trigger(), agentLabels.State(models.Waiting))
	f.AddNote(key, models.Note{Body: "label changed", Author: "owner", System: true})

	v, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, models.Issue{Key: key, ProjectPath: "krci/test-go-app", IssueState: models.IssueClosed,
		Labels: []string{agentLabels.Trigger(), agentLabels.State(models.Waiting)}}, v.Issue)
	assert.False(t, v.HasRecord)
	require.Len(t, v.Notes, 1)
	assert.True(t, v.Notes[0].System)
	assert.Equal(t, "owner", v.Notes[0].Author)
}

func WriteOrderRecordLabelNotes(t *testing.T, f *gitlabtest.Fake, p port) {
	err := p.Write(context.Background(), key, nil, models.Writes{
		Notes:  []models.NewNote{{Body: models.NoRoomText}},
		Label:  models.Waiting,
		Record: &models.Record{State: models.Waiting},
		Status: &models.StatusView{Actor: key.ActorName()},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"create-status", "label krci::agent::waiting", "note"}, f.Ops())
}

func WriteSingleStateLabel(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetIssue(key, models.IssueOpened, agentLabels.Trigger(), "bug",
		agentLabels.State(models.Working), agentLabels.State(models.Error))

	require.NoError(t, p.Write(context.Background(), key, nil, models.Writes{Label: models.Done}))
	assert.ElementsMatch(t, []string{agentLabels.Trigger(), "bug", agentLabels.State(models.Done)}, f.Labels(key))
}

// refusedNote is a note with a marker.
func refusedNote() models.NewNote {
	return models.NewNote{Body: models.StartRefusedText(models.Developer) + "\n" + models.StartRefusedMarker,
		Marker: models.StartRefusedMarker}
}

func WriteSkipsExistingMarkerNote(t *testing.T, f *gitlabtest.Fake, p port) {
	refused := refusedNote()
	f.AddNote(key, models.Note{Body: refused.Body, Author: "owner"})

	require.NoError(t, p.Write(context.Background(), key, nil, models.Writes{Notes: []models.NewNote{refused}}))
	require.NoError(t, p.Write(context.Background(), key, nil, models.Writes{Notes: []models.NewNote{refused}}))

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
	require.NoError(t, p.Write(ctx, key, nil, models.Writes{Record: &models.Record{State: models.Waiting},
		Status: &models.StatusView{Session: "s1"}}))
	require.NoError(t, p.Write(ctx, key, nil, models.Writes{Record: &models.Record{State: models.Working},
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
	require.NoError(t, p.Write(ctx, key, nil, models.Writes{Record: &models.Record{State: models.Working},
		Status: &models.StatusView{Actor: key.ActorName(), Session: "s1"}}))

	busy := models.Record{State: models.Error, Reason: models.ReasonActorBusy}
	require.NoError(t, p.Write(ctx, key, nil, models.Writes{Record: &busy}))

	v, err := p.Load(ctx, key)
	require.NoError(t, err)

	want := models.StatusView{State: models.Error, Actor: key.ActorName(), Session: "s1", Reason: models.ReasonActorBusy}
	assert.Equal(t, want, v.Status)
}

func WriteNotFound(t *testing.T, _ *gitlabtest.Fake, p port) {
	missing := models.IssueKey{ProjectID: 12, IID: 99}
	err := p.Write(context.Background(), missing, nil, models.Writes{Label: models.Done})
	require.ErrorIs(t, err, models.ErrNotFound)
}

func LoadForbidden(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetForbidden(key.ProjectID)

	_, err := p.Load(context.Background(), key)
	require.ErrorIs(t, err, models.ErrForbidden)
	assert.NotContains(t, err.Error(), token)
}

func WriteForbidden(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetForbidden(key.ProjectID)

	err := p.Write(context.Background(), key, nil, models.Writes{
		Record: &models.Record{State: models.Waiting}, Label: models.Waiting,
	})
	require.ErrorIs(t, err, models.ErrForbidden)
	assert.Empty(t, f.Ops(), "a forbidden write changes nothing")
}

func CheckProjectForbidden(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetForbidden(key.ProjectID)

	require.ErrorIs(t, p.CheckProject(context.Background(), key.ProjectID), models.ErrForbidden)
}

func CanTriggerDeveloperOrHigher(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "dev", 30)
	f.SetMember(12, "lead", 40)
	f.SetMember(12, "owner", 50)

	for _, u := range []string{"dev", "lead", "owner"} {
		ok, err := p.CanTrigger(context.Background(), 12, u)
		require.NoError(t, err)
		assert.True(t, ok, u)
	}
}

func CanTriggerBelowDeveloper(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "rep", 20)
	f.SetMember(12, "guest", 10)
	f.SetMember(13, "rep", 50)

	for _, u := range []string{"rep", "guest"} {
		ok, err := p.CanTrigger(context.Background(), 12, u)
		require.NoError(t, err)
		assert.False(t, ok, u)
	}
}

func CanTriggerNotMember(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "developer", 40)

	ok, err := p.CanTrigger(context.Background(), 12, "dev")
	require.NoError(t, err)
	assert.False(t, ok, "a member whose name contains the username does not count")
}

func CanTriggerCaseVariantUsername(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "Dev.User", 30)

	for _, u := range []string{"dev.user", "DEV.USER", "Dev.User"} {
		ok, err := p.CanTrigger(context.Background(), 12, u)
		require.NoError(t, err)
		assert.True(t, ok, u)
	}
}

func CanTriggerEmptyUsername(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "dev", 50)

	ok, err := p.CanTrigger(context.Background(), 12, "")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, f.MembersLookups(), "no lookup for an empty username")
}

func CanTriggerMembersHidden(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "dev", 30)

	for _, e := range []error{models.ErrNotFound, models.ErrForbidden} {
		f.FailCanTriggerNext(fmt.Errorf("members: %w", e))

		ok, err := p.CanTrigger(context.Background(), 12, "dev")
		require.NoError(t, err, e.Error())
		assert.False(t, ok, e.Error())
	}
}

func CanTriggerLookupError(t *testing.T, f *gitlabtest.Fake, p port) {
	f.SetMember(12, "dev", 30)
	f.FailCanTriggerNext(errors.New("gitlab down"))

	ok, err := p.CanTrigger(context.Background(), 12, "dev")
	require.Error(t, err, "a lookup error is an error, not false")
	assert.False(t, ok)
	assert.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotErrorIs(t, err, models.ErrForbidden)
}

func TestContractReply(t *testing.T) {
	cases := map[string]func(t *testing.T, f *gitlabtest.Fake, p replyPort){
		"PostReplyReturnsSavedNote": PostReplyReturnsSavedNote,
		"PostReplyNotFound":         PostReplyNotFound,
		"PostReplyForbidden":        PostReplyForbidden,
		"PostReplyFailAfterSave":    PostReplyFailAfterSave,
		"PostReplyFailBeforeSave":   PostReplyFailBeforeSave,
		"DefaultBranchRead":         DefaultBranchRead,
		"DefaultBranchNotFound":     DefaultBranchNotFound,
		"DefaultBranchForbidden":    DefaultBranchForbidden,
		"DefaultBranchEmpty":        DefaultBranchEmpty,
	}

	for subject, build := range subjects(models.Developer) {
		for name, run := range cases {
			t.Run(subject+"/"+name, func(t *testing.T) {
				f := gitlabtest.NewFake(bot)
				f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())
				run(t, f, build(t, f))
			})
		}
	}
}

func PostReplyReturnsSavedNote(t *testing.T, _ *gitlabtest.Fake, p replyPort) {
	ctx := context.Background()
	body := "Done.\n<!-- krci-agent-turn s=s1 n=1 exit=0 last=5 -->"

	n, err := p.PostReply(ctx, key, body)
	require.NoError(t, err)
	assert.Positive(t, n.ID)
	assert.Equal(t, bot, n.Author)
	assert.Equal(t, body, n.Body)

	v, err := p.Load(ctx, key)
	require.NoError(t, err)
	require.Len(t, v.Notes, 1)
	assert.Equal(t, n.ID, v.Notes[0].ID)
	assert.Equal(t, bot, v.Notes[0].Author)
	assert.Equal(t, body, v.Notes[0].Body)
}

func TestContractTriggerRole(t *testing.T) {
	levels := map[string]int{"guest": 10, "rep": 20, "dev": 30, "lead": 40, "owner": 50}

	for _, tc := range []struct {
		trigger models.Role
		allowed []string
	}{
		{models.Developer, []string{"dev", "lead", "owner"}},
		{models.Maintainer, []string{"lead", "owner"}},
		{models.Owner, []string{"owner"}},
	} {
		for subject, build := range subjects(tc.trigger) {
			t.Run(subject+"/"+tc.trigger.String(), func(t *testing.T) {
				f := gitlabtest.NewFake(bot)
				f.AddIssue(key, "krci/test-go-app", models.IssueOpened, agentLabels.Trigger())

				for u, l := range levels {
					f.SetMember(12, u, l)
				}

				p := build(t, f)

				for u := range levels {
					ok, err := p.CanTrigger(context.Background(), 12, u)
					require.NoError(t, err)
					assert.Equal(t, slices.Contains(tc.allowed, u), ok, u)
				}
			})
		}
	}
}

func TestNewRESTRejectsNonTriggerRole(t *testing.T) {
	for _, r := range []models.Role{0, models.Guest, models.Reporter, 25, 60} {
		_, err := gitlab.NewREST("https://gitlab.example", token, "", bot, r)
		require.Error(t, err)
	}
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
	require.ErrorIs(t, p.CheckProject(context.Background(), 99), models.ErrNotFound)
}

func NoteMergeRequestSkipsForkMR(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddForkMergeRequest(12, 8, 40, "agent/issue-7", "opened")

	require.NoError(t, p.NoteMergeRequest(context.Background(), 12, "agent/issue-7", stopped))
	assert.Empty(t, f.MRNotes(12, 8), "an MR from a fork branch of the same name is not the agent's")
}

func RefreshMergesNewAndEditedNotes(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()

	for i := range 120 {
		f.AddNote(key, models.Note{Body: fmt.Sprintf("comment %d", i), Author: "owner"})
	}

	waiting := models.Record{State: models.Waiting}
	status := f.AddNote(key, models.Note{Body: models.StatusBody(waiting, models.StatusView{Session: "s1"}), Author: bot})
	edited := f.AddNote(key, models.Note{Body: "draft", Author: bot})

	prev, err := p.Load(ctx, key)
	require.NoError(t, err)

	f.AddNote(key, models.Note{Body: "one more", Author: "owner"})
	f.EditNote(key, edited, "done\n<!-- krci-agent-turn s=s2 n=1 exit=0 last=5 -->")
	f.EditNote(key, status, models.StatusBody(models.Record{State: models.Working}, models.StatusView{Session: "s2"}))

	got, err := p.Refresh(ctx, prev)
	require.NoError(t, err)

	want, err := p.Load(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, models.Working, got.Record.State)
	assert.Equal(t, "s2", got.Status.Session)

	m, ok := turn.NewHistory(got.Notes, bot, got.Status.Session).MarkerAfter(0)
	require.True(t, ok, "a marker added by an edit is read")
	assert.Equal(t, edited, m.NoteID)

	again, err := p.Refresh(ctx, got)
	require.NoError(t, err)
	assert.Equal(t, want, again)
}

func RefreshReadsIssueFields(t *testing.T, f *gitlabtest.Fake, p port) {
	prev, err := p.Load(context.Background(), key)
	require.NoError(t, err)

	f.SetIssue(key, models.IssueClosed, agentLabels.Trigger(), agentLabels.State(models.Working))

	got, err := p.Refresh(context.Background(), prev)
	require.NoError(t, err)
	assert.Equal(t, models.IssueClosed, got.Issue.IssueState)
	assert.Equal(t, []string{agentLabels.Trigger(), agentLabels.State(models.Working)}, got.Issue.Labels)
	assert.Equal(t, "krci/test-go-app", got.Issue.ProjectPath)
}

func RefreshReadsSameTimeNote(t *testing.T, f *gitlabtest.Fake, p port) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	f.AddNote(key, models.Note{Body: "first", Author: "owner", UpdatedAt: at})

	prev, err := p.Load(context.Background(), key)
	require.NoError(t, err)

	f.AddNote(key, models.Note{Body: "same millisecond", Author: "owner", UpdatedAt: at})

	got, err := p.Refresh(context.Background(), prev)
	require.NoError(t, err)
	require.Len(t, got.Notes, 2)
	assert.Equal(t, "same millisecond", got.Notes[1].Body)
}

func RefreshReadsAllNewPages(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddNote(key, models.Note{Body: "old", Author: "owner"})

	prev, err := p.Load(context.Background(), key)
	require.NoError(t, err)

	for i := range 250 {
		f.AddNote(key, models.Note{Body: fmt.Sprintf("comment %d", i), Author: "owner"})
	}

	got, err := p.Refresh(context.Background(), prev)
	require.NoError(t, err)
	require.Len(t, got.Notes, 251)

	for i := 1; i < len(got.Notes); i++ {
		assert.Less(t, got.Notes[i-1].ID, got.Notes[i].ID)
	}
}

func RefreshFromEmptyView(t *testing.T, f *gitlabtest.Fake, p port) {
	for i := range 130 {
		f.AddNote(key, models.Note{Body: fmt.Sprintf("comment %d", i), Author: "owner"})
	}

	got, err := p.Refresh(context.Background(), models.IssueView{Issue: models.Issue{Key: key}})
	require.NoError(t, err)

	want, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func RefreshKeepsDeletedNote(t *testing.T, f *gitlabtest.Fake, p port) {
	f.AddNote(key, models.Note{Body: "kept", Author: "owner"})
	gone := f.AddNote(key, models.Note{Body: "deleted later", Author: "owner"})

	prev, err := p.Load(context.Background(), key)
	require.NoError(t, err)

	f.DeleteNote(key, gone)

	got, err := p.Refresh(context.Background(), prev)
	require.NoError(t, err)
	assert.Len(t, got.Notes, 2, "Refresh does not see a delete")

	full, err := p.Load(context.Background(), key)
	require.NoError(t, err)
	assert.Len(t, full.Notes, 1)
}

func RefreshNotFound(t *testing.T, f *gitlabtest.Fake, p port) {
	prev, err := p.Load(context.Background(), key)
	require.NoError(t, err)

	f.DeleteIssue(key)

	_, err = p.Refresh(context.Background(), prev)
	require.ErrorIs(t, err, models.ErrNotFound)
}

func WriteKnownEditsStatusNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	require.NoError(t, p.Write(ctx, key, nil, models.Writes{Record: &models.Record{State: models.Working},
		Status: &models.StatusView{Actor: key.ActorName(), Session: "s1"}}))

	known, err := p.Load(ctx, key)
	require.NoError(t, err)

	busy := models.Record{State: models.Error, Reason: models.ReasonActorBusy}
	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Record: &busy, Label: models.Error}))

	v, err := p.Load(ctx, key)
	require.NoError(t, err)
	require.Len(t, v.Notes, 1)

	want := models.StatusView{State: models.Error, Actor: key.ActorName(), Session: "s1", Reason: models.ReasonActorBusy}
	assert.Equal(t, want, v.Status, "rows without a new view come from the known status note")
	assert.Equal(t, []string{"create-status", "edit-status", "label krci::agent::error"}, f.Ops())
}

func WriteKnownWithoutStatusNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()

	known, err := p.Load(ctx, key)
	require.NoError(t, err)

	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Record: &models.Record{State: models.Waiting}}))
	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Record: &models.Record{State: models.Working}}))

	assert.Equal(t, []string{"create-status", "edit-status"}, f.Ops(),
		"a status note made after the view was read is found")

	r, ok := f.Record(key)
	require.True(t, ok)
	assert.Equal(t, models.Working, r.State)
}

func WriteKnownStatusNoteDeleted(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	id := f.AddNote(key, models.Note{Author: bot, Body: models.StatusBody(models.Record{State: models.Waiting},
		models.StatusView{Session: "s1"})})

	known, err := p.Load(ctx, key)
	require.NoError(t, err)

	f.DeleteNote(key, id)

	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Record: &models.Record{State: models.Done}}))

	r, ok := f.Record(key)
	require.True(t, ok, "a new status note holds the record")
	assert.Equal(t, models.Done, r.State)
	assert.Len(t, f.Notes(key), 1)
}

func WriteKnownSkipsMarkerNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	refused := refusedNote()
	plain := models.NewNote{Body: models.NoRoomText}

	known, err := p.Load(ctx, key)
	require.NoError(t, err)
	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Notes: []models.NewNote{refused, plain}}))

	known, err = p.Load(ctx, key)
	require.NoError(t, err)
	require.NoError(t, p.Write(ctx, key, &known, models.Writes{Notes: []models.NewNote{refused, plain}}))

	assert.Len(t, f.Notes(key), 3, "the marker note once, the plain note twice")
}

func WriteRetryAfterSavedMarkerNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	refused := refusedNote()
	w := models.Writes{Notes: []models.NewNote{refused}}

	known, err := p.Load(ctx, key)
	require.NoError(t, err)

	f.FailAfterNote(errors.New("gateway timeout"))
	require.Error(t, p.Write(ctx, key, &known, w), "the note is saved, the write fails")
	require.NoError(t, p.Write(ctx, key, nil, w), "the retry reads the notes")

	assert.Len(t, f.Notes(key), 1, "the marker note once")
}

func WriteRetryAfterSavedStatusNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	w := models.Writes{Record: &models.Record{State: models.Waiting}}

	f.FailAfterNote(errors.New("gateway timeout"))
	require.Error(t, p.Write(ctx, key, nil, w), "the status note is saved, the write fails")
	require.NoError(t, p.Write(ctx, key, nil, w), "the retry edits the saved status note")
	require.NoError(t, p.Write(ctx, key, nil, w), "no fault is left for a later write")

	assert.Len(t, f.Notes(key), 1, "one status note")
}

func WriteSavedStatusNoteEndsWrite(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	refused := refusedNote()
	w := models.Writes{Record: &models.Record{State: models.Waiting}, Notes: []models.NewNote{refused}}

	f.FailAfterNote(errors.New("gateway timeout"))
	require.Error(t, p.Write(ctx, key, nil, w))
	assert.Len(t, f.Notes(key), 1, "the status note only; the write ends before the marker note")

	require.NoError(t, p.Write(ctx, key, nil, w))
	assert.Len(t, f.Notes(key), 2, "the status note and the marker note once")
}

func PostReplyNotFound(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	_, err := p.PostReply(context.Background(), models.IssueKey{ProjectID: 12, IID: 99}, "reply")
	require.ErrorIs(t, err, models.ErrNotFound)

	_, err = p.PostReply(context.Background(), models.IssueKey{ProjectID: 99, IID: 7}, "reply")
	require.ErrorIs(t, err, models.ErrNotFound)
	assert.Empty(t, f.Ops())
}

func PostReplyForbidden(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	f.SetForbidden(key.ProjectID)

	_, err := p.PostReply(context.Background(), key, "reply")
	require.ErrorIs(t, err, models.ErrForbidden)
	assert.NotContains(t, err.Error(), token)
	assert.Empty(t, f.Notes(key))
}

func PostReplyFailAfterSave(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	ctx := context.Background()

	f.FailAfterNote(errors.New("gateway timeout"))

	_, err := p.PostReply(ctx, key, "reply")
	require.Error(t, err)
	assert.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotErrorIs(t, err, models.ErrForbidden)

	v, err := p.Load(ctx, key)
	require.NoError(t, err)
	require.Len(t, v.Notes, 1, "the note is saved")
	assert.Equal(t, "reply", v.Notes[0].Body)

	_, err = p.PostReply(ctx, key, "again")
	require.NoError(t, err, "the fault is spent")
}

func PostReplyFailBeforeSave(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	ctx := context.Background()

	f.FailBeforeNote(errors.New("gateway timeout"))

	_, err := p.PostReply(ctx, key, "reply")
	require.Error(t, err)
	assert.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotErrorIs(t, err, models.ErrForbidden)

	v, err := p.Load(ctx, key)
	require.NoError(t, err)
	assert.Empty(t, v.Notes, "no note is saved")

	_, err = p.PostReply(ctx, key, "reply")
	require.NoError(t, err, "the fault is spent")

	v, err = p.Load(ctx, key)
	require.NoError(t, err)
	assert.Len(t, v.Notes, 1)
}

func WriteFailBeforeNote(t *testing.T, f *gitlabtest.Fake, p port) {
	ctx := context.Background()
	status := models.Writes{Record: &models.Record{State: models.Waiting}}
	plain := models.Writes{Notes: []models.NewNote{{Body: models.NoRoomText}}}

	for i, w := range []models.Writes{status, plain} {
		f.FailBeforeNote(errors.New("gateway timeout"))
		require.Error(t, p.Write(ctx, key, nil, w))
		assert.Len(t, f.Notes(key), i, "no note is saved")
		require.NoError(t, p.Write(ctx, key, nil, w), "the fault is spent")
		assert.Len(t, f.Notes(key), i+1)
	}
}

func DefaultBranchRead(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	f.SetDefaultBranch(key.ProjectID, "trunk")

	got, err := p.DefaultBranch(context.Background(), key.ProjectID)
	require.NoError(t, err)
	assert.Equal(t, "trunk", got)
}

func DefaultBranchNotFound(t *testing.T, _ *gitlabtest.Fake, p replyPort) {
	_, err := p.DefaultBranch(context.Background(), 99)
	require.ErrorIs(t, err, models.ErrNotFound)
}

func DefaultBranchForbidden(t *testing.T, f *gitlabtest.Fake, p replyPort) {
	f.SetDefaultBranch(key.ProjectID, "trunk")
	f.SetForbidden(key.ProjectID)

	_, err := p.DefaultBranch(context.Background(), key.ProjectID)
	require.ErrorIs(t, err, models.ErrForbidden)
	assert.NotContains(t, err.Error(), token)
}

func DefaultBranchEmpty(t *testing.T, _ *gitlabtest.Fake, p replyPort) {
	got, err := p.DefaultBranch(context.Background(), key.ProjectID)
	require.Error(t, err, "an empty repository has no default branch")
	assert.Empty(t, got)
	assert.NotErrorIs(t, err, models.ErrNotFound)
	assert.NotErrorIs(t, err, models.ErrForbidden)
}
