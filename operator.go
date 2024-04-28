package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlser "k8s.io/apimachinery/pkg/runtime/serializer/yaml"
)

const (
	managedByLabel     = "app.kubernetes.io/managed-by"
	commitIdAnnotation = "meta.knops/commit-id"
	fileIdAnnotation   = "meta.knops/file-id"
)

type DotNops struct {
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels"`
}

type Operator struct {
	git         *GitClient
	kc          *KubeClient
	cacheIds    map[string]string
	kinds       []string
	namespaces  []string
	allowCreate bool
	onlyManaged bool
}

func (o *Operator) applyFile(ctx context.Context, dotNops *DotNops, commitId string, file *object.File) error {
	desired, gvk, err := loadUnstructuredFromFile(file)
	if err != nil {
		return err
	}

	if !slices.Contains(o.kinds, gvk.Kind) {
		return fmt.Errorf("Unsupported kind: %s", gvk.Kind)
	}
	if desired.GetNamespace() != dotNops.Namespace {
		return fmt.Errorf("Namespace mismatched: %s", desired.GetNamespace())
	}

	actual, err := o.kc.get(ctx, *gvk, dotNops.Namespace, desired.GetName())
	if err != nil {
		if !errors.IsNotFound(err) || !o.allowCreate {
			return err
		}
	}

	if actual != nil {
		labels := actual.GetLabels()
		if o.onlyManaged && labels[managedByLabel] != operatorName {
			slog.Debug("ignore unmanaged object", "name", actual.GetName())
			return nil
		}

		annotations := actual.GetAnnotations()
		if annotations[fileIdAnnotation] == file.ID().String() {
			slog.Debug("ignore same object", "name", actual.GetName())
			return nil
		}
	}

	labels := desired.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	maps.Copy(labels, dotNops.Labels)
	labels[managedByLabel] = operatorName
	desired.SetLabels(labels)

	annotations := desired.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[commitIdAnnotation] = commitId
	annotations[fileIdAnnotation] = file.ID().String()
	desired.SetAnnotations(annotations)

	if actual != nil {
		desired.SetResourceVersion(actual.GetResourceVersion())
		_, err := o.kc.update(ctx, desired)
		if err != nil {
			return err
		}
	} else {
		_, err := o.kc.create(ctx, desired)
		if err != nil {
			return err
		}
	}

	return nil
}

func (o *Operator) deployCommit(ctx context.Context, commit *object.Commit) error {
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	dotNops, err := loadDotNopsFromTree(tree)
	if err != nil {
		return fmt.Errorf("Load .nops.yaml: %v", err)
	}

	if len(o.namespaces) > 0 && !slices.Contains(o.namespaces, dotNops.Namespace) {
		return fmt.Errorf("Restricted namespace: %s", dotNops.Namespace)
	}

	commitId := commit.ID().String()

	err = tree.Files().ForEach(func(f *object.File) error {
		if !strings.HasSuffix(f.Name, ".yaml") || strings.HasPrefix(path.Base(f.Name), ".") {
			return nil
		}

		fileId := f.ID().String()
		slog.Debug("apply file", "file", f.Name, "id", fileId)

		if o.cacheIds != nil && o.cacheIds[f.Name] == fileId {
			slog.Debug("ignore object by same cache id", "name", f.Name)
			return nil
		}

		err := o.applyFile(ctx, dotNops, commitId, f)
		if err != nil {
			slog.Warn("apply file", "file", f.Name, "err", err)
			return nil
		}

		// cache applied file id
		if o.cacheIds != nil {
			// TODO: concurrent map access
			o.cacheIds[f.Name] = fileId
		}

		return nil
	})
	if err != nil {
		return err
	}

	// TODO: remove deleted file id

	return nil
}

func (o *Operator) cloneRepoAndDeploy(ctx context.Context) error {
	repo, err := o.git.clone(ctx)
	if err != nil {
		return err
	}

	headRef, err := repo.Head()
	if err != nil {
		return err
	}

	head, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return err
	}

	slog.Info("deploy HEAD commit", "id", head.ID(), "message", strings.SplitN(head.Message, "\n", 2)[0])

	err = o.deployCommit(ctx, head)
	if err != nil {
		return err
	}

	slog.Info("deploy commit done", "id", head.ID())
	return nil
}

func loadDotNopsFromTree(tree *object.Tree) (*DotNops, error) {
	dotNopsYaml, err := tree.File(".nops.yaml")
	if err != nil {
		return nil, err
	}

	dotNopsReader, err := dotNopsYaml.Reader()
	if err != nil {
		return nil, err
	}
	defer dotNopsReader.Close()

	dotNopsData, err := io.ReadAll(dotNopsReader)
	if err != nil {
		return nil, err
	}

	var dotNops DotNops
	err = yaml.Unmarshal(dotNopsData, &dotNops)
	if err != nil {
		return nil, err
	}

	return &dotNops, nil
}

func loadUnstructuredFromFile(file *object.File) (*unstructured.Unstructured, *schema.GroupVersionKind, error) {
	rd, err := file.Reader()
	if err != nil {
		return nil, nil, err
	}
	defer rd.Close()

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, nil, err
	}
	dec := yamlser.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	var obj unstructured.Unstructured
	_, gvk, err := dec.Decode(data, nil, &obj)
	if err != nil {
		return nil, nil, err
	}
	return &obj, gvk, nil
}
