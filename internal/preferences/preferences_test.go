package preferences

import (
	"testing"
	"time"
)

func TestPreferenceScopeAndTemporaryOverrides(t *testing.T) {
	start := time.Date(2030, time.January, 3, 14, 0, 0, 0, time.UTC)
	base := Record{
		ID: "user-default", Owner: "alice", Scope: OwnerScope, Name: "updates",
		Value: "digest", Original: "Put updates in a digest", Source: "chat:1", EffectiveAt: start,
	}
	project := base
	project.ID, project.Scope, project.Project, project.Value = "project-default", ProjectScope, "research", "immediate"
	project.Original, project.Source = "Tell me about these updates immediately", "chat:2"

	temporary := project
	expiry := start.Add(48 * time.Hour)
	temporary.ID, temporary.Value = "project-temporary", "silent"
	temporary.Original, temporary.Source = "Keep this project's updates silent until Saturday", "chat:3"
	temporary.ExpiresAt = &expiry
	temporary.EffectiveAt = start.Add(time.Hour)

	next := project
	next.ID, next.Value, next.Original = "project-revised", "digest", "Change the project's usual setting to digest"
	next.Source, next.EffectiveAt = "chat:4", start.Add(24*time.Hour)

	task := project
	task.ID, task.Scope, task.Task, task.Value = "task", TaskScope, "review", "immediate"
	task.Original, task.Source = "Notify me of revisions during this review", "chat:5"

	input := []Record{next, temporary, task, base, project}
	for _, pref := range input {
		if err := pref.Validate(); err != nil {
			t.Fatalf("invalid fixture %+v: %v", pref, err)
		}
	}
	ctx := Context{Owner: "alice", Project: "research"}
	assertResolved(t, input, ctx, start, project)
	assertResolved(t, input, ctx, start.Add(25*time.Hour), temporary)
	assertResolved(t, input, ctx, expiry, next)
	assertResolved(t, input, Context{Owner: "alice", Project: "different", Task: "review"}, expiry, base)
	assertResolved(t, input, Context{Owner: "alice", Project: "research", Task: "review"}, start.Add(25*time.Hour), task)
	if got, ok := Resolve(input, Context{Owner: "bob", Project: ctx.Project}, "updates", expiry); ok {
		t.Fatalf("cross-owner preference leak: %+v", got)
	}
}

func TestPreferenceRevocationAndReplay(t *testing.T) {
	start := time.Date(2030, time.January, 3, 14, 0, 0, 0, time.UTC)
	original := Record{
		ID: "original", Owner: "alice", Scope: OwnerScope, Name: "format",
		Value: "table", Original: "Always use tables", Source: "chat:1", EffectiveAt: start,
	}
	change := original
	change.ID, change.Value, change.Source, change.Original = "change", "summary", "chat:2", "Use a short summary instead"
	change.EffectiveAt = start.Add(time.Minute)
	ctx := Context{Owner: "alice"}

	revokedAt := start.Add(time.Hour)
	revoked, err := Revoke(change, revokedAt)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Revoke(revoked, revokedAt.Add(time.Hour))
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(revokedAt) || change.RevokedAt != nil {
		t.Fatalf("revocation is not idempotent: before=%+v after=%+v repeated=%+v err=%v", change, revoked, again, err)
	}
	input := []Record{again, original}
	assertResolved(t, input, ctx, revokedAt.Add(-time.Nanosecond), change)
	assertResolved(t, input, ctx, revokedAt, original)
}

func assertResolved(t *testing.T, records []Record, ctx Context, at time.Time, want Record) {
	t.Helper()
	got, ok := Resolve(records, ctx, want.Name, at)
	if !ok || got.ID != want.ID || got.Value != want.Value || got.Original != want.Original || got.Source != want.Source {
		t.Fatalf("effective preference at %s for %+v is %+v; want %+v", at, ctx, got, want)
	}
}
