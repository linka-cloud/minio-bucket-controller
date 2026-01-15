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

package controllers

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.linka.cloud/grpc/logger"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"dagger.io/dagger"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	"go.linka.cloud/minio-bucket-controller/pkg/recorder"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	dag       *dagger.Client
	endpoint  = "127.0.0.1:9000"

	provider = &s3v1alpha1.BucketProvider{
		ObjectMeta: ctrl.ObjectMeta{
			Name: "minio-provider",
		},
		Spec: s3v1alpha1.BucketProviderSpec{
			Endpoint: endpoint,
			AccessKey: s3v1alpha1.SecretRef{
				Name:      "minio-credentials",
				Namespace: "default",
				Key:       "accesskey",
			},
			SecretKey: s3v1alpha1.SecretRef{
				Name:      "minio-credentials",
				Namespace: "default",
				Key:       "secretkey",
			},
			Insecure: true,
		},
	}

	secret = &corev1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      "minio-credentials",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"accesskey": []byte("minioadmin"),
			"secretkey": []byte("minioadmin"),
		},
	}
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(logger.StandardLogger().Logr())

	ctx, cancel = context.WithCancel(context.TODO())

	Expect(s3v1alpha1.AddToScheme(scheme.Scheme)).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "config", "webhook")},
		},
	}

	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = s3v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                        scheme.Scheme,
		Host:                          testEnv.WebhookInstallOptions.LocalServingHost,
		Port:                          testEnv.WebhookInstallOptions.LocalServingPort,
		CertDir:                       testEnv.WebhookInstallOptions.LocalServingCertDir,
		LeaderElectionID:              "58e2baca.linka.cloud",
		LeaderElectionReleaseOnCancel: true,
	})
	Expect(err).ToNot(HaveOccurred())

	br := &BucketReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Rec:    recorder.New(mgr.GetEventRecorderFor("bucket-controller")),
	}
	Expect(br.SetupWithManager(ctx, mgr)).ToNot(HaveOccurred())
	Expect(br.SetupWebhookWithManager(ctx, mgr)).ToNot(HaveOccurred())

	bsar := &BucketServiceAccountReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Rec:            recorder.New(mgr.GetEventRecorderFor("bucket-service-account-controller")),
		ServiceAccount: "admin",
	}
	Expect(bsar.SetupWithManager(ctx, mgr)).ToNot(HaveOccurred())
	Expect(bsar.SetupWebhookWithManager(ctx, mgr)).ToNot(HaveOccurred())

	bpr := &BucketProviderReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Rec:    recorder.New(mgr.GetEventRecorderFor("bucket-provider-controller")),
	}
	Expect(bpr.SetupWithManager(mgr)).ToNot(HaveOccurred())
	Expect(bpr.SetupWebhookWithManager(mgr)).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()

	Expect(err).NotTo(HaveOccurred())
	go func() {
		defer GinkgoRecover()
		dag, err = dagger.Connect(ctx)
		Expect(err).NotTo(HaveOccurred())
		dag.Container().
			From("minio/minio:RELEASE.2025-09-07T16-13-09Z").
			WithExposedPort(9000).
			AsService(dagger.ContainerAsServiceOpts{Args: []string{"minio", "server", "/data"}}).
			Up(ctx, dagger.ServiceUpOpts{Ports: []dagger.PortForward{{Backend: 9000, Frontend: 9000}}})
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	Expect(waitForPort(ctx, endpoint)).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Expect(dag.Close()).NotTo(HaveOccurred())
	Expect(testEnv.Stop()).NotTo(HaveOccurred())
})

func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// func randomPort() (int, error) {
// 	lis, err := net.Listen("tcp", "127.0.0.1:0")
// 	if err != nil {
// 		return 0, err
// 	}
// 	defer lis.Close()
// 	return lis.Addr().(*net.TCPAddr).Port, nil
// }

func waitForPort(ctx context.Context, addr string) error {
	for {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}
