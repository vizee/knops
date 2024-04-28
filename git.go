package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

type GitClient struct {
	url      string
	branch   string
	auth     transport.AuthMethod
	dir      string
	progress bool
}

func (c *GitClient) clone(ctx context.Context) (*git.Repository, error) {
	slog.Debug("clone repo", "url", c.url, "branch", c.branch, "to", c.dir)

	err := os.RemoveAll(c.dir)
	if err != nil {
		return nil, err
	}
	storage := filesystem.NewStorage(osfs.New(c.dir), cache.NewObjectLRUDefault())
	var progress sideband.Progress
	if c.progress {
		progress = os.Stderr
	}
	return git.CloneContext(ctx, storage, nil, &git.CloneOptions{
		URL:           c.url,
		Auth:          c.auth,
		ReferenceName: plumbing.ReferenceName(c.branch),
		SingleBranch:  true,
		NoCheckout:    true,
		Depth:         1,
		Tags:          git.NoTags,
		Progress:      progress,
	})
}

func getGitAuth(conf *Config) (transport.AuthMethod, error) {
	authType := conf.Repo.Auth["type"]
	switch authType {
	case "ssh":
		sshAuth, err := ssh.NewPublicKeys(conf.Repo.Auth["user"], []byte(conf.Repo.Auth["privateKey"]), conf.Repo.Auth["password"])
		if err != nil {
			return nil, err
		}
		return sshAuth, nil
	case "http-basic-auth":
		return &http.BasicAuth{
			Username: conf.Repo.Auth["username"],
			Password: conf.Repo.Auth["password"],
		}, nil
	case "http-token-auth":
		return &http.TokenAuth{
			Token: conf.Repo.Auth["token"],
		}, nil
	case "", "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("Invalid auth type: %s", authType)
	}
}

func newGitClient(conf *Config) (*GitClient, error) {
	repoUrl := conf.Repo.Url
	if repoUrl == "" {
		return nil, fmt.Errorf("repo.url is required")
	}
	branch := cmp.Or(conf.Repo.Branch, "master")
	repoAuth, err := getGitAuth(conf)
	if err != nil {
		return nil, err
	}
	repoDir := cmp.Or(conf.Repo.Dir, filepath.Join(os.TempDir(), path.Base(strings.TrimRight(repoUrl, "/"))))

	if !conf.Repo.Force {
		_, err = os.Stat(repoDir)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("%s already exists", repoDir)
		}
	}

	return &GitClient{
		url:      repoUrl,
		branch:   branch,
		auth:     repoAuth,
		dir:      repoDir,
		progress: conf.Repo.Progress,
	}, nil
}
