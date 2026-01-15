package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
)

var _ = Describe("Bucket controller", func() {
	BeforeEach(func() {
		By("Creating BucketProvider and secret for tests")
		Expect(k8sClient.Create(ctx, secret.DeepCopy())).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, provider.DeepCopy())).NotTo(HaveOccurred())
	})

	It("Fails to create Bucket when provider does not exist", func() {
		By("Creating Bucket with non-existing provider")
		err := k8sClient.Create(ctx, &s3v1alpha1.Bucket{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      "test-bucket-no-provider",
				Namespace: "default",
			},
			Spec: s3v1alpha1.BucketSpec{
				Provider: "non-existing-provider",
			},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})

	It("Successfully creates Bucket when provider exists", func() {
		By("Creating Bucket with existing provider")
		err := k8sClient.Create(ctx, &s3v1alpha1.Bucket{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      "test-bucket",
				Namespace: "default",
			},
			Spec: s3v1alpha1.BucketSpec{
				Provider: provider.Name,
			},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("Uses default provider when none is specified", func() {
		By("Annotating default BucketProvider")
		Eventually(func(g Gomega) {
			provider := provider.DeepCopy()
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), provider)).NotTo(HaveOccurred())
			provider.Annotations = map[string]string{
				s3v1alpha1.DefaultProviderAnnotation: "",
			}
			g.Expect(k8sClient.Update(ctx, provider)).To(Succeed())
		}).Should(Succeed())

		By("Creating Bucket without specifying provider")
		err := k8sClient.Create(ctx, &s3v1alpha1.Bucket{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      "test-bucket-default-provider",
				Namespace: "default",
			},
			Spec: s3v1alpha1.BucketSpec{},
		})
		Expect(err).NotTo(HaveOccurred())
		By("Verifying that the default provider was assigned")
		k := &client.ObjectKey{Namespace: "default", Name: "test-bucket-default-provider"}
		bucket := &s3v1alpha1.Bucket{}
		Expect(k8sClient.Get(ctx, *k, bucket)).NotTo(HaveOccurred())
		Expect(bucket.Spec.Provider).To(Equal(provider.Name))
	})

	It("Creates Bucket access secret when not specified", func() {
		By("Creating Bucket without specifying secret name")
		bucket := &s3v1alpha1.Bucket{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      "test-bucket-secret",
				Namespace: "default",
			},
			Spec: s3v1alpha1.BucketSpec{
				Provider: provider.Name,
			},
		}
		err := k8sClient.Create(ctx, bucket)
		Expect(err).NotTo(HaveOccurred())
		By("Verifying that the secret was created")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(bucket), bucket)).NotTo(HaveOccurred())
			g.Expect(bucket.Spec.SecretName).To(Not(BeNil()))
			g.Expect(*bucket.Spec.SecretName).To(Equal("test-bucket-secret-bucket-credentials"))
		}).Should(Succeed())
	})

	It("Reconciles Bucket to Ready state", func() {
		By("Creating Bucket")
		bucket := &s3v1alpha1.Bucket{
			ObjectMeta: ctrl.ObjectMeta{
				Name:      "test-bucket-reconcile",
				Namespace: "default",
			},
			Spec: s3v1alpha1.BucketSpec{
				Provider: provider.Name,
			},
		}
		err := k8sClient.Create(ctx, bucket)
		Expect(err).NotTo(HaveOccurred())

		By("Verifying that the Bucket reaches Creating state")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(bucket), bucket)).NotTo(HaveOccurred())
			g.Expect(bucket.Status.Conditions).NotTo(BeEmpty())
			condition := meta.FindStatusCondition(bucket.Status.Conditions, s3v1alpha1.BucketConditionCreating)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		}).Should(Succeed())

		By("Verifying that the Bucket reaches Ready state")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(bucket), bucket)).NotTo(HaveOccurred())
			g.Expect(bucket.Status.Conditions).NotTo(BeEmpty())
			condition := meta.FindStatusCondition(bucket.Status.Conditions, s3v1alpha1.BucketConditionReady)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		}, 10*time.Second).Should(Succeed())

		By("Verifying that the access secret is created")
		Expect(bucket.Status.SecretName).NotTo(BeNil())
		Expect(bucket.Status.Phase).To(Equal(s3v1alpha1.BucketConditionReady))
		k := &client.ObjectKey{Namespace: bucket.Namespace, Name: *bucket.Status.SecretName}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, *k, secret)).NotTo(HaveOccurred())
		Expect(secret.Type).To(Equal(corev1.SecretType(s3v1alpha1.BucketAccessSecretType)))
		Expect(secret.Data[s3v1alpha1.MinioEndpoint]).To(Equal([]byte(provider.Spec.Endpoint)))
		Expect(secret.Data[s3v1alpha1.MinioBucket]).To(Equal([]byte(bucket.Name)))
		Expect(secret.Data[s3v1alpha1.MinioSecure]).To(Equal([]byte("false")))
		Expect(secret.Data[s3v1alpha1.MinioAccessKey]).To(Not(BeEmpty()))
		Expect(secret.Data[s3v1alpha1.MinioSecretKey]).To(Not(BeEmpty()))

		By("Verifying that the service account is created")
		sa := &s3v1alpha1.BucketServiceAccount{}
		saKey := &client.ObjectKey{Namespace: bucket.Namespace, Name: bucket.Spec.ServiceAccount}
		Expect(k8sClient.Get(ctx, *saKey, sa)).NotTo(HaveOccurred())
		Expect(sa.Spec.Provider).To(Equal(provider.Name))
	})

	AfterEach(func() {
		By("Cleaning up resources")
		Expect(k8sClient.Delete(ctx, provider)).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, secret)).NotTo(HaveOccurred())
	})
})
