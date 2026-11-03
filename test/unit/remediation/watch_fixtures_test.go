/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package remediation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	remediationctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/remediation"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// watchManager captures the real controller registered by SetupWithManager.
// Informers deliver events to its actual handlers, queue, and Reconcile method.
// The client records the first CR read and returns NotFound to keep these tests
// focused on event routing without running remediation after each event.
type watchManager struct {
	manager.Manager
	scheme     *runtime.Scheme
	reader     client.Reader
	informers  cache.Cache
	controller manager.Runnable
}

func (m *watchManager) GetScheme() *runtime.Scheme  { return m.scheme }
func (m *watchManager) GetAPIReader() client.Reader { return m.reader }
func (m *watchManager) GetCache() cache.Cache       { return m.informers }
func (m *watchManager) GetLogger() logr.Logger      { return logr.Discard() }
func (m *watchManager) GetControllerOptions() config.Controller {
	skipNameValidation := true
	return config.Controller{SkipNameValidation: &skipNameValidation}
}
func (m *watchManager) Add(r manager.Runnable) error { m.controller = r; return nil }

type readyInformer struct {
	*controllertest.FakeInformer
	ready chan struct{}
}

func (i *readyInformer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, options toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	registration, err := i.FakeInformer.AddEventHandlerWithOptions(handler, options)
	close(i.ready)
	return registration, err
}

type reconcileReadRecorder struct {
	client.Client
	requests chan client.ObjectKey
}

func (c *reconcileReadRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*remediationv1.PodRemediator); ok {
		select {
		case c.requests <- key:
		case <-ctx.Done():
			return ctx.Err()
		}
		return apierrors.NewNotFound(schema.GroupResource{Group: "remediation.openstack.org", Resource: "podremediators"}, key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

type watchHarness struct {
	requests <-chan client.ObjectKey
	pvc      *readyInformer
	node     *readyInformer
}

func startWatchHarness(ctx context.Context, t *testing.T, r *remediationctrl.PodRemediatorReconciler) *watchHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	requests := make(chan client.ObjectKey, 100)
	r.Client = &reconcileReadRecorder{Client: r.Client, requests: requests}
	informerFor := func() *readyInformer {
		return &readyInformer{FakeInformer: &controllertest.FakeInformer{Synced: true}, ready: make(chan struct{})}
	}
	prInformer, pvcInformer, nodeInformer := informerFor(), informerFor(), informerFor()
	informers := &informertest.FakeInformers{Scheme: r.Scheme, InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{
		remediationv1.GroupVersion.WithKind("PodRemediator"):        prInformer,
		corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim"): pvcInformer,
		corev1.SchemeGroupVersion.WithKind("Node"):                  nodeInformer,
	}}
	mgr := &watchManager{scheme: r.Scheme, reader: r.Client, informers: informers}
	if err := r.SetupWithManager(ctx, mgr); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mgr.controller.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("watch controller stopped with an error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("watch controller did not stop")
		}
	})
	for _, informer := range []*readyInformer{prInformer, pvcInformer, nodeInformer} {
		select {
		case <-informer.ready:
		case <-time.After(5 * time.Second):
			t.Fatal("controller did not register its watch handlers")
		}
	}
	return &watchHarness{requests: requests, pvc: pvcInformer, node: nodeInformer}
}
func expectWatchRequest(t *testing.T, requests <-chan client.ObjectKey, key client.ObjectKey) {
	t.Helper()
	select {
	case got := <-requests:
		if got != key {
			t.Fatalf("watch enqueued %v, want %v", got, key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not enqueue PodRemediator")
	}
}
func expectNoWatchRequest(t *testing.T, requests <-chan client.ObjectKey) {
	t.Helper()
	select {
	case got := <-requests:
		t.Fatalf("unexpected reconcile request: %v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPVCEventEnqueuesPodRemediatorWatchingItsNamespace(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t)
	pr.Spec.Namespaces = []string{"test", "workload"}
	if err := r.Create(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	harness := startWatchHarness(ctx, t, r)
	for _, namespace := range []string{"workload", "unwatched"} {
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: namespace}}
		harness.pvc.Add(pvc)
		if namespace == "workload" {
			expectWatchRequest(t, harness.requests, client.ObjectKeyFromObject(pr))
		} else {
			expectNoWatchRequest(t, harness.requests)
		}
	}
}

func TestNodeWatchOnlyEnqueuesOnHealthChanges(t *testing.T) {
	ctx := context.Background()
	r, pr := remediationFixture(t)
	if err := r.Create(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	harness := startWatchHarness(ctx, t, r)
	for _, tc := range []struct {
		name          string
		before, after corev1.ConditionStatus
		enqueue       bool
	}{
		{"heartbeat", corev1.ConditionTrue, corev1.ConditionTrue, false},
		{"failure", corev1.ConditionTrue, corev1.ConditionFalse, true},
		{"still unhealthy", corev1.ConditionFalse, corev1.ConditionUnknown, false},
		{"recovery", corev1.ConditionUnknown, corev1.ConditionTrue, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := remediationNode(tc.before), remediationNode(tc.after)
			after.ResourceVersion = "2"
			harness.node.Update(before, after)
			if tc.enqueue {
				expectWatchRequest(t, harness.requests, client.ObjectKeyFromObject(pr))
			} else {
				expectNoWatchRequest(t, harness.requests)
			}
		})
	}
}

func TestWatchListFailureRecoversOnNextEvent(t *testing.T) {
	for _, watch := range []string{"PVC", "Node"} {
		t.Run(watch, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t)
			mustCreate(t, r.Client, pr)
			if err := r.DynamicClient.Resource(gvrSelfNodeRemediation).Namespace("test").Delete(ctx, "worker-0-snr", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			var failing atomic.Bool
			failing.Store(true)
			failures := make(chan struct{}, 1)
			r.Client = &apiFaultClient{Client: r.Client, before: func(_ context.Context, _ string, obj runtime.Object) error {
				if _, ok := obj.(*remediationv1.PodRemediatorList); ok && failing.Load() {
					select {
					case failures <- struct{}{}:
					default:
					}
					return apierrors.NewServiceUnavailable("watch cannot list remediators")
				}
				return nil
			}}
			harness := startWatchHarness(ctx, t, r)
			fire := func() {
				if watch == "PVC" {
					harness.pvc.Add(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "test", Name: "claim"}})
				} else {
					harness.node.Add(remediationNode(corev1.ConditionFalse))
				}
			}
			fire()
			select {
			case <-failures:
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not attempt to list remediators")
			}
			expectNoWatchRequest(t, harness.requests)
			failing.Store(false)
			fire()
			expectWatchRequest(t, harness.requests, client.ObjectKeyFromObject(pr))
		})
	}
}
