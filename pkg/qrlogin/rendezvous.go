package qrlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.mau.fi/util/random"
)

// ErrRendezvousExpired is returned when the homeserver no longer has the
// rendezvous session. Synapse keeps a session for 60 seconds after creation.
var ErrRendezvousExpired = errors.New("rendezvous session expired")

// pollInterval is the wait between two reads of the rendezvous session, as in
// matrix-rust-sdk.
var pollInterval = time.Second

const maxRendezvousBody = 64 * 1024

// rendezvous is the transport of the secure channel: a mailbox on the
// homeserver that holds one message at a time.
type rendezvous interface {
	// send replaces the content of the mailbox.
	send(ctx context.Context, message string) error
	// receive waits until the other side writes, and returns the content.
	receive(ctx context.Context) (string, error)
	// aad is the additional authenticated data for the next message.
	aad() []byte
	// close deletes the session where the server supports it.
	close(ctx context.Context)
}

// rendezvousError is a non-success HTTP status from the rendezvous endpoint.
type rendezvousError struct {
	op     string
	status int
}

func (e *rendezvousError) Error() string {
	return fmt.Sprintf("rendezvous %s: HTTP %d", e.op, e.status)
}

func statusError(op string, resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrRendezvousExpired
	}
	return &rendezvousError{op: op, status: resp.StatusCode}
}

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxRendezvousBody))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// msc4108Rendezvous is the MSC4108 rendezvous: a URL with ETag versioning.
type msc4108Rendezvous struct {
	http *http.Client
	url  string
	etag string
}

func joinClientPath(homeserver, path string) (string, error) {
	base, err := url.Parse(homeserver)
	if err != nil {
		return "", err
	}
	return base.JoinPath(path).String(), nil
}

func createMSC4108Rendezvous(ctx context.Context, cli *http.Client, homeserver string) (*msc4108Rendezvous, error) {
	endpoint, err := joinClientPath(homeserver, "/_matrix/client/unstable/org.matrix.msc4108/rendezvous")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, &rendezvousError{op: "create", status: resp.StatusCode}
	}
	var created struct {
		URL string `json:"url"`
	}
	if err = json.Unmarshal(body, &created); err != nil {
		return nil, fmt.Errorf("rendezvous create: %w", err)
	}
	if u, err := url.Parse(created.URL); err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("rendezvous create: invalid URL")
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return nil, fmt.Errorf("rendezvous create: no ETag")
	}
	return &msc4108Rendezvous{http: cli, url: created.URL, etag: etag}, nil
}

func (r *msc4108Rendezvous) send(ctx context.Context, message string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.url, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("If-Match", r.etag)
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRendezvousBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError("send", resp)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return fmt.Errorf("rendezvous send: no ETag")
	}
	r.etag = etag
	return nil
}

func (r *msc4108Rendezvous) receive(ctx context.Context) (string, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("If-None-Match", r.etag)
		resp, err := r.http.Do(req)
		if err != nil {
			return "", err
		}
		body, err := readBody(resp)
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}
		switch {
		case resp.StatusCode == http.StatusNotModified:
			if etag := resp.Header.Get("ETag"); etag != "" {
				r.etag = etag
			}
			if err = sleepCtx(ctx, pollInterval); err != nil {
				return "", err
			}
		case resp.StatusCode == http.StatusOK:
			if etag := resp.Header.Get("ETag"); etag != "" {
				r.etag = etag
			}
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || len(body) == 0 {
				return "", fmt.Errorf("rendezvous receive: unexpected empty or non-text message")
			}
			return string(body), nil
		default:
			return "", statusError("receive", resp)
		}
	}
}

func (r *msc4108Rendezvous) aad() []byte { return nil }

func (r *msc4108Rendezvous) close(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.url, nil)
	if err != nil {
		return
	}
	if resp, err := r.http.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// msc4388Rendezvous is the MSC4388 rendezvous: an ID on the homeserver with a
// sequence token. The AAD of each message binds the base URL, the ID and the
// sequence token.
type msc4388Rendezvous struct {
	http          *http.Client
	baseURL       string
	id            string
	sequenceToken string
}

const msc4388Path = "/_matrix/client/unstable/io.element.msc4388/rendezvous"

type msc4388Session struct {
	ID            string `json:"id,omitempty"`
	SequenceToken string `json:"sequence_token"`
	Data          string `json:"data"`
	ExpiresInMS   int64  `json:"expires_in_ms,omitempty"`
}

func createMSC4388Rendezvous(ctx context.Context, cli *http.Client, baseURL string) (*msc4388Rendezvous, error) {
	if len(baseURL) > 0xff {
		return nil, fmt.Errorf("rendezvous create: base URL too long")
	}
	endpoint, err := joinClientPath(baseURL, msc4388Path)
	if err != nil {
		return nil, err
	}
	var created msc4388Session
	if err = doJSON(ctx, cli, http.MethodPost, endpoint, map[string]string{"data": ""}, &created, "create"); err != nil {
		return nil, err
	}
	if created.ID == "" || len(created.ID) > 0xff || len(created.SequenceToken) > 0xff {
		return nil, fmt.Errorf("rendezvous create: invalid ID or sequence token")
	}
	return &msc4388Rendezvous{http: cli, baseURL: baseURL, id: created.ID, sequenceToken: created.SequenceToken}, nil
}

func doJSON(ctx context.Context, cli *http.Client, method, endpoint string, body, out any, op string) error {
	var reader io.Reader = http.NoBody
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(op, resp)
	}
	if out != nil {
		if err = json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("rendezvous %s: %w", op, err)
		}
	}
	return nil
}

func (r *msc4388Rendezvous) sessionURL(extra ...string) (string, error) {
	return joinClientPath(r.baseURL, strings.Join(append([]string{msc4388Path, r.id}, extra...), "/"))
}

func (r *msc4388Rendezvous) send(ctx context.Context, message string) error {
	endpoint, err := r.sessionURL(random.String(16))
	if err != nil {
		return err
	}
	var resp msc4388Session
	err = doJSON(ctx, r.http, http.MethodPut, endpoint, msc4388Session{SequenceToken: r.sequenceToken, Data: message}, &resp, "send")
	if err != nil {
		return err
	} else if len(resp.SequenceToken) > 0xff {
		return fmt.Errorf("rendezvous send: sequence token too long")
	}
	r.sequenceToken = resp.SequenceToken
	return nil
}

func (r *msc4388Rendezvous) receive(ctx context.Context) (string, error) {
	endpoint, err := r.sessionURL()
	if err != nil {
		return "", err
	}
	for {
		var resp msc4388Session
		if err = doJSON(ctx, r.http, http.MethodGet, endpoint, nil, &resp, "receive"); err != nil {
			return "", err
		} else if len(resp.SequenceToken) > 0xff {
			return "", fmt.Errorf("rendezvous receive: sequence token too long")
		}
		if resp.SequenceToken != r.sequenceToken {
			r.sequenceToken = resp.SequenceToken
			return resp.Data, nil
		}
		if err = sleepCtx(ctx, pollInterval); err != nil {
			return "", err
		}
	}
}

// aad is len(base URL) || base URL || len(ID) || ID || len(token) || token,
// each length one byte.
func (r *msc4388Rendezvous) aad() []byte {
	out := make([]byte, 0, 3+len(r.baseURL)+len(r.id)+len(r.sequenceToken))
	for _, s := range []string{r.baseURL, r.id, r.sequenceToken} {
		out = append(out, byte(len(s)))
		out = append(out, s...)
	}
	return out
}

func (r *msc4388Rendezvous) close(ctx context.Context) {
	endpoint, err := r.sessionURL()
	if err != nil {
		return
	}
	_ = doJSON(ctx, r.http, http.MethodDelete, endpoint, nil, nil, "delete")
}
