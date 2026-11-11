package hubws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/hub"
)

const (
	// readTimeout closes a link that has been silent for three heartbeats.
	readTimeout  = 45 * time.Second
	writeTimeout = 10 * time.Second
	// maxMessageBytes bounds one envelope; bulk data such as packages travels over HTTP, not the link.
	maxMessageBytes = 1 << 20
	outboxSize      = 64
	// maxInflight bounds the requests a peer serves at once; more are refused rather than queued.
	maxInflight = 32
)

var errLinkClosed = fault.WithDetail(fault.ErrUnavailable, "the hub link closed")

// RemoteError is a request that the other side received and failed.
type RemoteError struct {
	Message string
}

func (e *RemoteError) Error() string {
	return "remote: " + e.Message
}

type handler interface {
	// event handles a message that needs no reply; an error closes the link.
	event(ctx context.Context, env Envelope) error
	request(ctx context.Context, env Envelope) (any, error)
}

// peer owns one connection: a reader goroutine, a writer goroutine fed by the outbox, and one goroutine
// per request being served. Reads and writes are not cancelled by the peer's context, because cancelling
// one drops the connection without a close frame; run closes the connection instead, which ends them.
type peer struct {
	conn     *websocket.Conn
	handler  handler
	logger   *slog.Logger
	ctx      context.Context
	cancel   context.CancelCauseFunc
	outbox   chan Envelope
	inflight chan struct{}
	serving  sync.WaitGroup
	nextID   atomic.Uint64
	mu       sync.Mutex
	pending  map[string]chan Envelope
	// aborts cancels the requests being served, by request ID.
	aborts map[string]context.CancelFunc
}

func newPeer(ctx context.Context, conn *websocket.Conn, h handler, logger *slog.Logger) *peer {
	conn.SetReadLimit(maxMessageBytes)
	ctx, cancel := context.WithCancelCause(ctx)
	return &peer{
		conn: conn, handler: h, logger: logger, ctx: ctx, cancel: cancel,
		outbox: make(chan Envelope, outboxSize), inflight: make(chan struct{}, maxInflight),
		pending: map[string]chan Envelope{}, aborts: map[string]context.CancelFunc{},
	}
}

// run serves the link until it fails or the peer is closed, then closes the connection with a status that
// tells the other side why, and returns the cause.
func (p *peer) run() error {
	var loops sync.WaitGroup
	loops.Go(func() { p.cancel(p.write(p.ctx)) })
	loops.Go(func() { p.cancel(p.read(p.ctx)) })
	<-p.ctx.Done()
	cause := context.Cause(p.ctx)
	code, reason := closeStatus(cause)
	_ = p.conn.Close(code, reason)
	loops.Wait()
	p.serving.Wait()
	return cause
}

func closeStatus(cause error) (websocket.StatusCode, string) {
	switch {
	case errors.Is(cause, errReplaced), errors.Is(cause, errShutdown), errors.Is(cause, context.Canceled):
		return websocket.StatusGoingAway, "shutting down or replaced"
	case errors.Is(cause, fault.ErrInvalidInput):
		detail, _ := fault.Detail(cause)
		return websocket.StatusPolicyViolation, detail
	default:
		return websocket.StatusInternalError, ""
	}
}

func (p *peer) close(cause error) {
	p.cancel(cause)
}

func (p *peer) done() <-chan struct{} {
	return p.ctx.Done()
}

func (p *peer) read(ctx context.Context) error {
	for {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		var env Envelope
		err := wsjson.Read(readCtx, p.conn, &env)
		cancel()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		switch {
		case env.ReplyTo != "":
			p.resolve(env)
		case env.ID != "":
			p.serve(ctx, env)
		case env.Type == TypeCancel:
			p.abort(env)
		default:
			if err := p.handler.event(ctx, env); err != nil {
				return err
			}
		}
	}
}

// write drains the outbox and sends a heartbeat whenever the link has been idle for an interval, so each
// side's read timeout only fires on a dead link.
func (p *peer) write(ctx context.Context) error {
	idle := time.NewTimer(hub.HeartbeatInterval)
	defer idle.Stop()
	for {
		var env Envelope
		select {
		case env = <-p.outbox:
		case <-idle.C:
			env = Envelope{Type: TypeHeartbeat}
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		err := wsjson.Write(writeCtx, p.conn, env)
		cancel()
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		idle.Reset(hub.HeartbeatInterval)
	}
}

func (p *peer) send(ctx context.Context, env Envelope) error {
	select {
	case p.outbox <- env:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return errLinkClosed
	}
}

func (p *peer) notify(ctx context.Context, typ string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", typ, err)
	}
	return p.send(ctx, Envelope{Type: typ, Payload: encoded})
}

// call sends a request and waits for its reply's payload.
func (p *peer) call(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", typ, err)
	}
	id := strconv.FormatUint(p.nextID.Add(1), 10)
	reply := make(chan Envelope, 1)
	p.mu.Lock()
	p.pending[id] = reply
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	if err := p.send(ctx, Envelope{Type: typ, ID: id, Payload: encoded}); err != nil {
		return nil, err
	}
	select {
	case env := <-reply:
		if env.Error != "" {
			return nil, &RemoteError{Message: env.Error}
		}
		return env.Payload, nil
	case <-ctx.Done():
		p.cancelRemote(id)
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, errLinkClosed
	}
}

// cancelRemote tells the other side to stop serving an abandoned request. It never blocks: if the outbox
// is full the request simply runs to completion and its reply is dropped.
func (p *peer) cancelRemote(id string) {
	encoded, err := json.Marshal(Cancel{RequestID: id})
	if err != nil {
		return
	}
	select {
	case p.outbox <- Envelope{Type: TypeCancel, Payload: encoded}:
	default:
		p.logger.Debug("cancel dropped", "request_id", id)
	}
}

func (p *peer) abort(env Envelope) {
	var cancel Cancel
	if err := env.decode(&cancel); err != nil {
		p.logger.Debug("malformed cancel", "error", err)
		return
	}
	p.mu.Lock()
	abort, ok := p.aborts[cancel.RequestID]
	p.mu.Unlock()
	if ok {
		abort()
	}
}

func (p *peer) resolve(env Envelope) {
	p.mu.Lock()
	reply, ok := p.pending[env.ReplyTo]
	p.mu.Unlock()
	if !ok {
		p.logger.Debug("reply to an abandoned request", "reply_to", env.ReplyTo)
		return
	}
	select {
	case reply <- env:
	default:
		p.logger.Debug("duplicate reply", "reply_to", env.ReplyTo)
	}
}

func (p *peer) serve(ctx context.Context, env Envelope) {
	select {
	case p.inflight <- struct{}{}:
	default:
		p.reply(env, nil, errors.New("too many concurrent requests"))
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.aborts[env.ID] = cancel
	p.mu.Unlock()
	p.serving.Go(func() {
		defer func() {
			p.mu.Lock()
			delete(p.aborts, env.ID)
			p.mu.Unlock()
			cancel()
			<-p.inflight
		}()
		result, err := p.handler.request(ctx, env)
		p.reply(env, result, err)
	})
}

func (p *peer) reply(request Envelope, result any, failure error) {
	env := Envelope{ReplyTo: request.ID}
	if failure == nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			failure = fmt.Errorf("encode %s reply: %w", request.Type, err)
		} else {
			env.Payload = encoded
		}
	}
	if failure != nil {
		env.Error = failure.Error()
	}
	if err := p.send(p.ctx, env); err != nil {
		p.logger.Debug("reply dropped", "type", request.Type, "error", err)
	}
}
