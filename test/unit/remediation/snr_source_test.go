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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestSNRRemediationChanged verifies the SNR event predicate.
func TestSNRRemediationChanged(t *testing.T) {
	original := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"phase": "Pre-Reboot-Completed"},
	}}
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		want   bool
	}{
		{"unchanged", func(*unstructured.Unstructured) {}, false},
		{"unrelated metadata", func(o *unstructured.Unstructured) {
			o.SetResourceVersion("2")
			o.SetAnnotations(map[string]string{"other": "value"})
		}, false},
		{"unrelated status", func(o *unstructured.Unstructured) {
			o.Object["status"].(map[string]interface{})["timeAssumedRebooted"] = "elapsed"
		}, false},
		{"reboot complete", func(o *unstructured.Unstructured) {
			o.Object["status"].(map[string]interface{})["phase"] = "Reboot-Completed"
		}, true},
		{"fencing complete", func(o *unstructured.Unstructured) {
			o.Object["status"].(map[string]interface{})["phase"] = "Fencing-Completed"
		}, true},
		{"phase removed", func(o *unstructured.Unstructured) { delete(o.Object, "status") }, true},
		{"terminating", func(o *unstructured.Unstructured) { now := metav1.Now(); o.SetDeletionTimestamp(&now) }, true},
		{"node annotation", func(o *unstructured.Unstructured) {
			o.SetAnnotations(map[string]string{"remediation.medik8s.io/node-name": "worker-0"})
		}, true},
		{"node label", func(o *unstructured.Unstructured) {
			o.SetLabels(map[string]string{"remediation.medik8s.io/node-name": "worker-0"})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r, pr := remediationFixture(t)
			if err := r.Create(ctx, pr); err != nil {
				t.Fatal(err)
			}
			dyn := r.DynamicClient.(*dynamicfake.FakeDynamicClient)
			resource := dyn.Resource(gvrSelfNodeRemediation).Namespace("test")
			snr, err := resource.Get(ctx, "worker-0-snr", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			snr.Object["status"] = original.Object["status"]
			snr.SetAnnotations(nil)
			snr.SetLabels(nil)
			if _, err := resource.Update(ctx, snr, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			watcher := watch.NewRaceFreeFake()
			defer watcher.Stop()
			watching := make(chan struct{}, 1)
			dyn.PrependWatchReactor("selfnoderemediations", func(clienttesting.Action) (bool, watch.Interface, error) {
				select {
				case watching <- struct{}{}:
				default:
				}
				return true, watcher, nil
			})
			harness := startWatchHarness(ctx, t, r)
			key := client.ObjectKeyFromObject(pr)
			expectWatchRequest(t, harness.requests, key)
			select {
			case <-watching:
			case <-time.After(5 * time.Second):
				t.Fatal("SNR watch did not start")
			}
			updated := snr.DeepCopy()
			tc.change(updated)
			watcher.Modify(updated)
			if tc.want {
				expectWatchRequest(t, harness.requests, key)
			} else {
				expectNoWatchRequest(t, harness.requests)
			}
		})
	}
}

// TestSNRSourceConnectsAfterAPIInstalled verifies the optional informer retry path.
func TestSNRSourceConnectsAfterAPIInstalled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, pr := remediationFixture(t)
	if err := r.Create(ctx, pr); err != nil {
		t.Fatal(err)
	}
	dyn := r.DynamicClient.(*dynamicfake.FakeDynamicClient)
	var installed atomic.Bool
	attempted := make(chan struct{}, 1)
	dyn.PrependReactor("list", "selfnoderemediations", func(clienttesting.Action) (bool, runtime.Object, error) {
		if installed.Load() {
			return false, nil, nil
		}
		select {
		case attempted <- struct{}{}:
		default:
		}
		return true, nil, apierrors.NewNotFound(gvrSelfNodeRemediation.GroupResource(), "")
	})
	watcher := watch.NewRaceFreeFake()
	defer watcher.Stop()
	watching := make(chan struct{}, 1)
	dyn.PrependWatchReactor("selfnoderemediations", func(clienttesting.Action) (bool, watch.Interface, error) {
		select {
		case watching <- struct{}{}:
		default:
		}
		return true, watcher, nil
	})
	harness := startWatchHarness(ctx, t, r)
	select {
	case <-attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("optional API was never checked")
	}
	expectNoWatchRequest(t, harness.requests)
	installed.Store(true)
	expectEvent := func() {
		t.Helper()
		expectWatchRequest(t, harness.requests, client.ObjectKeyFromObject(pr))
	}
	// The initial list after installation must also trigger reconciliation.
	expectEvent()
	select {
	case <-watching:
	case <-time.After(5 * time.Second):
		t.Fatal("SNR watch did not start")
	}
	snr, err := dyn.Resource(gvrSelfNodeRemediation).Namespace("test").Get(ctx, "worker-0-snr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	updated := snr.DeepCopy()
	if err := unstructured.SetNestedField(updated.Object, "Reboot-Completed", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	watcher.Modify(updated)
	expectEvent()
	watcher.Delete(updated)
	expectEvent()
	watcher.Add(updated)
	expectEvent()
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !watcher.IsStopped() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !watcher.IsStopped() {
		t.Fatal("SNR watch survived controller shutdown")
	}
}
