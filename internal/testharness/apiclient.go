package testharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/alexbrand/software-factory/internal/apiserver"
)

// APIClient wraps the test API server with convenience methods.
type APIClient struct {
	baseURL string
	http    *http.Client
}

// BaseURL returns the API server's base URL.
func (c *APIClient) BaseURL() string { return c.baseURL }

// CreateWorkflow submits a workflow via POST /v1/workflows.
func (c *APIClient) CreateWorkflow(req apiserver.CreateWorkflowRequest) (*apiserver.WorkflowResponse, error) {
	var resp apiserver.WorkflowResponse
	if err := c.do(http.MethodPost, "/v1/workflows", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetWorkflow fetches a workflow via GET /v1/workflows/{id}.
func (c *APIClient) GetWorkflow(name string) (*apiserver.WorkflowResponse, error) {
	var resp apiserver.WorkflowResponse
	if err := c.do(http.MethodGet, "/v1/workflows/"+name, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteWorkflow deletes a workflow via DELETE /v1/workflows/{id}.
func (c *APIClient) DeleteWorkflow(name string) error {
	return c.do(http.MethodDelete, "/v1/workflows/"+name, nil, nil)
}

// CreateTask submits a task via POST /v1/tasks.
func (c *APIClient) CreateTask(req apiserver.CreateTaskRequest) (*apiserver.TaskResponse, error) {
	var resp apiserver.TaskResponse
	if err := c.do(http.MethodPost, "/v1/tasks", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetTask fetches a task via GET /v1/tasks/{id}.
func (c *APIClient) GetTask(name string) (*apiserver.TaskResponse, error) {
	var resp apiserver.TaskResponse
	if err := c.do(http.MethodGet, "/v1/tasks/"+name, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListPools lists all pools via GET /v1/pools.
func (c *APIClient) ListPools() ([]apiserver.PoolResponse, error) {
	var resp []apiserver.PoolResponse
	if err := c.do(http.MethodGet, "/v1/pools", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// GetPool fetches a pool via GET /v1/pools/{id}.
func (c *APIClient) GetPool(name string) (*apiserver.PoolResponse, error) {
	var resp apiserver.PoolResponse
	if err := c.do(http.MethodGet, "/v1/pools/"+name, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Raw makes a raw HTTP request and returns the response body.
func (c *APIClient) Raw(method, path string, body any) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *APIClient) do(method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("making request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp apiserver.ErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return fmt.Errorf("API error %d: %s", resp.StatusCode, errResp.Message)
		}
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	// 204 No Content — nothing to decode.
	if resp.StatusCode == http.StatusNoContent || out == nil {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response: %w (body: %s)", err, string(respBody))
	}
	return nil
}
