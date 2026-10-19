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
	"fmt"
	"testing"

	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestConsentAnnotationCombinations(t *testing.T) {
	for _, request := range []string{"", "current-request"} {
		for _, consent := range []string{"", "current-request", "old-request"} {
			for _, safe := range []string{"", "true", "false", "True", "1"} {
				t.Run("request="+request+"/consent="+consent+"/safe="+safe, func(t *testing.T) {
					annotations := map[string]string{}
					for key, value := range map[string]string{
						remediationv1.RequestIDAnnotation:    request,
						remediationv1.ConsentIDAnnotation:    consent,
						remediationv1.SafeToDeleteAnnotation: safe,
					} {
						if value != "" {
							annotations[key] = value
						}
					}
					want := request == "current-request" && consent == "current-request" && safe == "true"
					if got := remediationv1.HasRemediationConsent(annotations); got != want {
						t.Fatalf("consent = %t, want %t for %v", got, want, annotations)
					}
				})
			}
		}
	}
	if remediationv1.HasRemediationConsent(nil) {
		t.Fatal("nil annotations authorized deletion")
	}
}

func TestInvalidConsentNeverStartsDeletion(t *testing.T) {
	for _, change := range []struct{ name, key, value string }{
		{"missing request", remediationv1.RequestIDAnnotation, ""},
		{"missing consent ID", remediationv1.ConsentIDAnnotation, ""},
		{"stale consent ID", remediationv1.ConsentIDAnnotation, "old-request"},
		{"missing permission", remediationv1.SafeToDeleteAnnotation, ""},
		{"withdrawn permission", remediationv1.SafeToDeleteAnnotation, "false"},
		{"wrong case", remediationv1.SafeToDeleteAnnotation, "True"},
		{"numeric permission", remediationv1.SafeToDeleteAnnotation, "1"},
		{"foreign owner", remediationv1.RemediatorUIDAnnotation, "another-remediator"},
		{"stale node identity", remediationv1.FencingNodeUIDAnnotation, "old-node"},
	} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
			pvc := &corev1.PersistentVolumeClaim{}
			key := client.ObjectKey{Namespace: "test", Name: "claim"}
			if err := r.Get(ctx, key, pvc); err != nil {
				t.Fatal(err)
			}
			if change.value == "" {
				delete(pvc.Annotations, change.key)
			} else {
				pvc.Annotations[change.key] = change.value
			}
			if err := r.Update(ctx, pvc); err != nil {
				t.Fatal(err)
			}
			pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
			mustCreate(t, r.Client, pod)
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := reconcileRemediation(ctx, r, pr); err != nil {
					t.Fatal(err)
				}
				assertObjectAbsent(t, r.Client, pod, false)
				if err := r.Get(ctx, key, pvc); err != nil {
					t.Fatal(err)
				}
				if !pvc.DeletionTimestamp.IsZero() || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
					t.Fatal("invalid consent authorized deletion")
				}
				commits := &corev1.ConfigMapList{}
				if err := r.List(ctx, commits); err != nil {
					t.Fatal(err)
				}
				if len(commits.Items) != 0 {
					t.Fatal("invalid consent created a deletion record")
				}
			}
		})
	}
}

func TestUnsafePVShapesNeverAuthorizeDeletion(t *testing.T) {
	for _, shape := range []string{"unbound", "missing PV", "network volume", "no affinity", "field selector", "multiple direct expressions"} {
		for _, missingNode := range []bool{false, true} {
			for _, consented := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/missingNode=%t/consented=%t", shape, missingNode, consented), func(t *testing.T) {
					ctx := context.Background()
					r, pr := remediationFixture(t, remediationNode(corev1.ConditionFalse))
					if missingNode {
						if err := r.Delete(ctx, remediationNode(corev1.ConditionFalse)); err != nil {
							t.Fatal(err)
						}
					}
					pvc := &corev1.PersistentVolumeClaim{}
					key := client.ObjectKey{Namespace: "test", Name: "claim"}
					if err := r.Get(ctx, key, pvc); err != nil {
						t.Fatal(err)
					}
					if !consented {
						pvc.Annotations = nil
					}
					if shape == "unbound" {
						pvc.Spec.VolumeName = ""
					}
					if err := r.Update(ctx, pvc); err != nil {
						t.Fatal(err)
					}
					pv := &corev1.PersistentVolume{}
					if err := r.Get(ctx, client.ObjectKey{Name: "pv"}, pv); err != nil {
						t.Fatal(err)
					}
					pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Key = "topology.topolvm.io/node"
					switch shape {
					case "network volume":
						pv.Spec.PersistentVolumeSource = corev1.PersistentVolumeSource{NFS: &corev1.NFSVolumeSource{Server: "storage", Path: "/share"}}
					case "no affinity":
						pv.Spec.NodeAffinity = nil
					case "field selector":
						pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchFields = []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker-0"}}}
					case "multiple direct expressions":
						pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions = append(pv.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions, corev1.NodeSelectorRequirement{Key: "test.example/unknown", Operator: corev1.NodeSelectorOpIn, Values: []string{"value"}})
					}
					if shape == "missing PV" {
						if err := r.Delete(ctx, pv); err != nil {
							t.Fatal(err)
						}
					} else if err := r.Update(ctx, pv); err != nil {
						t.Fatal(err)
					}
					pod := podUsingClaim("pod", "worker-0", "pod-uid", nil)
					mustCreate(t, r.Client, pod)
					if _, err := reconcileRemediation(ctx, r, pr); err != nil {
						t.Fatal(err)
					}
					assertObjectAbsent(t, r.Client, pod, false)
					if err := r.Get(ctx, key, pvc); err != nil {
						t.Fatal(err)
					}
					if !pvc.DeletionTimestamp.IsZero() || pvc.Annotations[deletionCommitFinalizedAnnotation] != "" {
						t.Fatal("unsupported PV authorized deletion")
					}
				})
			}
		}
	}
}
