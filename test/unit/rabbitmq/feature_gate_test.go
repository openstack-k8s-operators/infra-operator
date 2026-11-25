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

package rabbitmq_test

import (
	"context"
	"errors"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	remediationv1 "github.com/openstack-k8s-operators/infra-operator/apis/remediation/v1beta1"
	rabbitmqcontroller "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The PodRemediator CRD ships inside infra-operator, so it is installed in
// every deployment and its presence says nothing about whether anyone enabled
// PVC remediation. These tests drive the full Reconcile and check what it
// deploys, since the gate's whole purpose is to keep the startup gate, the
// serving label and the Service selector off clusters that are not using the
// feature.

// gateFixture builds the object graph a full RabbitMq reconcile needs. Any
// extra objects, such as a PodRemediator CR, are added on top.
func gateFixture(t *testing.T, fx *fakeExec, instance *rabbitmqv1beta1.RabbitMq, extra ...client.Object) (*rabbitmqcontroller.Reconciler, client.WithWatch) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, policyv1.AddToScheme, rbacv1.AddToScheme,
		configv1.AddToScheme, rabbitmqv1beta1.AddToScheme, remediationv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	// The CRD is always installed; only a CR decides whether the feature is on.
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{remediationv1.GroupVersion})
	mapper.Add(remediationv1.GroupVersion.WithKind("PodRemediator"), meta.RESTScopeNamespace)

	instance.Generation = 7
	instance.Spec.ContainerImage = "rabbitmq:4.2"
	instance.UID = types.UID("rabbitmq-uid")
	instance.Finalizers = []string{"openstack.org/rabbitmq"}
	instance.Status.OldCRCleaned = "True"

	objects := append([]client.Object{
		instance,
		replacementPVC(newPVCUID),
		replacementPod(testRequestID),
		clusterPod(testName+"-server-1", true),
		clusterPod(testName+"-server-2", true),
		&configv1.Network{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.NetworkStatus{
				ClusterNetwork: []configv1.ClusterNetworkEntry{{CIDR: "10.128.0.0/14"}},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-config-v1", Namespace: "kube-system"},
			Data:       map[string]string{"install-config": "{}"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testName + "-default-user", Namespace: testNS},
			Data:       map[string][]byte{"username": []byte("operator"), "password": []byte("test-password")},
		},
	}, extra...)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithStatusSubresource(&rabbitmqv1beta1.RabbitMq{}, &corev1.Pod{}).
		WithObjects(objects...).Build()
	return &rabbitmqcontroller.Reconciler{
		Client:               c,
		APIReader:            c,
		Kclient:              kubernetesfake.NewSimpleClientset(),
		Scheme:               scheme,
		PodCommandExecutor:   fx.run,
		ManagementHTTPClient: managementClientFor(fx),
	}, c
}

func reconcileOnce(t *testing.T, r *rabbitmqcontroller.Reconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName},
	})
	return err
}

func serverStatefulSet(t *testing.T, c client.WithWatch) *appsv1.StatefulSet {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: testNS, Name: testName + "-server"}, sts); err != nil {
		t.Fatalf("reading StatefulSet: %v", err)
	}
	return sts
}

func hasStartupGateContainer(sts *appsv1.StatefulSet) bool {
	for _, init := range sts.Spec.Template.Spec.InitContainers {
		if init.Name == "startup-gate" {
			return true
		}
	}
	return false
}

// TestReconcileWithoutPodRemediatorCRDeploysNoGate is the case the old CRD
// check got wrong. The CRD exists, as it always does, but nothing is
// configured, so none of the operator-dependent machinery may be deployed.
func TestReconcileWithoutPodRemediatorCRDeploysNoGate(t *testing.T) {
	for _, test := range []struct {
		name string
		crs  []client.Object
	}{
		{name: "no PodRemediator at all"},
		{
			name: "PodRemediator disabled",
			crs: []client.Object{&remediationv1.PodRemediator{
				ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: testNS},
				Spec:       remediationv1.PodRemediatorSpec{Disabled: true},
			}},
		},
		{
			name: "PodRemediator scoped elsewhere",
			crs: []client.Object{&remediationv1.PodRemediator{
				ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "other"},
				Spec:       remediationv1.PodRemediatorSpec{Namespaces: []string{"elsewhere"}},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			r, c := gateFixture(t, fx, instanceWith(3), test.crs...)

			if err := reconcileOnce(t, r); err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}

			sts := serverStatefulSet(t, c)
			if hasStartupGateContainer(sts) {
				t.Error("startup gate must not be deployed when no PodRemediator CR covers the namespace")
			}
			if _, ok := sts.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel]; ok {
				t.Error("pod template must not carry the serving label without remediation")
			}

			svc := &corev1.Service{}
			if err := c.Get(context.Background(),
				types.NamespacedName{Namespace: testNS, Name: testName}, svc); err != nil {
				t.Fatalf("reading client Service: %v", err)
			}
			if _, ok := svc.Spec.Selector[rabbitmqv1beta1.RabbitMQServingLabel]; ok {
				t.Error("client Service must not select on serving without remediation")
			}

			// Pre-existing pods are left alone rather than being labelled.
			peer := &corev1.Pod{}
			if err := c.Get(context.Background(),
				types.NamespacedName{Namespace: testNS, Name: testName + "-server-1"}, peer); err != nil {
				t.Fatal(err)
			}
			if peer.Annotations[rabbitmqv1beta1.AnnotationStartupApproved] != "" {
				t.Error("no startup approval should be written without remediation")
			}
		})
	}
}

// TestReconcileWithPodRemediatorCRDeploysGate is the positive control: the
// same fixture with a covering CR must deploy the machinery, so the test above
// is not passing for an unrelated reason.
func TestReconcileWithPodRemediatorCRDeploysGate(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	r, c := gateFixture(t, fx, instanceWith(3), enabledPodRemediator())

	if err := reconcileOnce(t, r); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	sts := serverStatefulSet(t, c)
	if !hasStartupGateContainer(sts) {
		t.Error("a covering PodRemediator CR must deploy the startup gate")
	}
	if sts.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel] != rabbitmqv1beta1.RabbitMQServingFalse {
		t.Errorf("new pods must start serving=false, labels=%v", sts.Spec.Template.Labels)
	}
}

// disabledPodRemediator is a PodRemediator for testNS that has been switched
// off. PodRemediator still finishes a PVC deletion it had already committed
// to in this state.
func disabledPodRemediator() *remediationv1.PodRemediator {
	pr := enabledPodRemediator()
	pr.Spec.Disabled = true
	return pr
}

// deletingPodRemediator is a PodRemediator for testNS that is being deleted.
// Its own controller holds the finalizer until every committed PVC deletion
// has finished, so the CR stays visible for exactly as long as one can still
// be running.
func deletingPodRemediator() *remediationv1.PodRemediator {
	pr := enabledPodRemediator()
	pr.Finalizers = []string{"openstack.org/podremediator"}
	pr.DeletionTimestamp = ptr.To(metav1.NewTime(time.Now()))
	return pr
}

func clientService(t *testing.T, c client.WithWatch) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: testNS, Name: testName}, svc); err != nil {
		t.Fatalf("reading client Service: %v", err)
	}
	return svc
}

// TestRemediationInProgressKeepsGateWhenCRGoesAway is the unsafe window.
// PodRemediator goes through with a PVC deletion it has already committed to
// even after its CR is disabled or deleted. If RabbitMQ read either state as
// "feature off" it would drop the startup gate and the Services' serving
// requirement, and the replacement broker - whose data directory is empty -
// would start and take client traffic while the member it replaces is still
// in the cluster.
func TestRemediationInProgressKeepsGateWhenCRGoesAway(t *testing.T) {
	for _, test := range []struct {
		name string
		cr   *remediationv1.PodRemediator
	}{
		{name: "PodRemediator disabled mid-recovery", cr: disabledPodRemediator()},
		{name: "PodRemediator deleted mid-recovery", cr: deletingPodRemediator()},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := newFakeExecWithQuorumQueues(map[string][]string{
				testName + "-server-0": {node(testName + "-server-0")},
				testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
				testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
			})
			modelRemovedSeed(fx)

			// A replacement is mid-recovery: consent was given and the old
			// member is already out of the cluster.
			r, c := gateFixture(t, fx, instanceAfterMemberRemoval(3), test.cr)

			// Rejoin is still running, so a requeue or a deferral is expected
			// here; what matters is what was deployed.
			_ = reconcileOnce(t, r)

			sts := serverStatefulSet(t, c)
			if !hasStartupGateContainer(sts) {
				t.Error("the startup gate must stay while a committed deletion can still complete")
			}
			if got := sts.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel]; got != rabbitmqv1beta1.RabbitMQServingFalse {
				t.Errorf("replacement pods must still start outside client Services, serving=%q", got)
			}
			if got := clientService(t, c).Spec.Selector[rabbitmqv1beta1.RabbitMQServingLabel]; got != rabbitmqv1beta1.RabbitMQServingTrue {
				t.Errorf("the client Service must still require serving=true, selector value %q", got)
			}
			if len(fx.calls) == 0 {
				t.Error("rejoin must keep running so the replacement can be brought back safely")
			}
		})
	}
}

// TestDisabledPodRemediatorGrantsNoNewConsent checks the other half of the
// split: a remediation already under way continues, but no further PVC may be
// marked safe to delete while the CR is switched off.
func TestDisabledPodRemediatorGrantsNoNewConsent(t *testing.T) {
	ctx := context.Background()
	r, instance, h := newConsentFixture(t, testPVCName)

	err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h,
		rabbitmqcontroller.RemediationDecision{AllowNewConsent: false, RecoveryInFlight: true})
	if err != nil {
		t.Fatalf("status bookkeeping must still run: %v", err)
	}

	claim := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testPVCName}, claim); err != nil {
		t.Fatal(err)
	}
	if remediationv1.HasRemediationConsent(claim.Annotations) {
		t.Error("no new PVC may be marked safe to delete while PodRemediator is switched off")
	}
}

// TestRecoveryRecordClearsWhileDisabled guards against the recovery record
// latching on. Clearing a finished record is the only way RecoveryInFlight
// ever goes back to false, and it happens in the same pass that grants new
// consent. If that pass were skipped whenever new consent is not allowed, a
// cluster disabled mid-recovery would keep its record, and the record would
// keep the operator-dependent startup gate, forever.
func TestRecoveryRecordClearsWhileDisabled(t *testing.T) {
	ctx := context.Background()
	r, instance, h := newConsentFixture(t, testPVCName)
	instance.Status.PVCRemediation = instanceWithRecovery(3).Status.PVCRemediation

	// The replacement claim is in place under a new UID and its pod is back in
	// the cluster, which is what finishes the recovery.
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testPVCName}, claim); err != nil {
		t.Fatal(err)
	}
	claim.UID = types.UID(newPVCUID)
	claim.Annotations = nil
	if err := r.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: testNS, Name: testName + "-server-0"}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.Volumes = []corev1.Volume{{Name: "persistence", VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: testPVCName},
	}}}
	if err := r.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}

	if err := r.CheckForStuckPVCRequiringRemediation(ctx, instance, h,
		rabbitmqcontroller.RemediationDecision{AllowNewConsent: false, RecoveryInFlight: true}); err != nil {
		t.Fatalf("finished recovery should not defer: %v", err)
	}
	if len(instance.Status.PVCRemediation) != 0 {
		t.Fatalf("a finished recovery must clear its record even while disabled, got %v",
			instance.Status.PVCRemediation)
	}
}

// TestDisablingWithNoRecoveryRestoresPlainStartup is the way back. With no
// remediation under way, switching PodRemediator off has to return the cluster
// to broker startup and client admission that need no running operator.
func TestDisablingWithNoRecoveryRestoresPlainStartup(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	r, c := gateFixture(t, fx, instanceWith(3), enabledPodRemediator())

	if err := reconcileOnce(t, r); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}
	if !hasStartupGateContainer(serverStatefulSet(t, c)) {
		t.Fatal("fixture precondition: the gate should be deployed while enabled")
	}

	pr := &remediationv1.PodRemediator{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: testNS, Name: "podremediator"}, pr); err != nil {
		t.Fatal(err)
	}
	pr.Spec.Disabled = true
	if err := c.Update(context.Background(), pr); err != nil {
		t.Fatal(err)
	}

	if err := reconcileOnce(t, r); err != nil {
		t.Fatalf("reconcile after disabling failed: %v", err)
	}
	sts := serverStatefulSet(t, c)
	if hasStartupGateContainer(sts) {
		t.Error("with nothing in flight, disabling must remove the startup gate")
	}
	if _, ok := sts.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel]; ok {
		t.Error("with nothing in flight, disabling must drop the serving label from the pod template")
	}
	if _, ok := clientService(t, c).Spec.Selector[rabbitmqv1beta1.RabbitMQServingLabel]; ok {
		t.Error("with nothing in flight, disabling must drop the serving requirement from the client Service")
	}
}

// TestStaleRecoveryStatusDoesNotRejoinWhenInactive covers the destructive
// path. A recovery record left behind by a remediation that cannot still be
// running - its PodRemediator is gone outright, finalizer and all - must not
// drive forget_cluster_node, join_cluster or any exec.
func TestStaleRecoveryStatusDoesNotRejoinWhenInactive(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-0": {node(testName + "-server-0")},
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	modelRemovedSeed(fx)

	// Status says a remediation is mid-flight, but no PodRemediator CR exists.
	stale := instanceAfterMemberRemoval(3)
	r, c := gateFixture(t, fx, stale)

	if err := reconcileOnce(t, r); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(fx.calls) != 0 {
		t.Fatalf("a stale record must not exec in any broker once remediation is inactive, calls=%v", fx.calls)
	}

	persisted := &rabbitmqv1beta1.RabbitMq{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: testNS, Name: testName}, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.PVCRemediation[testPVCName].Phase == rabbitmqv1beta1.PVCRemediationRecovered {
		t.Error("an inactive feature must not advance a recovery to Recovered")
	}
}

// TestPodRemediatorLookupErrorDoesNotRollStatefulSet covers the rollout
// hazard: reading a transient API failure as "feature disabled" would strip
// the startup gate from a cluster using it and restart every broker.
func TestPodRemediatorLookupErrorDoesNotRollStatefulSet(t *testing.T) {
	fx := newFakeExecWithQuorumQueues(map[string][]string{
		testName + "-server-1": {node(testName + "-server-1"), node(testName + "-server-2")},
		testName + "-server-2": {node(testName + "-server-1"), node(testName + "-server-2")},
	})
	r, c := gateFixture(t, fx, instanceWith(3), enabledPodRemediator())

	// Converge once with the feature enabled so the gate is deployed.
	if err := reconcileOnce(t, r); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}
	before := serverStatefulSet(t, c)
	if !hasStartupGateContainer(before) {
		t.Fatal("fixture precondition: the gate should be deployed before the failure")
	}

	// Now make the PodRemediator lookup fail transiently.
	r.Client = interceptor.NewClient(c, interceptor.Funcs{
		List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*remediationv1.PodRemediatorList); ok {
				return errors.New("etcdserver: request timed out")
			}
			return delegate.List(ctx, list, opts...)
		},
	})
	if err := reconcileOnce(t, r); err == nil {
		t.Fatal("a failed PodRemediator lookup must fail the reconcile, not disable the feature")
	}

	after := serverStatefulSet(t, c)
	if !hasStartupGateContainer(after) {
		t.Error("a transient lookup failure must not remove the startup gate")
	}
	if after.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel] !=
		before.Spec.Template.Labels[rabbitmqv1beta1.RabbitMQServingLabel] {
		t.Error("a transient lookup failure must not change the serving label on the pod template")
	}
}
