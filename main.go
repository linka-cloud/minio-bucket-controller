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
	"flag"
	"os"
	"strings"

	"go.linka.cloud/grpc/logger"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	"go.linka.cloud/minio-bucket-controller/controllers"
	mc2 "go.linka.cloud/minio-bucket-controller/pkg/mc"
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
		metricsAddr                    string
		enableLeaderElection           bool
		probeAddr                      string
		endpoint, accessKey, secretKey string
		insecure                       bool
		certs                          string
	)
	flag.StringVar(&endpoint, "endpoint", os.Getenv(s3v1alpha1.MinioEndpoint), "The Minio endpoint [$"+s3v1alpha1.MinioEndpoint+"]")
	flag.StringVar(&accessKey, "access-key", os.Getenv(s3v1alpha1.MinioAccessKey), "The Minio access key [$"+s3v1alpha1.MinioAccessKey+"]")
	flag.StringVar(&secretKey, "secret-key", os.Getenv(s3v1alpha1.MinioSecretKey), "The Minio secret key [$"+s3v1alpha1.MinioSecretKey+"]")
	flag.BoolVar(&insecure, "insecure", os.Getenv("MINIO_INSECURE") != "", "Whether to use insecure connection [$MINIO_INSECURE]")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&certs, "certs", "", "The directory where the TLS certs are stored")
	flag.Parse()

	ctrl.SetLogger(logger.StandardLogger().Logr())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc, err := mc2.New(endpoint, accessKey, secretKey, !insecure)
	if err != nil {
		setupLog.Error(err, "unable to create minio client")
		os.Exit(1)
	}

	cfg := ctrl.GetConfigOrDie()
	var serviceAccount string
	switch {
	case cfg.CertData != nil:
		b, _ := pem.Decode(cfg.CertData)
		if b == nil {
			setupLog.Error(err, "unable to decode cert data")
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
			setupLog.Error(err, "unable to parse bearer token")
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
	default:
		setupLog.Error(err, "unable to find service account")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		CertDir:                certs,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "58e2baca.linka.cloud",
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

	br := &controllers.BucketReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		MC:     mc,
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
		MC:             mc,
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
