package tasks

import "testing"

func TestReferencesByIDOrRefTheProjectFirstAmbiguitySaidRelationsBlockedLoops(t *testing.T) {
	a := task(func(t *Task) { t.ID, t.Ref, t.Project = "t-1", "TASK-430", "work/x" })
	b := task(func(t *Task) {
		t.ID, t.Ref, t.Project, t.BlockedBy, t.Parent = "t-2", "TASK-495", "work/x", []string{"t-1"}, "t-4"
	})
	c := task(func(t *Task) { t.ID, t.Ref, t.Project = "t-3", "TASK-495", "work/y" })
	d := task(func(t *Task) { t.ID, t.Ref, t.Project = "t-4", "TASK-500", "work/x" })
	all := []Task{a, b, c, d}
	for _, k := range []struct{ key, project, want string }{
		{"#t-3", "", "t-3"}, {"task-430", "", "t-1"}, {"TASK-495", "work/y", "t-3"},
	} {
		got, err := ResolveTask(all, k.key, k.project)
		if err != nil || got.ID != k.want {
			t.Errorf("%v: %s, %v", k, got.ID, err)
		}
	}
	_, err := ResolveTask(all, "TASK-495", "")
	wantErr(t, err, "2 tasks")
	_, err = ResolveTask(all, "TASK-9", "")
	wantErr(t, err, "no task")
	if got := RefTaken(all, "task-495", "work/x", ""); got == nil || got.ID != "t-2" {
		t.Errorf("%v", got)
	}
	if RefTaken(all, "TASK-495", "work/x", "t-2") != nil {
		t.Error("a task does not take its own ref")
	}
	if !IsBlocked(all, b) {
		t.Error("b waits for a")
	}
	r := Relations(all, d)
	if len(r.Parts) != 1 || r.Parts[0].ID != "t-2" || r.PartsDone != 0 {
		t.Errorf("%+v", r)
	}
	if r := Relations(all, a); len(r.Blocking) != 1 || r.Blocking[0].ID != "t-2" {
		t.Errorf("%+v", r)
	}
	closed := a
	closed.Status = Done
	if IsBlocked([]Task{closed, b, c, d}, b) {
		t.Error("a done blocker releases")
	}
	if !WouldLoop(all, "t-4", "t-2") || WouldLoop(all, "t-2", "t-4") {
		t.Error("loops")
	}
}

func TestResolveRefsChecksTheRefTakenAndTheParentLoop(t *testing.T) {
	a := task(func(t *Task) { t.ID, t.Ref, t.Project = "t-1", "TASK-1", "p" })
	b := task(func(t *Task) { t.ID, t.Parent = "t-2", "t-3" })
	c := task(func(t *Task) { t.ID = "t-3" })
	all := []Task{a, b, c}
	_, err := ResolveRefs(all, Input{Ref: S("task-1"), Project: S("p")}, nil)
	wantErr(t, err, "is already t-1")
	out, err := ResolveRefs(all, Input{BlockedBy: L("TASK-1")}, &c)
	if err != nil || (*out.BlockedBy)[0] != "t-1" {
		t.Errorf("%v %v", out.BlockedBy, err)
	}
	_, err = ResolveRefs(all, Input{Parent: S("t-2")}, &c)
	wantErr(t, err, "circle")
	_, err = ResolveRefs(all, Input{PartOf: L("[[t-2]]")}, &c)
	wantErr(t, err, "circle")
}
