package controllers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
)

var _ = Describe("BucketProvider controller", Ordered, func() {

	It("Fails to create provider when secret is missing", func() {
		By("Creating BucketProvider with missing secret")
		err := k8sClient.Create(ctx, provider)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("secret not found"))
	})

	It("Fails when accesskey is not in secret", func() {
		By("Creating secret")
		err := k8sClient.Create(ctx, secret.DeepCopy())
		Expect(err).ToNot(HaveOccurred())
		By("Creating BucketProvider with missing accesskey in secret")
		provider := provider.DeepCopy()
		provider.Spec.AccessKey.Key = "noop"
		err = k8sClient.Create(ctx, provider)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.accessKey.key: Invalid value: \"noop\": key not found in secret"))
	})

	It("Fails when secretkey is not in secret", func() {
		By("Creating BucketProvider with missing secretkey in secret")
		provider := provider.DeepCopy()
		provider.Spec.SecretKey.Key = "noop"
		err := k8sClient.Create(ctx, provider)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.secretKey.key: Invalid value: \"noop\": key not found in secret"))
	})

	It("Successfully creates BucketProvider when secret is valid", func() {
		By("Creating BucketProvider with valid secret")
		err := k8sClient.Create(ctx, provider.DeepCopy())
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) {
			prov := provider.DeepCopy()
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prov), prov)).To(Succeed())
			cond := meta.FindStatusCondition(prov.Status.Conditions, s3v1alpha1.ProviderConditionReady)
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	AfterAll(func() {
		By("Cleaning up resources")
		Expect(k8sClient.Delete(ctx, provider)).ToNot(HaveOccurred())
		Expect(k8sClient.Delete(ctx, secret)).ToNot(HaveOccurred())
	})
})
