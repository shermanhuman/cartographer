package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// adminClient copies breakdown-sales' cold-email client: post is a wrapper
// that fixes the method; do takes it from the call site.
type adminClient struct {
	baseURL, key string
	http         *http.Client
}

func (a *adminClient) post(ctx context.Context, path string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(out)
}

func (a *adminClient) Check(ctx context.Context, email string) error {
	return a.post(ctx, "/api/v1/sales/cold/check", email, nil)
}

func (a *adminClient) Suppress(ctx context.Context, email string) error {
	return a.post(ctx, "/api/v1/sales/cold/suppress", email, nil)
}

func (a *adminClient) do(ctx context.Context, method, path string) error {
	req, err := http.NewRequest(method, a.baseURL+path, nil)
	if err != nil {
		return err
	}
	_, err = a.http.Do(req.WithContext(ctx))
	return err
}

// get passes its path parameter through to do, so it is a wrapper too.
func (a *adminClient) get(ctx context.Context, path string) error {
	return a.do(ctx, http.MethodGet, path)
}

// Lookup: an interpolated path (GET via get), a method fixed at the call
// site, and a computed path (not extracted).
func (a *adminClient) Lookup(ctx context.Context, id string) error {
	if err := a.get(ctx, "/api/v1/people/"+id); err != nil {
		return err
	}
	if err := a.do(ctx, "DELETE", "/api/v1/cache"); err != nil {
		return err
	}
	return a.do(ctx, http.MethodGet, strings.TrimSpace(id))
}

// gmail puts a literal prefix before its path parameter (breakdown-sales'
// gmailJSON); err.Error() is external, not unresolved.
func (a *adminClient) gmail(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/gmail/v1/"+path, nil)
	if err != nil {
		return fmt.Errorf("gmail: %s", err.Error())
	}
	_, err = a.http.Do(req)
	return err
}

// Message: an escaped value is :id, and the query string is dropped.
func (a *adminClient) Message(ctx context.Context, id string, q url.Values) error {
	return a.gmail(ctx, "messages/"+url.PathEscape(id)+"?"+q.Encode())
}
