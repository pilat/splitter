package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	log "github.com/inconshreveable/log15"
)

type Client interface {
	ListArtifacts(ctx context.Context) ([]Artifact, error)
	DownloadArtifact(ctx context.Context, artifactID int64) ([]byte, error)
}

type client struct {
	log  log.Logger
	opts Opts
}

type Opts struct {
	Token string
	Repo  string // owner/repo
}

type Artifact struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	CreatedAt   string      `json:"created_at"`
	Expired     bool        `json:"expired"`
	WorkflowRun WorkflowRun `json:"workflow_run"`
}

type WorkflowRun struct {
	ID         int64  `json:"id"`
	HeadBranch string `json:"head_branch"`
}

func New(opts Opts, log log.Logger) *client {
	return &client{
		log:  log,
		opts: opts,
	}
}

func (c *client) ListArtifacts(ctx context.Context) ([]Artifact, error) {
	c.log.Info("listing artifacts", "repo", c.opts.Repo)

	artifacts := make([]Artifact, 0)

	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/actions/artifacts?per_page=100&page=%d", c.opts.Repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.opts.Token))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		tmp := struct {
			Artifacts  []Artifact `json:"artifacts"`
			TotalCount int        `json:"total_count"`
		}{}
		if err := json.Unmarshal(body, &tmp); err != nil {
			return nil, err
		}

		artifacts = append(artifacts, tmp.Artifacts...)
	}

	return artifacts, nil
}

func (c *client) DownloadArtifact(ctx context.Context, artifactID int64) ([]byte, error) {
	c.log.Info("downloading artifact", "artifactID", artifactID)

	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/artifacts/%d/zip", c.opts.Repo, artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.opts.Token))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return ioutil.ReadAll(resp.Body)
}
