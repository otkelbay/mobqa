package hubws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
)

const (
	minBackoff = time.Second
	maxBackoff = time.Minute
)

// ErrRejected means the API refused the hub's token; retrying cannot help until the token is replaced.
var ErrRejected = errors.New("the API rejected the hub token")

var errUnknownRequest = errors.New("the hub does not handle this request")

type Agent interface {
	Inventory(ctx context.Context) (hub.Inventory, error)
	Execute(ctx context.Context, execution run.Execution) (run.Outcome, error)
	Observe(ctx context.Context, target ui.Target) (ui.Screen, error)
	Act(ctx context.Context, command ui.Command) error
	Capture(ctx context.Context, target ui.Target) ([]byte, error)
	Perform(ctx context.Context, request action.Request, variables map[string]string) (map[string]string, error)
	Publish(ctx context.Context, target ui.Target, offer string) (string, error)
	Watch(ctx context.Context, target ui.Target, offer string) (string, error)
}

type ClientConfig struct {
	// URL is the API's base URL, http(s) or ws(s).
	URL   string
	Token string
	// PollInterval is how often the hub looks for attached and removed devices.
	PollInterval time.Duration
}

// Client keeps a hub connected to the API: it reports the inventory on connect and whenever it changes,
// and reconnects with jittered exponential backoff.
type Client struct {
	cfg    ClientConfig
	agent  Agent
	logger *slog.Logger
}

func NewClient(cfg ClientConfig, agent Agent, logger *slog.Logger) *Client {
	return &Client{cfg: cfg, agent: agent, logger: logger}
}

// Run keeps the link up until ctx ends or the API rejects the token.
func (c *Client) Run(ctx context.Context) error {
	backoff := minBackoff
	for {
		connected, err := c.connect(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		switch {
		case errors.Is(err, ErrRejected):
			return err
		case connected:
			backoff = minBackoff
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		backoff = min(2*backoff, maxBackoff)
		c.logger.Warn("hub link down", "error", err, "retry_in", wait.String())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
	}
}

// connect runs one connection and reports whether it got as far as sending the inventory.
func (c *Client) connect(ctx context.Context) (bool, error) {
	inventory, err := c.agent.Inventory(ctx)
	if err != nil {
		return false, fmt.Errorf("discover devices: %w", err)
	}
	conn, resp, err := websocket.Dial(ctx, c.cfg.URL+Path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.cfg.Token}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return false, ErrRejected
	}
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	p := newPeer(ctx, conn, c, c.logger)
	if err := p.notify(ctx, TypeInventory, newInventory(inventory)); err != nil {
		_ = conn.CloseNow()
		return false, err
	}
	c.logger.Info("hub connected", "devices", len(inventory.Devices))
	var reporter sync.WaitGroup
	reporter.Go(func() { p.close(c.report(p, inventory)) })
	cause := p.run()
	reporter.Wait()
	return true, cause
}

// report polls the agent and sends the inventory whenever it changes.
func (c *Client) report(p *peer, sent hub.Inventory) error {
	poll := time.NewTicker(c.cfg.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-p.done():
			return context.Cause(p.ctx)
		case <-poll.C:
		}
		current, err := c.agent.Inventory(p.ctx)
		if err != nil {
			c.logger.Warn("discover devices", "error", err)
			continue
		}
		if current.Version == sent.Version && slices.Equal(current.Devices, sent.Devices) {
			continue
		}
		if err := p.notify(p.ctx, TypeInventory, newInventory(current)); err != nil {
			return err
		}
		sent = current
	}
}

func (c *Client) event(_ context.Context, env Envelope) error {
	if env.Type != TypeHeartbeat {
		c.logger.Warn("unexpected event from the API", "type", env.Type)
	}
	return nil
}

func (c *Client) request(ctx context.Context, env Envelope) (any, error) {
	switch env.Type {
	case TypeExecute:
		return c.execute(ctx, env)
	case TypeObserve:
		var target Target
		if err := env.decode(&target); err != nil {
			return nil, err
		}
		screen, err := c.agent.Observe(ctx, target.ui())
		if err != nil {
			return nil, err
		}
		return newScreen(screen), nil
	case TypeAct:
		var command Command
		if err := env.decode(&command); err != nil {
			return nil, err
		}
		return struct{}{}, c.agent.Act(ctx, command.ui())
	case TypeCapture:
		var target Target
		if err := env.decode(&target); err != nil {
			return nil, err
		}
		shot, err := c.agent.Capture(ctx, target.ui())
		if err != nil {
			return nil, err
		}
		return Capture{JPEG: shot}, nil
	case TypePerform:
		var perform Perform
		if err := env.decode(&perform); err != nil {
			return nil, err
		}
		extracted, err := c.agent.Perform(ctx, perform.Action.request(), perform.Variables)
		if err != nil {
			return nil, err
		}
		return Performed{Variables: extracted}, nil
	case TypePublish, TypeWatch:
		return c.stream(ctx, env)
	default:
		return nil, errUnknownRequest
	}
}

func (c *Client) execute(ctx context.Context, env Envelope) (any, error) {
	var execution Execution
	if err := env.decode(&execution); err != nil {
		return nil, err
	}
	outcome, err := c.agent.Execute(ctx, execution.run())
	if err != nil {
		return nil, err
	}
	return newOutcome(outcome), nil
}

func (c *Client) stream(ctx context.Context, env Envelope) (any, error) {
	var offer Offer
	if err := env.decode(&offer); err != nil {
		return nil, err
	}
	answer := c.agent.Watch
	if env.Type == TypePublish {
		answer = c.agent.Publish
	}
	sdp, err := answer(ctx, offer.target(), offer.SDP)
	if err != nil {
		return nil, err
	}
	return Answer{SDP: sdp}, nil
}
