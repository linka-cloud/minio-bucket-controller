// Copyright 2023 Linka Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"go.linka.cloud/grpc-toolkit/logger"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	"go.linka.cloud/minio-bucket-controller/controllers"
	"go.linka.cloud/minio-bucket-controller/pkg/recorder"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = logger.StandardLogger().Logr().WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(s3v1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var (
		metricsAddr          string
		enableLeaderElection bool
		probeAddr            string
		certs                string
		serviceAccount       string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&certs, "certs", "", "The directory where the TLS certs are stored")
	flag.StringVar(&serviceAccount, "service-account", "", "The service account name (if not set, will be extracted from the client cert or token)")
	flag.Parse()

	ctrl.SetLogger(logger.StandardLogger().Logr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := ctrl.GetConfigOrDie()
	switch {
	case cfg.CertData != nil:
		b, _ := pem.Decode(cfg.CertData)
		if b == nil {
			setupLog.Error(errors.New("invalid cert"), "unable to decode cert data")
			os.Exit(1)
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			setupLog.Error(err, "unable to parse cert")
			os.Exit(1)
		}
		serviceAccount = cert.Subject.CommonName
	case cfg.BearerToken != "":
		parts := strings.Split(cfg.BearerToken, ".")
		if len(parts) != 3 {
			setupLog.Error(errors.New("invalid bearer token"), "unable to parse bearer token")
			os.Exit(1)
		}
		b, err := base64.RawStdEncoding.DecodeString(parts[1])
		if err != nil {
			setupLog.Error(err, "unable to decode bearer token", "token", parts[1])
			os.Exit(1)
		}

		h := &struct {
			Subject string `json:"sub"`
		}{}
		if err := json.Unmarshal(b, h); err != nil {
			setupLog.Error(err, "unable to parse bearer token")
			os.Exit(1)
		}
		serviceAccount = h.Subject
	case serviceAccount != "":
		// use provided service account
	default:
		setupLog.Error(errors.New("service account missing"), "unable to find service account")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme,
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "58e2baca.linka.cloud",
		LeaderElectionReleaseOnCancel: true,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    9443,
			CertDir: certs,
		}),
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	br := &controllers.BucketReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Rec:    recorder.New(mgr.GetEventRecorderFor("bucket-controller")),
	}

	if err = br.SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Bucket")
		os.Exit(1)
	}
	if err = br.SetupWebhookWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "Bucket")
		os.Exit(1)
	}
	bsar := &controllers.BucketServiceAccountReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Rec:            recorder.New(mgr.GetEventRecorderFor("bucket-service-account-controller")),
		ServiceAccount: serviceAccount,
	}
	if err = bsar.SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BucketServiceAccount")
		os.Exit(1)
	}
	if err = bsar.SetupWebhookWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "BucketServiceAccount")
		os.Exit(1)
	}
	bpr := &controllers.BucketProviderReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Rec:    recorder.New(mgr.GetEventRecorderFor("bucket-provider-controller")),
	}
	if err = bpr.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BucketProvider")
		os.Exit(1)
	}
	if err = bpr.SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "BucketProvider")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

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
