//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

type authoringFixture struct {
	testFixture
	authorings *postgres.Authorings
	jobs       *postgres.Jobs
	test       test.Test
	device     device.Device
}

// newAuthorings sets up a project with one Android test and one device on an online hub.
func newAuthorings(t *testing.T) authoringFixture {
	t.Helper()
	f := authoringFixture{testFixture: newTests(t)}
	f.authorings, f.jobs = postgres.NewAuthorings(f.db), postgres.NewJobs(f.db)
	f.test = f.add(t, "Sign in")
	ctx := context.Background()
	hubs := postgres.NewHubs(f.db)
	h, err := hubs.CreateHub(ctx, hub.Hub{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, Name: "Office Mac", CreatedBy: f.userID,
	}, []byte("token"))
	if err != nil {
		t.Fatalf("CreateHub: %v", err)
	}
	now := f.now.Now()
	err = hubs.ReplaceInventory(ctx, h.ID, hub.Inventory{Version: "1", Devices: []hub.ReportedDevice{
		reported("A1", project.PlatformAndroid),
	}}, hub.Presence{SeenAt: now, OnlineUntil: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("ReplaceInventory: %v", err)
	}
	devices, err := postgres.NewDevices(f.db).Devices(ctx, device.Query{ProjectID: f.projectID})
	if err != nil || len(devices) != 1 {
		t.Fatalf("Devices = %+v, %v", devices, err)
	}
	f.device = devices[0]
	return f
}

func (f authoringFixture) start(t *testing.T) authoring.Authoring {
	t.Helper()
	created, err := f.authorings.CreateAuthoring(context.Background(), f.plan())
	if err != nil {
		t.Fatalf("CreateAuthoring: %v", err)
	}
	return created
}

func (f authoringFixture) plan() authoring.NewAuthoring {
	return authoring.NewAuthoring{
		Authoring: authoring.Authoring{
			ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, TestID: f.test.ID,
			TestVersion: f.test.Version, Platform: project.PlatformAndroid, DeviceID: f.device.ID,
			DeviceName: f.device.Name, Status: authoring.StatusQueued, CreatedBy: f.userID,
		},
		HubID: f.device.HubID, Serial: f.device.Serial,
		Job: worker.EnqueueInput{Kind: authoring.JobKind, Payload: []byte(`{}`), MaxAttempts: 3},
	}
}

func TestAuthoringsLookups(t *testing.T) {
	f := newAuthorings(t)
	ctx := context.Background()

	if found, err := f.authorings.Test(ctx, f.projectID, f.test.ID); err != nil || found.StepCount != 2 {
		t.Fatalf("Test = %+v, %v", found, err)
	}
	if found, err := f.authorings.Device(ctx, f.projectID, f.device.ID); err != nil || found.Serial != "A1" {
		t.Fatalf("Device = %+v, %v", found, err)
	}
	if _, err := f.authorings.DefaultEnvironment(ctx, f.projectID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DefaultEnvironment error = %v", err)
	}
	for _, id := range []string{"not-a-uuid", uuid.NewString()} {
		if _, err := f.authorings.Environment(ctx, f.projectID, id); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("Environment(%s) error = %v", id, err)
		}
	}
}

func TestAuthoringsAllowOneActivePerTest(t *testing.T) {
	f := newAuthorings(t)
	ctx := context.Background()

	created := f.start(t)
	_, err := f.authorings.CreateAuthoring(ctx, f.plan())

	if created.Status != authoring.StatusQueued || created.DeviceName != "A1 phone" ||
		!created.CreatedAt.Equal(f.now.Now()) || len(created.Script) != 0 {
		t.Fatalf("created = %+v", created)
	}
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second CreateAuthoring error = %v", err)
	}
	if _, ok, err := f.jobs.Claim(ctx, worker.ClaimInput{
		Kind: authoring.JobKind, Owner: "test", Lease: time.Minute,
	}); err != nil || !ok {
		t.Fatalf("Claim = %v, %v", ok, err)
	}
}

func TestAuthoringsLifecycleKeepsScriptAndLog(t *testing.T) {
	f := newAuthorings(t)
	ctx := context.Background()
	created := f.start(t)

	work, err := f.authorings.Work(ctx, created.ID)
	if err != nil || work.Title != "Sign in" || len(work.Steps) != 2 || work.Steps[0].Target != "Sign in" ||
		work.HubID != f.device.HubID || work.Serial != "A1" {
		t.Fatalf("Work = %+v, %v", work, err)
	}
	if started, err := f.authorings.StartAuthoring(ctx, created.ID); err != nil || !started {
		t.Fatalf("StartAuthoring = %v, %v", started, err)
	}
	script := []ui.Op{
		{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/sign_in", Index: 1}},
		{Step: 2, Kind: ui.KindType, Text: "{{PHONE}}"},
		{Step: 2, Kind: ui.KindSwipe, Direction: ui.DirectionUp},
	}
	log := []authoring.Turn{{Step: 1, Action: "tap", Element: "[3]", Thought: "the button"}}
	finish := authoring.Finish{Status: authoring.StatusSucceeded, Script: script, Log: log}
	if err := f.authorings.FinishAuthoring(ctx, created.ID, finish); err != nil {
		t.Fatalf("FinishAuthoring: %v", err)
	}

	found, err := f.authorings.Authoring(ctx, f.projectID, created.ID)
	if err != nil || found.Status != authoring.StatusSucceeded || found.StartedAt == nil || found.FinishedAt == nil ||
		len(found.Script) != 3 || *found.Script[0].Selector != *script[0].Selector || found.Script[1] != script[1] ||
		found.Script[2].Direction != ui.DirectionUp || len(found.Log) != 1 || found.Log[0] != log[0] {
		t.Fatalf("Authoring = %+v, %v", found, err)
	}
	if err := f.authorings.FinishAuthoring(ctx, created.ID, finish); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second FinishAuthoring error = %v", err)
	}
	if started, err := f.authorings.StartAuthoring(ctx, created.ID); err != nil || started {
		t.Fatalf("StartAuthoring finished = %v, %v", started, err)
	}
	list, err := f.authorings.Authorings(ctx, f.projectID, f.test.ID)
	if err != nil || len(list) != 1 || list[0].ID != created.ID || list[0].Script != nil {
		t.Fatalf("Authorings = %+v, %v", list, err)
	}
	f.start(t)
}

func TestAuthoringsCancel(t *testing.T) {
	f := newAuthorings(t)
	ctx := context.Background()
	created := f.start(t)

	cancelled, err := f.authorings.CancelAuthoring(ctx, f.projectID, created.ID)
	if err != nil || cancelled.Status != authoring.StatusCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("CancelAuthoring = %+v, %v", cancelled, err)
	}
	if _, ok, err := f.jobs.Claim(ctx, worker.ClaimInput{
		Kind: authoring.JobKind, Owner: "test", Lease: time.Minute,
	}); err != nil || ok {
		t.Fatalf("Claim after cancel = %v, %v", ok, err)
	}
	if _, err := f.authorings.CancelAuthoring(ctx, f.projectID, created.ID); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second CancelAuthoring error = %v", err)
	}
	if _, err := f.authorings.CancelAuthoring(ctx, f.projectID, uuid.NewString()); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("CancelAuthoring missing error = %v", err)
	}
	if _, err := f.authorings.Work(ctx, created.ID); err != nil {
		t.Fatalf("Work of a cancelled authoring: %v", err)
	}
}

func TestAuthoringsAreScopedToTheirProject(t *testing.T) {
	f := newAuthorings(t)
	created := f.start(t)
	other := f.create(t, f.userID, "Other").ID

	if _, err := f.authorings.Authoring(context.Background(), other, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Authoring from another project error = %v", err)
	}
	list, err := f.authorings.Authorings(context.Background(), other, f.test.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("Authorings from another project = %+v, %v", list, err)
	}
}

func TestAuthoringsReviewKeepsLessonsOnce(t *testing.T) {
	f := newAuthorings(t)
	ctx := context.Background()
	created := f.start(t)
	review := worker.EnqueueInput{Kind: authoring.ReviewJobKind, Payload: []byte(`{}`), MaxAttempts: 3}
	finish := authoring.Finish{Status: authoring.StatusFailed, Log: []authoring.Turn{{Step: 1, Action: "wait"}}}
	if err := f.authorings.FinishAuthoring(ctx, created.ID, finish, review); err != nil {
		t.Fatalf("FinishAuthoring: %v", err)
	}
	if _, ok, err := f.jobs.Claim(ctx, worker.ClaimInput{
		Kind: authoring.ReviewJobKind, Owner: "test", Lease: time.Minute,
	}); err != nil || !ok {
		t.Fatalf("Claim review = %v, %v", ok, err)
	}
	notes := postgres.NewKnowledge(f.db)
	index := worker.EnqueueInput{Kind: knowledge.IndexJobKind, Payload: []byte(`{}`), MaxAttempts: 1}
	people, err := notes.CreateNote(ctx, knowledge.NewNote{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, Kind: knowledge.KindNote, Title: "Ours",
		Content: "Written by people", Source: knowledge.SourceUser, CreatedBy: f.userID,
	}, index)
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	lesson := func(id, title string) authoring.Lesson {
		return authoring.Lesson{
			NoteID: id, ProjectID: f.projectID, Kind: knowledge.KindGotcha, Title: title, Content: "Wait for the code.",
			CreatedBy: f.userID, Index: index,
		}
	}
	first := uuid.Must(uuid.NewV7()).String()

	if err := f.authorings.SaveLessons(ctx, created.ID, []authoring.Lesson{
		lesson(first, "SMS takes a while"), lesson(people.ID, "Overwrite"),
	}, []string{people.ID}); err != nil {
		t.Fatalf("SaveLessons: %v", err)
	}
	if err := f.authorings.SaveLessons(ctx, created.ID, nil, nil); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second SaveLessons error = %v", err)
	}

	agent, err := f.authorings.AgentNotes(ctx, f.projectID, 10)
	if err != nil || len(agent) != 1 || agent[0].ID != first || agent[0].Source != knowledge.SourceAgent ||
		agent[0].Indexed {
		t.Fatalf("AgentNotes = %+v, %v", agent, err)
	}
	if kept, err := notes.Note(ctx, f.projectID, people.ID); err != nil || kept.Title != "Ours" {
		t.Fatalf("people's note = %+v, %v", kept, err)
	}
	if found, err := f.authorings.Authoring(ctx, f.projectID, created.ID); err != nil || found.ReviewedAt == nil {
		t.Fatalf("Authoring = %+v, %v", found, err)
	}
	second := f.start(t)
	finish.Knowledge = []string{first, "not-a-uuid"}
	if err := f.authorings.FinishAuthoring(ctx, second.ID, finish); err != nil {
		t.Fatalf("FinishAuthoring: %v", err)
	}
	if work, err := f.authorings.Work(ctx, second.ID); err != nil || len(work.Authoring.Knowledge) != 1 ||
		work.Authoring.Knowledge[0] != first {
		t.Fatalf("Work knowledge = %+v, %v", work.Authoring.Knowledge, err)
	}
	if err := f.authorings.SaveLessons(ctx, second.ID, []authoring.Lesson{lesson(first, "SMS takes a minute")},
		nil); err != nil {
		t.Fatalf("SaveLessons rewrite: %v", err)
	}
	agent, err = f.authorings.AgentNotes(ctx, f.projectID, 10)
	if err != nil || len(agent) != 1 || agent[0].Title != "SMS takes a minute" {
		t.Fatalf("AgentNotes after rewrite = %+v, %v", agent, err)
	}
	third := f.start(t)
	if err := f.authorings.FinishAuthoring(ctx, third.ID, finish); err != nil {
		t.Fatalf("FinishAuthoring: %v", err)
	}
	if err := f.authorings.SaveLessons(ctx, third.ID, nil, []string{first}); err != nil {
		t.Fatalf("SaveLessons retire: %v", err)
	}
	if agent, err := f.authorings.AgentNotes(ctx, f.projectID, 10); err != nil || len(agent) != 0 {
		t.Fatalf("AgentNotes after retiring = %+v, %v", agent, err)
	}
	pending := 0
	for {
		_, ok, err := f.jobs.Claim(ctx, worker.ClaimInput{Kind: knowledge.IndexJobKind, Owner: "test", Lease: time.Minute})
		if err != nil {
			t.Fatalf("Claim index: %v", err)
		}
		if !ok {
			break
		}
		pending++
	}
	if pending != 3 {
		t.Fatalf("index jobs = %d, want people's note, the lesson and its rewrite", pending)
	}
}
