package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	operatorName = "knops"
)

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
	os.Exit(1)
}

type Config struct {
	Http struct {
		Listen string `yaml:"listen"`
		Key    string `yaml:"key"`
	} `yaml:"http"`
	Repo struct {
		Url      string            `yaml:"url"`
		Branch   string            `yaml:"branch"`
		Auth     map[string]string `yaml:"auth"`
		Dir      string            `yaml:"dir"`
		Progress bool              `yaml:"progress"`
		Force    bool              `yaml:"force"`
	} `yaml:"repo"`
	Operator struct {
		OnlyManaged bool     `yaml:"onlyManaged"`
		AllowCreate bool     `yaml:"allowCreate"`
		CacheFileId bool     `yaml:"cacheFileId"`
		Kinds       []string `yaml:"kinds"`
		Namespaces  []string `yaml:"namespaces"`
	} `yaml:"operator"`
	Debug bool `yaml:"debug"`
}

func loadConfig(fname string) (*Config, error) {
	data, err := os.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	var conf Config
	err = yaml.Unmarshal(data, &conf)
	if err != nil {
		return nil, err
	}
	return &conf, nil
}

type DeployJob struct {
	ctx context.Context
	res chan error
}

func main() {
	var (
		configFile string
	)
	flag.StringVar(&configFile, "c", "./config.yaml", "config path")
	flag.Parse()

	conf, err := loadConfig(configFile)
	if err != nil {
		fatal("load config:", err)
	}

	if conf.Debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	gitClient, err := newGitClient(conf)
	if err != nil {
		fatal("new git client:", err)
	}

	kubeClient, err := newKubeClient(operatorName)
	if err != nil {
		fatal("new kube client", err)
	}

	operator := &Operator{
		git:         gitClient,
		kc:          kubeClient,
		kinds:       conf.Operator.Kinds,
		namespaces:  conf.Operator.Namespaces,
		allowCreate: conf.Operator.AllowCreate,
		onlyManaged: conf.Operator.OnlyManaged,
	}

	if conf.Operator.CacheFileId {
		operator.cacheIds = make(map[string]string)
	}

	deployJobs := make(chan DeployJob, 16)
	go func() {
		const deployTimeout = 3 * time.Minute

		for {
			job := <-deployJobs

			select {
			case <-job.ctx.Done():
				continue
			default:
			}

			ctx, cancel := context.WithTimeout(job.ctx, deployTimeout)
			err := operator.cloneRepoAndDeploy(ctx)
			cancel()
			if err != nil {
				slog.Error("deploy", "err", err)
			}

			if job.res != nil {
				job.res <- err
			}
		}
	}()

	http.Handle("POST /deploy/trigger", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("key") != conf.Http.Key {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		if r.FormValue("sync") != "1" {
			select {
			case deployJobs <- DeployJob{ctx: context.Background()}:
				w.Write([]byte("triggered"))
			case <-r.Context().Done():
				slog.Info("deploy cancelled")
			}

			return
		}

		res := make(chan error, 1)
		select {
		case deployJobs <- DeployJob{ctx: r.Context(), res: res}:
		case <-r.Context().Done():
			slog.Info("deploy cancelled")
			return
		}

		err := <-res
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			w.Write([]byte("finished"))
		}
	}))
	err = http.ListenAndServe(conf.Http.Listen, nil)
	if err != nil {
		fatal("http listen:", err)
	}
}
