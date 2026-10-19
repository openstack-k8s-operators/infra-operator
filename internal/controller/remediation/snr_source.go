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

package remediation

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// snrSource is deliberately not a SyncingSource: SNR is optional and its CRD may
// be installed after the manager starts. A separate dynamic informer retries
// list/watch without blocking the manager cache or controller workers. Events
// only request a scan; reconciliation still reads live SNR fencing evidence.
type snrSource struct {
	reconciler *PodRemediatorReconciler
}

var _ source.Source = &snrSource{}

// Start installs the optional SNR informer and maps relevant events to scans.
func (s *snrSource) Start(ctx context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	r := s.reconciler
	if r.DynamicClient == nil {
		return fmt.Errorf("SNR watch requires a dynamic client")
	}
	logger := r.GetLogger(ctx)
	informer := dynamicinformer.NewFilteredDynamicInformer(r.DynamicClient,
		gvrSelfNodeRemediation, metav1.NamespaceAll, 0, cache.Indexers{}, nil).Informer()
	enqueue := func() {
		for _, request := range r.enqueuePodRemediatorsClusterWide(ctx, logger) {
			queue.Add(request)
		}
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(interface{}) { enqueue() },
		UpdateFunc: func(old, current interface{}) {
			if snrRemediationChanged(old, current) {
				enqueue()
			}
		},
		// The object may be a DeletedFinalStateUnknown tombstone; all CRs need
		// a scan regardless, so no object cast is needed here.
		DeleteFunc: func(interface{}) { enqueue() },
	}); err != nil {
		return err
	}
	if err := informer.SetWatchErrorHandlerWithContext(func(ctx context.Context, _ *cache.Reflector, err error) {
		if ctx.Err() != nil {
			return
		}
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("SNR API is not installed; retrying optional watch")
			return
		}
		logger.Error(err, "SNR watch interrupted; retrying with polling as fallback")
	}); err != nil {
		return err
	}
	go informer.RunWithContext(ctx)
	return nil
}

// snrRemediationChanged ignores unrelated status/metadata changes, but includes node identity and
// termination changes because both affect which fencing evidence is valid.
func snrRemediationChanged(old, current interface{}) bool {
	oldSNR, oldOK := old.(*unstructured.Unstructured)
	newSNR, newOK := current.(*unstructured.Unstructured)
	if !oldOK || !newOK {
		return true
	}
	oldPhase, _, _ := unstructured.NestedString(oldSNR.Object, "status", "phase")
	newPhase, _, _ := unstructured.NestedString(newSNR.Object, "status", "phase")
	const nodeKey = "remediation.medik8s.io/node-name"
	return oldPhase != newPhase ||
		!oldSNR.GetDeletionTimestamp().Equal(newSNR.GetDeletionTimestamp()) ||
		oldSNR.GetAnnotations()[nodeKey] != newSNR.GetAnnotations()[nodeKey] ||
		oldSNR.GetLabels()[nodeKey] != newSNR.GetLabels()[nodeKey]
}
