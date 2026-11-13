// Package substrate is the Agent Substrate gRPC adapter of the dispatcher's Substrate port.
package substrate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

const (
	egressPolicyName = "default"
	callTimeout      = 30 * time.Second
	listPageSize     = 100
)

var actorStates = map[ateapi.ActorState]models.ActorState{
	ateapi.ActorState_ACTOR_STATE_RUNNING:    models.Running,
	ateapi.ActorState_ACTOR_STATE_SUSPENDED:  models.Suspended,
	ateapi.ActorState_ACTOR_STATE_CRASHED:    models.Crashed,
	ateapi.ActorState_ACTOR_STATE_RESUMING:   models.Resuming,
	ateapi.ActorState_ACTOR_STATE_SUSPENDING: models.Suspending,
	ateapi.ActorState_ACTOR_STATE_REVERTING:  models.Reverting,
	ateapi.ActorState_ACTOR_STATE_DELETING:   models.Deleting,
}

// StateOf maps a Substrate actor state to the dispatcher's view; an unspecified or unlisted state is Unspecified.
func StateOf(s ateapi.ActorState) models.ActorState {
	if v, ok := actorStates[s]; ok {
		return v
	}

	return models.Unspecified
}

// Deployment holds the settings shared by every issue actor.
type Deployment = templates.Deployment

// GRPC calls the ateapi Control service of one atespace.
type GRPC struct {
	conn   *grpc.ClientConn
	client ateapi.ControlClient
	deploy Deployment
}

// tokenFile sends the bearer token read from a projected ServiceAccount token file on each call.
type tokenFile string

func (f tokenFile) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	b, err := os.ReadFile(string(f))
	if err != nil {
		return nil, fmt.Errorf("read Substrate API token: %w", err)
	}

	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return nil, errors.New("substrate API token file is empty")
	}

	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (tokenFile) RequireTransportSecurity() bool { return true }

// NewGRPC returns an adapter for addr (host:port, optionally with a gRPC scheme) that creates actors in
// d.Atespace from d. The server certificate is verified against caFile for the host of addr. The token file is
// read on each call. d.GitLabURL must be set.
func NewGRPC(addr, tokenPath, caFile string, d Deployment, opts ...grpc.DialOption) (*GRPC, error) {
	if d.GitLabURL.String() == "" {
		return nil, errors.New("gitlab url is empty")
	}

	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read Substrate API CA file: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificate in %s", caFile)
	}

	target := addr
	if i := strings.Index(target, ":///"); i >= 0 {
		target = target[i+4:]
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("substrate API address %q: %w", addr, err)
	}

	creds := credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12})
	opts = append([]grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(tokenFile(tokenPath)),
	}, opts...)

	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("substrate API client: %w", err)
	}

	return &GRPC{conn: conn, client: ateapi.NewControlClient(conn), deploy: d}, nil
}

// Close closes the connection.
func (g *GRPC) Close() error { return g.conn.Close() }

func (g *GRPC) ref(name string) *ateapi.ObjectRef {
	return &ateapi.ObjectRef{Atespace: g.deploy.Atespace, Name: name}
}

// callCtx bounds one port call, every RPC of it, by callTimeout; an earlier deadline of ctx stays.
func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, callTimeout)
}

// mapErr wraps NotFound as models.ErrActorNotFound and ResourceExhausted as models.ErrNoRoom.
func mapErr(op, name string, err error) error {
	if err == nil {
		return nil
	}

	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%s %s: %w: %s", op, name, models.ErrActorNotFound, status.Convert(err).Message())
	case codes.ResourceExhausted:
		return fmt.Errorf("%s %s: %w", op, name, models.ErrNoRoom)
	default:
		return fmt.Errorf("%s %s: %w", op, name, err)
	}
}

func isNotFound(err error) bool { return status.Code(err) == codes.NotFound }

// created treats AlreadyExists as success: a retry after a cut create reuses the parts made before.
func created(err error) error {
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}

	return err
}

// Inspect reports which parts of the actor exist and its state.
func (g *GRPC) Inspect(ctx context.Context, name string) (models.ActorView, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	v := models.ActorView{State: models.None}

	_, err := g.client.GetActorTemplate(ctx, &ateapi.GetActorTemplateRequest{ActorTemplate: g.ref(name)})

	switch {
	case err == nil:
		v.Template = true
	case !isNotFound(err):
		return v, mapErr("get actor template", name, err)
	}

	a, err := g.client.GetActor(ctx, &ateapi.GetActorRequest{Actor: g.ref(name)})

	switch {
	case isNotFound(err):
		return v, nil
	case err != nil:
		return v, mapErr("get actor", name, err)
	}

	v.Actor = true
	v.State = StateOf(a.GetStatus().GetState())
	v.UID = a.GetMetadata().GetUid()
	v.Pod = a.GetStatus().GetWorkerAssignment().GetWorkerPod()

	polReq := &ateapi.GetActorEgressPolicyRequest{Actor: g.ref(name), Name: egressPolicyName}
	_, err = g.client.GetActorEgressPolicy(ctx, polReq)

	switch {
	case err == nil:
		v.EgressPolicy = true
	case !isNotFound(err):
		return v, mapErr("get egress policy", name, err)
	}

	return v, nil
}

// List returns the atespace's actors whose names start with prefix, with their actor states.
func (g *GRPC) List(ctx context.Context, prefix string) ([]models.ActorSummary, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	var (
		out   []models.ActorSummary
		token string
	)

	for {
		resp, err := g.client.ListActors(ctx, &ateapi.ListActorsRequest{
			Atespace: g.deploy.Atespace,
			Options:  &ateapi.ListOptions{PageSize: listPageSize, PageToken: token},
		})
		if err != nil {
			return nil, mapErr("list actors", g.deploy.Atespace, err)
		}

		for _, a := range resp.GetActors() {
			if n := a.GetMetadata().GetName(); strings.HasPrefix(n, prefix) {
				out = append(out, models.ActorSummary{Name: n, State: StateOf(a.GetStatus().GetState())})
			}
		}

		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// Create renders the actor template and egress policy of spec with the deployment settings, then creates the
// template, the actor and the egress policy, all named name. Parts that exist are kept. A spec that does not
// render returns models.ErrInvalidSpec before any call.
func (g *GRPC) Create(ctx context.Context, name string, spec models.ActorSpec) error {
	tmpl, pol, err := templates.Render(templates.Params{Deployment: g.deploy, ActorSpec: spec})
	if err != nil {
		return fmt.Errorf("render actor %s: %w: %w", name, models.ErrInvalidSpec, err)
	}

	ctx, cancel := callCtx(ctx)
	defer cancel()

	tmpl.Metadata = &ateapi.ResourceMetadata{Atespace: g.deploy.Atespace, Name: name}

	_, err = g.client.CreateActorTemplate(ctx, &ateapi.CreateActorTemplateRequest{ActorTemplate: tmpl})
	if created(err) != nil {
		return mapErr("create actor template", name, err)
	}

	actor := &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: g.deploy.Atespace, Name: name},
		ActorTemplate: g.ref(name),
	}
	if _, err = g.client.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: actor}); created(err) != nil {
		return mapErr("create actor", name, err)
	}

	pol.Metadata = &ateapi.ResourceMetadata{Atespace: g.deploy.Atespace, Name: egressPolicyName}

	polReq := &ateapi.CreateActorEgressPolicyRequest{Actor: g.ref(name), EgressPolicy: pol}
	_, err = g.client.CreateActorEgressPolicy(ctx, polReq)

	return mapErr("create egress policy", name, created(err))
}

// Resume resumes a suspended actor; models.ErrNoRoom when no worker has room.
func (g *GRPC) Resume(ctx context.Context, name string) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	_, err := g.client.ResumeActor(ctx, &ateapi.ResumeActorRequest{Actor: g.ref(name)})

	return mapErr("resume actor", name, err)
}

// Suspend suspends a running actor.
func (g *GRPC) Suspend(ctx context.Context, name string) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	_, err := g.client.SuspendActor(ctx, &ateapi.SuspendActorRequest{Actor: g.ref(name)})

	return mapErr("suspend actor", name, err)
}

// Revert returns the actor to its durable snapshot, suspended.
func (g *GRPC) Revert(ctx context.Context, name string) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	_, err := g.client.RevertActor(ctx, &ateapi.RevertActorRequest{Actor: g.ref(name)})

	return mapErr("revert actor", name, err)
}

// Delete deletes the egress policy, the actor and the template, each only if present. It also clears a
// partial create. An actor is deleted in any actor state: Substrate ends the workload of a running one and
// frees its worker. Snapshots are deleted with the actor.
func (g *GRPC) Delete(ctx context.Context, name string) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	polReq := &ateapi.DeleteActorEgressPolicyRequest{Actor: g.ref(name), Name: egressPolicyName}

	_, err := g.client.DeleteActorEgressPolicy(ctx, polReq)
	if err != nil && !isNotFound(err) {
		return mapErr("delete egress policy", name, err)
	}

	_, err = g.client.DeleteActor(ctx, &ateapi.DeleteActorRequest{Actor: g.ref(name)})
	if err != nil && !isNotFound(err) {
		return mapErr("delete actor", name, err)
	}

	_, err = g.client.DeleteActorTemplate(ctx, &ateapi.DeleteActorTemplateRequest{ActorTemplate: g.ref(name)})
	if err != nil && !isNotFound(err) {
		return mapErr("delete actor template", name, err)
	}

	return nil
}
