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
	"google.golang.org/protobuf/proto"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

var (
	// ErrNotFound is returned when the actor does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNoRoom is returned by Resume when no worker has room for the actor.
	ErrNoRoom = errors.New("no worker has room for the actor")
)

const (
	egressPolicyName = "default"
	callTimeout      = 30 * time.Second
	listPageSize     = 100
)

// StateOf maps a Substrate actor state to the dispatcher's view.
func StateOf(s ateapi.ActorState) models.ActorState {
	switch s {
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		return models.Running
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		return models.Suspended
	case ateapi.ActorState_ACTOR_STATE_CRASHED:
		return models.Crashed
	default:
		return models.Transitional
	}
}

// GRPC calls the ateapi Control service of one atespace.
type GRPC struct {
	conn     *grpc.ClientConn
	client   ateapi.ControlClient
	atespace string
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

// NewGRPC returns an adapter for addr (host:port, optionally with a gRPC scheme). The server certificate is
// verified against caFile for the host of addr. The token file is read on each call.
func NewGRPC(addr, tokenPath, caFile, atespace string, opts ...grpc.DialOption) (*GRPC, error) {
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

	return &GRPC{conn: conn, client: ateapi.NewControlClient(conn), atespace: atespace}, nil
}

// Close closes the connection.
func (g *GRPC) Close() error { return g.conn.Close() }

func (g *GRPC) ref(name string) *ateapi.ObjectRef {
	return &ateapi.ObjectRef{Atespace: g.atespace, Name: name}
}

func callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, callTimeout)
}

// mapErr wraps NotFound as ErrNotFound and ResourceExhausted as ErrNoRoom.
func mapErr(op, name string, err error) error {
	if err == nil {
		return nil
	}

	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("%s %s: %w: %s", op, name, ErrNotFound, status.Convert(err).Message())
	case codes.ResourceExhausted:
		return fmt.Errorf("%s %s: %w", op, name, ErrNoRoom)
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

// List returns the names of the atespace's actors that start with prefix.
func (g *GRPC) List(ctx context.Context, prefix string) ([]string, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	var (
		out   []string
		token string
	)

	for {
		resp, err := g.client.ListActors(ctx, &ateapi.ListActorsRequest{
			Atespace: g.atespace,
			Options:  &ateapi.ListOptions{PageSize: listPageSize, PageToken: token},
		})
		if err != nil {
			return nil, mapErr("list actors", g.atespace, err)
		}

		for _, a := range resp.GetActors() {
			if n := a.GetMetadata().GetName(); strings.HasPrefix(n, prefix) {
				out = append(out, n)
			}
		}

		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// Create creates the actor template, the actor, then its egress policy, all named name. Parts that exist are kept.
func (g *GRPC) Create(ctx context.Context, name string, tmpl *ateapi.ActorTemplate, pol *ateapi.EgressPolicy) error {
	ctx, cancel := callCtx(ctx)
	defer cancel()

	tmpl = proto.CloneOf(tmpl)
	tmpl.Metadata = &ateapi.ResourceMetadata{Atespace: g.atespace, Name: name}

	_, err := g.client.CreateActorTemplate(ctx, &ateapi.CreateActorTemplateRequest{ActorTemplate: tmpl})
	if created(err) != nil {
		return mapErr("create actor template", name, err)
	}

	actor := &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: g.atespace, Name: name},
		ActorTemplate: g.ref(name),
	}
	if _, err = g.client.CreateActor(ctx, &ateapi.CreateActorRequest{Actor: actor}); created(err) != nil {
		return mapErr("create actor", name, err)
	}

	pol = proto.CloneOf(pol)
	pol.Metadata = &ateapi.ResourceMetadata{Atespace: g.atespace, Name: egressPolicyName}

	polReq := &ateapi.CreateActorEgressPolicyRequest{Actor: g.ref(name), EgressPolicy: pol}
	_, err = g.client.CreateActorEgressPolicy(ctx, polReq)

	return mapErr("create egress policy", name, created(err))
}

// Resume resumes a suspended actor; ErrNoRoom when no worker has room.
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
