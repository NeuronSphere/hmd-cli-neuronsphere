package nsrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a running nsrunner.
//
// It is deliberately the same shape as the submit half of Argo's API, because
// that is what the service exposes and what hmd-ms-deployment will speak once
// runner selection lands: one client, two callers.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient addresses a runner. No timeout on the client itself -- following a
// deployment's log is a long-lived read, and a deadline here would cut off the
// very thing the stream exists for. Per-request budgets are set by context.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

// Reachable reports whether a runner is answering.
//
// This is what makes the service optional rather than a new hard dependency:
// the CLI asks, and falls back to executing nodes itself when the answer is no.
func (c *Client) Reachable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// Submit hands a manifest to the runner and returns the workflow's name.
func (c *Client) Submit(ctx context.Context, namespace string, m Manifest) (string, error) {
	body, err := json.Marshal(Submission{Namespace: namespace, Workflow: m})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/workflows/"+namespace, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("submitting to the runner at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &SubmitError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	var out struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("reading the runner's reply: %w", err)
	}
	if out.Metadata.Name == "" {
		return "", fmt.Errorf("the runner returned no workflow name")
	}
	return out.Metadata.Name, nil
}

// Get reads a workflow's current state.
func (c *Client) Get(ctx context.Context, namespace, name string) (Workflow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/workflows/"+namespace+"/"+name, nil)
	if err != nil {
		return Workflow{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Workflow{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return Workflow{}, &SubmitError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	var out struct {
		Status Workflow `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Workflow{}, err
	}
	return out.Status, nil
}

// Active returns the running workflow for an environment, if there is one.
// This is how `env attach` finds a deployment it did not start.
func (c *Client) Active(ctx context.Context, namespace, environment string) (Workflow, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/workflows/"+namespace+"?environment="+environment, nil)
	if err != nil {
		return Workflow{}, false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Workflow{}, false, err
	}
	defer resp.Body.Close()
	var out struct {
		Items []Workflow `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Workflow{}, false, err
	}
	for _, w := range out.Items {
		if !w.Phase.Terminal() {
			return w, true, nil
		}
	}
	return Workflow{}, false, nil
}

// Stream copies a workflow's output to w until the deployment finishes.
//
// The runner replays the backlog first, so a caller that attaches halfway
// through still sees the whole run rather than the tail of it.
func (c *Client) Stream(ctx context.Context, namespace, name string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/v1/workflows/"+namespace+"/"+name+"/log?follow=true", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return &SubmitError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	// Line-buffered rather than io.Copy so progress appears as it happens
	// rather than in chunks whenever the transport decides to flush.
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if _, werr := io.WriteString(w, line); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// SubmitError is a non-2xx from the runner. A 409 is the refusal to interleave
// two deployments on one environment, and callers distinguish it.
type SubmitError struct {
	Status int
	Body   string
}

func (e *SubmitError) Error() string {
	return fmt.Sprintf("the runner answered %d: %s", e.Status, e.Body)
}

// Busy reports the refusal to start a second deployment on one environment.
func (e *SubmitError) Busy() bool { return e.Status == http.StatusConflict }

// WaitForActive polls until a workflow appears for an environment.
//
// It exists because the fully inverted path is asynchronous end to end:
// apply_changeset returns as soon as the changeset is recorded and invokes the
// deploying Lambda in the background, so the workflow does not exist yet when
// the call returns. The CLI has a changeset id and needs the workflow the
// service is about to create for it.
func (c *Client) WaitForActive(ctx context.Context, namespace, environment, csdNid string, timeout time.Duration) (Workflow, error) {
	deadline := time.Now().Add(timeout)
	for {
		wf, active, err := c.Active(ctx, namespace, environment)
		if err == nil && active && (csdNid == "" || wf.CSDNid == csdNid) {
			return wf, nil
		}
		if time.Now().After(deadline) {
			return Workflow{}, &NoWorkflowError{Environment: environment, Timeout: timeout}
		}
		select {
		case <-ctx.Done():
			return Workflow{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// NoWorkflowError is the inverted submit path timing out: the changeset was
// applied and no workflow followed.
//
// A distinct type because the message it replaced blamed the one thing nsctl
// had already guaranteed. "The control plane may not be configured with
// HMD_WORKFLOW_RUNNER_URL" sent the reader to check a variable that
// controlplane.syncRunnerSelection reconciles onto the deployed Lambda on every
// start, and that this code path cannot be reached without -- the runner has to
// be answering for the CLI to be waiting at all.
//
// The real cause, found the first time this path ever ran:
// hmd-ms-deployment's call_deploy_change_set_deployment builds a synthetic
// Lambda event with `"headers": {"Authorization": auth_token}` and invokes
// itself. Locally there is no auth, so auth_token is None, and Mangum's API
// Gateway handler does `[[k.encode(), v.encode()] for k, v in headers.items()]`
// -- AttributeError: 'NoneType' object has no attribute 'encode'. The
// self-invocation dies before reaching any runner, and because it is an Event
// invocation nothing surfaces to the caller: apply_changeset already returned
// 200 and the CLI waits out its timeout against a workflow that was never going
// to be created.
//
// A cloud caller passes a real token, so this is local-only, and it is a change
// in that repository -- omitting the header when there is no token.
type NoWorkflowError struct {
	Environment string
	Timeout     time.Duration
}

func (e *NoWorkflowError) Error() string {
	return fmt.Sprintf(
		"no workflow appeared for %s within %s. The changeset was applied and the control plane's "+
			"asynchronous self-invocation did not reach the runner.\n"+
			"  Known cause: hmd-ms-deployment's call_deploy_change_set_deployment sends "+
			"`\"Authorization\": None` in the event it invokes itself with, and Mangum cannot encode a "+
			"None header value -- so the deploying invocation dies before it submits anything, silently, "+
			"because it is an Event invocation.\n"+
			"  Check the ms-deployment Lambda's log for \"'NoneType' object has no attribute 'encode'\". "+
			"Until that is fixed there, leave HMD_LOCAL_SERVICE_SUBMIT unset and the CLI submits directly.",
		e.Environment, e.Timeout)
}
