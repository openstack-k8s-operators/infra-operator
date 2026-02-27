/*
Copyright 2022.

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

package functional_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	k8s_networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"

	memcached_ctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/memcached"
	network_ctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/network"
	rabbitmq_ctrl "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"

	webhookmemcachedv1beta1 "github.com/openstack-k8s-operators/infra-operator/internal/webhook/memcached/v1beta1"
	webhooknetworkv1beta1 "github.com/openstack-k8s-operators/infra-operator/internal/webhook/network/v1beta1"
	webhookrabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/internal/webhook/rabbitmq/v1beta1"

	ocp_configv1 "github.com/openshift/api/config/v1"
	infra_test "github.com/openstack-k8s-operators/infra-operator/apis/test/helpers"
	test "github.com/openstack-k8s-operators/lib-common/modules/test"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg       *rest.Config
	k8sClient client.Client // You'll be using this client in your tests.
	dynClient *dynamic.DynamicClient
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
	logger    logr.Logger
	namespace string
	th        *infra_test.TestHelper
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true), func(o *zap.Options) {
		o.Development = true
		o.TimeEncoder = zapcore.ISO8601TimeEncoder
	}))

	ctx, cancel = context.WithCancel(context.TODO())

	rabbitmqv2CRDs, err := test.GetCRDDirFromModule(
		"github.com/rabbitmq/cluster-operator/v2", "../../go.mod", "config/crd/bases")
	Expect(err).ShouldNot(HaveOccurred())

	ocpconfigv1CRDs, err := test.GetOpenShiftCRDDir("config/v1", "../../go.mod")
	Expect(err).ShouldNot(HaveOccurred())

	frrCRDs, err := test.GetCRDDirFromModule(
		"github.com/metallb/frr-k8s", "../../go.mod", "config/crd/bases")
	Expect(err).ShouldNot(HaveOccurred())

	networkv1CRD, err := test.GetCRDDirFromModule(
		"github.com/k8snetworkplumbingwg/network-attachment-definition-client", "../../go.mod", "artifacts/networks-crd.yaml")
	Expect(err).ShouldNot(HaveOccurred())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		// Increase this to 60 or 120 seconds for the single-core run
		ControlPlaneStartTimeout: 120 * time.Second,
		// Give it plenty of time to wind down (e.g., 60-120 seconds)
		ControlPlaneStopTimeout: 120 * time.Second,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			ocpconfigv1CRDs,
			rabbitmqv2CRDs,
			frrCRDs,
		},
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{
				networkv1CRD,
			},
		},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "config", "webhook")},
			// NOTE(gibi): if localhost is resolved to ::1 (ipv6) then starting
			// the webhook fails as it try to parse the address as ipv4 and
			// failing on the colons in ::1
			LocalServingHost: "127.0.0.1",
		},
		ControlPlane: envtest.ControlPlane{
			APIServer: &envtest.APIServer{
				Args: []string{
					"--service-cluster-ip-range=10.0.0.0/12", // 65k+ IPs
					"--disable-admission-plugins=ResourceQuota,ServiceAccount,NamespaceLifecycle",
				},
			},
		},
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// NOTE: Need to add all API schemas our operator can own.
	// Keep this in synch with DNSMasqReconciler.SetupWithManager,
	// otherwise the reconciler loop will silently not start
	// in the test env.
	err = networkv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = rabbitmqv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = rabbitmqclusterv2.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = memcachedv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = k8s_networkv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = frrk8sv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = topologyv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = ocp_configv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = rabbitmqv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	logger = ctrl.Log.WithName("---Test---")

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
	th = infra_test.NewTestHelper(ctx, k8sClient, timeout, interval, logger)
	Expect(th).NotTo(BeNil())

	// Start the controller-manager if goroutine
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		// NOTE(gibi): disable metrics reporting in test to allow
		// parallel test execution. Otherwise each instance would like to
		// bind to the same port
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		WebhookServer: webhook.NewServer(
			webhook.Options{
				Host:    webhookInstallOptions.LocalServingHost,
				Port:    webhookInstallOptions.LocalServingPort,
				CertDir: webhookInstallOptions.LocalServingCertDir,
			}),
		LeaderElection: false,
	})
	Expect(err).ToNot(HaveOccurred())

	kclient, err := kubernetes.NewForConfig(cfg)
	Expect(err).ToNot(HaveOccurred(), "failed to create kclient")

	dynClient, err = dynamic.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(dynClient).NotTo(BeNil())

	err = webhooknetworkv1beta1.SetupNetConfigWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhooknetworkv1beta1.SetupIPSetWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhooknetworkv1beta1.SetupReservationWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhooknetworkv1beta1.SetupDNSMasqWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookmemcachedv1beta1.SetupMemcachedWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookrabbitmqv1beta1.SetupRabbitMqWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookrabbitmqv1beta1.SetupRabbitMQUserWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookrabbitmqv1beta1.SetupRabbitMQPolicyWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookrabbitmqv1beta1.SetupRabbitMQVhostWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())
	err = webhookrabbitmqv1beta1.SetupRabbitMQFederationWebhookWithManager(k8sManager)
	Expect(err).NotTo(HaveOccurred())

	err = (&network_ctrl.DNSMasqReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(context.Background(), k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&network_ctrl.DNSDataReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&network_ctrl.ServiceReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&network_ctrl.IPSetReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(context.Background(), k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&network_ctrl.BGPConfigurationReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(context.Background(), k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.TransportURLReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&memcached_ctrl.Reconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.Reconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.RabbitMQVhostReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.RabbitMQUserReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.RabbitMQPolicyReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&rabbitmq_ctrl.RabbitMQFederationReconciler{
		Client:  k8sManager.GetClient(),
		Scheme:  k8sManager.GetScheme(),
		Kclient: kclient,
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	th.CreateClusterNetworkConfig()

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = BeforeEach(func() {
	// NOTE(gibi): We need to create a unique namespace for each test run
	// as namespaces cannot be deleted in a locally running envtest. See
	// https://book.kubebuilder.io/reference/envtest.html#namespace-usage-limitation
	namespace = uuid.New().String()
	th.CreateNamespace(namespace)
	// We still request the delete of the Namespace to properly cleanup if
	// we run the test in an existing cluster.
	DeferCleanup(th.DeleteNamespace, namespace)
})
