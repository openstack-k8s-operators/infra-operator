/*
Copyright 2023.

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

package main

import (
	"flag"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	keystonev1 "github.com/openstack-k8s-operators/keystone-operator/api/v1beta1"
	rabbitmqclusterv1 "github.com/rabbitmq/cluster-operator/api/v1beta1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	clientv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/client/v1beta1"
	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	clientcontrollers "github.com/openstack-k8s-operators/infra-operator/controllers/client"
	memcachedcontrollers "github.com/openstack-k8s-operators/infra-operator/controllers/memcached"
	rabbitmqcontrollers "github.com/openstack-k8s-operators/infra-operator/controllers/rabbitmq"
	rediscontrollers "github.com/openstack-k8s-operators/infra-operator/controllers/redis"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(rabbitmqv1beta1.AddToScheme(scheme))
	utilruntime.Must(rabbitmqclusterv1.AddToScheme(scheme))
	utilruntime.Must(clientv1beta1.AddToScheme(scheme))
	utilruntime.Must(keystonev1.AddToScheme(scheme))
	utilruntime.Must(memcachedv1.AddToScheme(scheme))
	utilruntime.Must(redisv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "c8c223a1.openstack.org",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cfg, err := config.GetConfig()
	if err != nil {
		setupLog.Error(err, "")
		os.Exit(1)
	}
	kclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "")
		os.Exit(1)
	}

	if err = (&rabbitmqcontrollers.TransportURLReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Kclient: kclient,
		Log:     ctrl.Log.WithName("controllers").WithName("OpenStackClient"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TransportURL")
		os.Exit(1)
	}
	if err = (&clientcontrollers.OpenStackClientReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Kclient: kclient,
		Log:     ctrl.Log.WithName("controllers").WithName("OpenStackClient"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "OpenStackClient")
		os.Exit(1)
	}
	if err = (&memcachedcontrollers.Reconciler{
		Client:  mgr.GetClient(),
		Kclient: kclient,
		Log:     ctrl.Log.WithName("controllers").WithName("Memcached"),
		Scheme:  mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Memcached")
		os.Exit(1)
	}
	if err = (&rediscontrollers.Reconciler{
		Client:  mgr.GetClient(),
		Kclient: kclient,
		Log:     ctrl.Log.WithName("controllers").WithName("Redis"),
		Scheme:  mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Redis")
		os.Exit(1)
	}

	// Acquire environmental defaults and initialize OpenStackClient defaults with them
	openStackClientDefaults := clientv1beta1.OpenStackClientDefaults{
		ContainerImageURL: os.Getenv("INFRA_CLIENT_IMAGE_URL_DEFAULT"),
	}

	clientv1beta1.SetupOpenStackClientDefaults(openStackClientDefaults)

	// Acquire environmental defaults and initialize Memcached defaults with them
	memcachedDefaults := memcachedv1.MemcachedDefaults{
		ContainerImageURL: os.Getenv("INFRA_MEMCACHED_IMAGE_URL_DEFAULT"),
	}

	memcachedv1.SetupMemcachedDefaults(memcachedDefaults)

	// Acquire environmental defaults and initialize Redis defaults with them
	redisDefaults := redisv1.RedisDefaults{
		ContainerImageURL: os.Getenv("INFRA_REDIS_IMAGE_URL_DEFAULT"),
	}

	redisv1.SetupRedisDefaults(redisDefaults)

	// Setup webhooks if requested
	if strings.ToLower(os.Getenv("ENABLE_WEBHOOKS")) != "false" {
		if err = (&clientv1beta1.OpenStackClient{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "OpenStackClient")
			os.Exit(1)
		}
		if err = (&memcachedv1.Memcached{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Memcached")
			os.Exit(1)
		}
		if err = (&redisv1.Redis{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Redis")
			os.Exit(1)
		}
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
