package main

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

type KubeClient struct {
	name       string
	di         dynamic.Interface
	restMapper *restmapper.DeferredDiscoveryRESTMapper
}

func (c *KubeClient) resourceInterface(gvk schema.GroupVersionKind, ns string) (dynamic.ResourceInterface, error) {
	gvr, err := c.restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}

	var ri dynamic.ResourceInterface
	if gvr.Scope.Name() == meta.RESTScopeNameNamespace {
		ri = c.di.Resource(gvr.Resource).Namespace(ns)
	} else {
		ri = c.di.Resource(gvr.Resource)
	}
	return ri, nil
}

func (c *KubeClient) get(ctx context.Context, gvk schema.GroupVersionKind, ns string, name string) (*unstructured.Unstructured, error) {
	ri, err := c.resourceInterface(gvk, ns)
	if err != nil {
		return nil, err
	}
	return ri.Get(ctx, name, metav1.GetOptions{})
}

func (c *KubeClient) create(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := c.resourceInterface(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	return ri.Create(ctx, obj, metav1.CreateOptions{FieldManager: c.name})
}

func (c *KubeClient) update(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	ri, err := c.resourceInterface(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}
	return ri.Update(ctx, obj, metav1.UpdateOptions{FieldManager: c.name})
}

func newKubeClient(name string) (*KubeClient, error) {
	// config, err := clientcmd.BuildConfigFromFlags("", os.ExpandEnv("$HOME/.kube/config"))
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	di, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	return &KubeClient{
		name:       name,
		di:         di,
		restMapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)),
	}, nil
}
