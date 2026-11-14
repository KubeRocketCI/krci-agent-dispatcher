// Package substratetest is the in-memory adapter of the Substrate port. It implements the port directly and
// serves the ateapi Control subset the gRPC adapter uses (Server), over the same state, so one contract
// suite runs against both adapters.
package substratetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
)

type actor struct {
	state ateapi.ActorState
	uid   string
	pod   string
}

// Fake is an in-memory Substrate atespace: templates, actors and egress policies. Safe for concurrent use.
type Fake struct {
	mu        sync.Mutex
	atespace  string
	templates map[string]bool
	actors    map[string]*actor
	policies  map[string]bool
	specs     map[string]models.ActorSpec
	sentTmpl  map[string]*ateapi.ActorTemplate
	sentPol   map[string]*ateapi.EgressPolicy
	ops       []string
	noRoom    bool
	nextUID   int
	failures  map[string][]error
	states    map[string][]ateapi.ActorState
	onResume  func(name string)
	onNoRoom  func(name string)
	onSuspend func(name string)
	authz     []string
}

// NewFake returns an empty atespace.
func NewFake(atespace string) *Fake {
	return &Fake{
		atespace:  atespace,
		templates: map[string]bool{},
		actors:    map[string]*actor{},
		policies:  map[string]bool{},
		specs:     map[string]models.ActorSpec{},
		sentTmpl:  map[string]*ateapi.ActorTemplate{},
		sentPol:   map[string]*ateapi.EgressPolicy{},
		failures:  map[string][]error{},
		states:    map[string][]ateapi.ActorState{},
	}
}

// AddPartial adds a template and, if withActor, a suspended actor, with no egress policy.
func (f *Fake) AddPartial(name string, withActor bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.templates[name] = true
	if withActor {
		f.addActorLocked(name)
	}
}

// AddComplete adds a template, a suspended actor and its egress policy.
func (f *Fake) AddComplete(name string, state ateapi.ActorState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.templates[name] = true
	f.addActorLocked(name)
	f.actors[name].state = state
	f.policies[name] = true
}

func (f *Fake) addActorLocked(name string) {
	f.nextUID++
	f.actors[name] = &actor{state: ateapi.ActorState_ACTOR_STATE_SUSPENDED, uid: fmt.Sprintf("uid-%d", f.nextUID)}
}

// SetState sets the actor state of an existing actor.
func (f *Fake) SetState(name string, s ateapi.ActorState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if a := f.actors[name]; a != nil {
		a.state = s
	}
}

// RemoveActor deletes an actor and its policy without an op, as if it vanished.
func (f *Fake) RemoveActor(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.actors, name)
	delete(f.policies, name)
}

// QueueStates makes the next Inspect calls of name report states, one per call; then the stored state.
func (f *Fake) QueueStates(name string, states ...ateapi.ActorState) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.states[name] = append(f.states[name], states...)
}

// SetNoRoom makes Resume fail with ErrNoRoom while on.
func (f *Fake) SetNoRoom(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.noRoom = on
}

// Fail makes the next calls of op (create, resume, suspend, revert, delete, inspect, list) return errs, one
// per call, before any change.
func (f *Fake) Fail(op string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failures[op] = append(f.failures[op], errs...)
}

// OnSuspend calls fn after each successful Suspend, outside the fake's lock.
func (f *Fake) OnSuspend(fn func(name string)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onSuspend = fn
}

// OnNoRoom calls fn after each Resume that fails with ErrNoRoom, outside the fake's lock.
func (f *Fake) OnNoRoom(fn func(name string)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onNoRoom = fn
}

// OnResume calls fn after each successful Resume, outside the fake's lock.
func (f *Fake) OnResume(fn func(name string)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.onResume = fn
}

// Ops returns the changes in order: create-template, create-actor, create-policy, resume, suspend, revert,
// delete-policy, delete-actor, delete-template; each followed by the actor name.
func (f *Fake) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.ops)
}

// Count returns how many times op was applied to name.
func (f *Fake) Count(op, name string) int {
	n := 0

	for _, o := range f.Ops() {
		if o == op+" "+name {
			n++
		}
	}

	return n
}

// ClearOps empties the op log.
func (f *Fake) ClearOps() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.ops = nil
}

// Spec returns the spec of the Create of name through the port that made its template; deleting the template clears
// it.
func (f *Fake) Spec(name string) (models.ActorSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s, ok := f.specs[name]

	return s, ok
}

// SentTemplate returns the actor template of name that the gRPC server stored in a create; nil when none or
// after the template is deleted.
func (f *Fake) SentTemplate(name string) *ateapi.ActorTemplate {
	f.mu.Lock()
	defer f.mu.Unlock()

	return proto.CloneOf(f.sentTmpl[name])
}

// SentEgressPolicy returns the last egress policy of the actor name the gRPC server received in a create; nil
// when none or after the policy is deleted.
func (f *Fake) SentEgressPolicy(name string) *ateapi.EgressPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()

	return proto.CloneOf(f.sentPol[name])
}

// Authorizations returns the authorization metadata the gRPC server received, in order.
func (f *Fake) Authorizations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.authz)
}

func (f *Fake) failLocked(op string) error {
	errs := f.failures[op]
	if len(errs) == 0 {
		return nil
	}

	f.failures[op] = errs[1:]

	return errs[0]
}

func (f *Fake) opLocked(op, name string) { f.ops = append(f.ops, op+" "+name) }

var errAlreadyExists = errors.New("already exists")

func notFound(kind, name string) error {
	return fmt.Errorf("%s %s: %w", kind, name, models.ErrActorNotFound)
}

// Inspect implements the Substrate port.
func (f *Fake) Inspect(_ context.Context, name string) (models.ActorView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failLocked("inspect"); err != nil {
		return models.ActorView{}, err
	}

	v := models.ActorView{Template: f.templates[name], EgressPolicy: f.policies[name], State: models.None}
	if v.Template {
		v.Session = f.specs[name].SessionID
	}

	a, st := f.observeLocked(name)
	if a == nil {
		return v, nil
	}

	v.Actor, v.State, v.UID, v.Pod = true, substrate.StateOf(st), a.uid, a.pod

	return v, nil
}

// observeLocked returns the actor and the state one observation reports: the next queued state, else the stored one.
func (f *Fake) observeLocked(name string) (*actor, ateapi.ActorState) {
	a := f.actors[name]
	if a == nil {
		return nil, ateapi.ActorState_ACTOR_STATE_UNSPECIFIED
	}

	st := a.state
	if q := f.states[name]; len(q) > 0 {
		st, f.states[name] = q[0], q[1:]
	}

	return a, st
}

// List implements the Substrate port. The actor states are the stored ones; queued observations are kept.
func (f *Fake) List(_ context.Context, prefix string) ([]models.ActorSummary, error) {
	listed, err := f.list(prefix)
	if err != nil {
		return nil, err
	}

	out := make([]models.ActorSummary, 0, len(listed))
	for _, a := range listed {
		out = append(out, models.ActorSummary{Name: a.name, State: substrate.StateOf(a.state)})
	}

	return out, nil
}

type listedActor struct {
	name  string
	state ateapi.ActorState
}

// list returns the actors whose names start with prefix, sorted by name.
func (f *Fake) list(prefix string) ([]listedActor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failLocked("list"); err != nil {
		return nil, err
	}

	var out []listedActor

	for n, a := range f.actors {
		if strings.HasPrefix(n, prefix) {
			out = append(out, listedActor{name: n, state: a.state})
		}
	}

	slices.SortFunc(out, func(a, b listedActor) int { return strings.Compare(a.name, b.name) })

	return out, nil
}

// Create implements the Substrate port. A spec that fails models.ActorSpec.Validate returns models.ErrInvalidSpec
// before any change.
func (f *Fake) Create(_ context.Context, name string, spec models.ActorSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failLocked("create"); err != nil {
		return err
	}

	if err := spec.Validate(); err != nil {
		return fmt.Errorf("create %s: %w: %w", name, models.ErrInvalidSpec, err)
	}

	switch err := f.createTemplateLocked(name); {
	case err == nil:
		f.specs[name] = spec
	case !errors.Is(err, errAlreadyExists):
		return err
	}

	for _, step := range []func(string) error{f.createActorLocked, f.createPolicyLocked} {
		if err := step(name); err != nil && !errors.Is(err, errAlreadyExists) {
			return err
		}
	}

	return nil
}

// createTemplateLocked, createActorLocked and createPolicyLocked return errAlreadyExists for an existing part;
// Fail("create-template"|"create-actor"|"create-policy") fails the step before any change.
func (f *Fake) createTemplateLocked(name string) error {
	if err := f.failLocked("create-template"); err != nil {
		return err
	}

	if f.templates[name] {
		return fmt.Errorf("template %s: %w", name, errAlreadyExists)
	}

	f.templates[name] = true
	f.opLocked("create-template", name)

	return nil
}

func (f *Fake) createActorLocked(name string) error {
	if !f.templates[name] {
		return notFound("template", name)
	}

	if err := f.failLocked("create-actor"); err != nil {
		return err
	}

	if f.actors[name] != nil {
		return fmt.Errorf("actor %s: %w", name, errAlreadyExists)
	}

	f.addActorLocked(name)
	f.opLocked("create-actor", name)

	return nil
}

func (f *Fake) createPolicyLocked(name string) error {
	if err := f.failLocked("create-policy"); err != nil {
		return err
	}

	if f.actors[name] == nil {
		return notFound("actor", name)
	}

	if f.policies[name] {
		return fmt.Errorf("egress policy %s: %w", name, errAlreadyExists)
	}

	f.policies[name] = true
	f.opLocked("create-policy", name)

	return nil
}

// Resume implements the Substrate port.
func (f *Fake) Resume(_ context.Context, name string) error {
	f.mu.Lock()

	err := f.resumeLocked(name)
	fn, noRoom := f.onResume, f.onNoRoom
	f.mu.Unlock()

	if errors.Is(err, models.ErrNoRoom) && noRoom != nil {
		noRoom(name)
	}

	if err == nil && fn != nil {
		fn(name)
	}

	return err
}

func (f *Fake) resumeLocked(name string) error {
	if err := f.failLocked("resume"); err != nil {
		return err
	}

	a := f.actors[name]
	if a == nil {
		return notFound("actor", name)
	}

	if f.noRoom {
		return fmt.Errorf("resume %s: %w", name, models.ErrNoRoom)
	}

	a.state, a.pod = ateapi.ActorState_ACTOR_STATE_RUNNING, "workers-pod-0"

	f.opLocked("resume", name)

	return nil
}

// Suspend implements the Substrate port.
func (f *Fake) Suspend(_ context.Context, name string) error {
	f.mu.Lock()

	err := f.settleLocked("suspend", name)
	fn := f.onSuspend
	f.mu.Unlock()

	if err == nil && fn != nil {
		fn(name)
	}

	return err
}

// Revert implements the Substrate port.
func (f *Fake) Revert(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.settleLocked("revert", name)
}

func (f *Fake) settleLocked(op, name string) error {
	if err := f.failLocked(op); err != nil {
		return err
	}

	a := f.actors[name]
	if a == nil {
		return notFound("actor", name)
	}

	a.state, a.pod = ateapi.ActorState_ACTOR_STATE_SUSPENDED, ""

	f.opLocked(op, name)

	return nil
}

// Delete implements the Substrate port.
func (f *Fake) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.failLocked("delete"); err != nil {
		return err
	}

	f.deletePolicyLocked(name)
	f.deleteActorLocked(name)
	f.deleteTemplateLocked(name)

	return nil
}

func (f *Fake) deletePolicyLocked(name string) bool {
	if !f.policies[name] {
		return false
	}

	delete(f.policies, name)
	delete(f.sentPol, name)
	f.opLocked("delete-policy", name)

	return true
}

func (f *Fake) deleteActorLocked(name string) bool {
	if f.actors[name] == nil {
		return false
	}

	delete(f.actors, name)
	f.opLocked("delete-actor", name)

	return true
}

func (f *Fake) deleteTemplateLocked(name string) bool {
	if !f.templates[name] {
		return false
	}

	delete(f.templates, name)
	delete(f.specs, name)
	delete(f.sentTmpl, name)
	f.opLocked("delete-template", name)

	return true
}

// Server returns an ateapi Control server over the fake's state.
func (f *Fake) Server() ateapi.ControlServer { return &server{f: f} }

type server struct {
	ateapi.UnimplementedControlServer

	f *Fake
}

// enter records the authorization metadata and checks the atespace of the request.
func (s *server) enter(ctx context.Context, ref *ateapi.ObjectRef) error {
	md, _ := metadata.FromIncomingContext(ctx)

	s.f.mu.Lock()
	s.f.authz = append(s.f.authz, md.Get("authorization")...)
	s.f.mu.Unlock()

	if ref != nil && ref.GetAtespace() != s.f.atespace {
		return status.Errorf(codes.PermissionDenied, "atespace %q", ref.GetAtespace())
	}

	return nil
}

func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, models.ErrActorNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, models.ErrNoRoom):
		return status.Error(codes.ResourceExhausted, "no worker has room for the actor")
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *server) GetActorTemplate(
	ctx context.Context, req *ateapi.GetActorTemplateRequest,
) (*ateapi.ActorTemplate, error) {
	if err := s.enter(ctx, req.GetActorTemplate()); err != nil {
		return nil, err
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if !s.f.templates[req.GetActorTemplate().GetName()] {
		return nil, status.Error(codes.NotFound, "actor template not found")
	}

	name := req.GetActorTemplate().GetName()
	if sent := s.f.sentTmpl[name]; sent != nil {
		return proto.CloneOf(sent), nil
	}

	return &ateapi.ActorTemplate{Metadata: &ateapi.ResourceMetadata{Atespace: s.f.atespace, Name: name}}, nil
}

func (s *server) GetActor(ctx context.Context, req *ateapi.GetActorRequest) (*ateapi.Actor, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	a, st := s.f.observeLocked(req.GetActor().GetName())
	if a == nil {
		return nil, status.Error(codes.NotFound, "actor not found")
	}

	return &ateapi.Actor{
		Metadata: &ateapi.ResourceMetadata{Atespace: s.f.atespace, Name: req.GetActor().GetName(), Uid: a.uid},
		Status:   &ateapi.ActorStatus{State: st, WorkerAssignment: &ateapi.WorkerAssignment{WorkerPod: a.pod}},
	}, nil
}

func (s *server) GetActorEgressPolicy(
	ctx context.Context, req *ateapi.GetActorEgressPolicyRequest,
) (*ateapi.EgressPolicy, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if !s.f.policies[req.GetActor().GetName()] {
		return nil, status.Error(codes.NotFound, "egress policy not found")
	}

	return &ateapi.EgressPolicy{Metadata: &ateapi.ResourceMetadata{Atespace: s.f.atespace, Name: req.GetName()}}, nil
}

func (s *server) ListActors(ctx context.Context, req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
	if err := s.enter(ctx, &ateapi.ObjectRef{Atespace: req.GetAtespace()}); err != nil {
		return nil, err
	}

	listed, err := s.f.list("")
	if err != nil {
		return nil, toStatus(err)
	}

	const pageSize = 2

	start := 0
	if tok := req.GetOptions().GetPageToken(); tok != "" {
		start = slices.IndexFunc(listed, func(a listedActor) bool { return a.name == tok })
		if start < 0 {
			return nil, status.Error(codes.InvalidArgument, "bad page token")
		}
	}

	end := min(start+pageSize, len(listed))
	resp := &ateapi.ListActorsResponse{}

	for _, a := range listed[start:end] {
		resp.Actors = append(resp.Actors, &ateapi.Actor{
			Metadata: &ateapi.ResourceMetadata{Atespace: s.f.atespace, Name: a.name},
			Status:   &ateapi.ActorStatus{State: a.state},
		})
	}

	if end < len(listed) {
		resp.NextPageToken = listed[end].name
	}

	return resp, nil
}

func (s *server) CreateActorTemplate(
	ctx context.Context, req *ateapi.CreateActorTemplateRequest,
) (*ateapi.ActorTemplate, error) {
	md := req.GetActorTemplate().GetMetadata()
	if err := s.enter(ctx, &ateapi.ObjectRef{Atespace: md.GetAtespace(), Name: md.GetName()}); err != nil {
		return nil, err
	}

	if len(req.GetActorTemplate().GetContainers()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one container")
	}

	return req.GetActorTemplate(), s.locked(func() error {
		if err := s.f.createTemplateLocked(md.GetName()); err != nil {
			return err
		}

		s.f.sentTmpl[md.GetName()] = proto.CloneOf(req.GetActorTemplate())

		return nil
	})
}

func (s *server) CreateActor(ctx context.Context, req *ateapi.CreateActorRequest) (*ateapi.Actor, error) {
	md := req.GetActor().GetMetadata()
	if err := s.enter(ctx, &ateapi.ObjectRef{Atespace: md.GetAtespace(), Name: md.GetName()}); err != nil {
		return nil, err
	}

	if req.GetActor().GetActorTemplate().GetName() != md.GetName() {
		return nil, status.Error(codes.InvalidArgument, "actor template ref differs from the actor name")
	}

	return req.GetActor(), s.locked(func() error { return s.f.createActorLocked(md.GetName()) })
}

func (s *server) CreateActorEgressPolicy(
	ctx context.Context, req *ateapi.CreateActorEgressPolicyRequest,
) (*ateapi.EgressPolicy, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	pol := req.GetEgressPolicy()
	md := pol.GetMetadata()

	if md.GetName() != "default" || md.GetAtespace() != s.f.atespace || len(pol.GetRules()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "egress policy must be named default, in the atespace, with rules")
	}

	return pol, s.locked(func() error {
		s.f.sentPol[req.GetActor().GetName()] = proto.CloneOf(pol)

		return s.f.createPolicyLocked(req.GetActor().GetName())
	})
}

// locked runs fn under the fake's lock and returns its error as a gRPC status.
func (s *server) locked(fn func() error) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	return toStatus(fn())
}

// deleted runs a delete step under the lock; NotFound when nothing was there.
func (s *server) deleted(step func() bool) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if !step() {
		return status.Error(codes.NotFound, "not found")
	}

	return nil
}

func (s *server) ResumeActor(ctx context.Context, req *ateapi.ResumeActorRequest) (*ateapi.ResumeActorResponse, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	if err := s.f.Resume(ctx, req.GetActor().GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &ateapi.ResumeActorResponse{}, nil
}

func (s *server) SuspendActor(
	ctx context.Context, req *ateapi.SuspendActorRequest,
) (*ateapi.SuspendActorResponse, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	if err := s.f.Suspend(ctx, req.GetActor().GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &ateapi.SuspendActorResponse{}, nil
}

func (s *server) RevertActor(ctx context.Context, req *ateapi.RevertActorRequest) (*ateapi.RevertActorResponse, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	if err := s.f.Revert(ctx, req.GetActor().GetName()); err != nil {
		return nil, toStatus(err)
	}

	return &ateapi.RevertActorResponse{}, nil
}

func (s *server) DeleteActorEgressPolicy(
	ctx context.Context, req *ateapi.DeleteActorEgressPolicyRequest,
) (*ateapi.EgressPolicy, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	n := req.GetActor().GetName()

	return &ateapi.EgressPolicy{}, s.deleted(func() bool { return s.f.deletePolicyLocked(n) })
}

func (s *server) DeleteActor(ctx context.Context, req *ateapi.DeleteActorRequest) (*ateapi.Actor, error) {
	if err := s.enter(ctx, req.GetActor()); err != nil {
		return nil, err
	}

	n := req.GetActor().GetName()

	return &ateapi.Actor{}, s.deleted(func() bool { return s.f.deleteActorLocked(n) })
}

func (s *server) DeleteActorTemplate(
	ctx context.Context, req *ateapi.DeleteActorTemplateRequest,
) (*ateapi.ActorTemplate, error) {
	if err := s.enter(ctx, req.GetActorTemplate()); err != nil {
		return nil, err
	}

	n := req.GetActorTemplate().GetName()

	return &ateapi.ActorTemplate{}, s.deleted(func() bool { return s.f.deleteTemplateLocked(n) })
}
