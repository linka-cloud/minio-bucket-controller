# minio-bucket-controller

A simple controller to create buckets and corresponding users in MinIO.

**Note**: *The controller does not intend to be a replacement for the [COSI](https://container-object-storage-interface.github.io/) project.
It is not as structured and does not have the same level of abstraction as the COSI.
It is not meant to be a generic bucket controller, but rather a simple controller to create app buckets and users in MinIO.
It covers a specific need: manage application's MinIO buckets and access lifecycle inside Kubernetes*

## Description

This controller allows to manage buckets directly from Kubernetes:

```yaml
apiVersion: s3.linka.cloud/v1alpha1
kind: Bucket
metadata:
  labels:
    app.kubernetes.io/name: bucket
    app.kubernetes.io/instance: bucket-sample
    app.kubernetes.io/part-of: minio-bucket-controller
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/created-by: minio-bucket-controller
  name: bucket-sample
spec:
  reclaimPolicy: Delete
  secretName: bucket-sample-creds
```

When a new bucket resource is created, the corresponding minio bucket is created 
with a new user `bucket.s3.linka.cloud/bucket-sample` and an assigned policy that allows read/write access to the bucket only:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListAllMyBuckets",
        "s3:GetBucketLocation",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::${BUCKET}"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:*"
      ],
      "Resource": [
        "arn:aws:s3:::${BUCKET}/*"
      ]
    }
  ]
}
```

Then a service account is created for the user.
The service account credentials are stored in a secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ${BUCKET}-bucket-credentials
  namespace: $NAMESPACE
stringData:
  MINIO_ACCESS_KEY: $ACCESS_KEY
  MINIO_SECRET_KEY: $SECRET_KEY
  MINIO_ENDPOINT: $ENDPOINT
  MINIO_BUCKET: $BUCKET
  MINIO_SECURE: $SECURE
```



## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Preparation

#### Create a MinIO Service Account for the controller

You need to have a MinIO user with the right permissions (e.g. console admin) for the controller service account.

If you don't have one, you can create one with the following command:

```sh
mc admin user add myminio myminio-admin myminio-password
```

Assign the policy `consoleAdmin` to the user:

```sh
mc admin policy set myminio consoleAdmin user=myminio-admin
```

Create the controller service account:

```sh
mc admin user svcacct add myminio myminio-admin

Access Key: <ACCESS_KEY>
Secret Key: <SECRET_KEY>
```

Create a `policy.json` file with the service account IAM definition:

```shell
cat <<EOF > policy.json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "admin:CreateUser",
        "admin:DeleteUser",
        "admin:ListUsers",
        "admin:CreatePolicy",
        "admin:DeletePolicy",
        "admin:GetPolicy",
        "admin:ListUserPolicies",
        "admin:CreateServiceAccount",
        "admin:UpdateServiceAccount",
        "admin:RemoveServiceAccount",
        "admin:ListServiceAccounts"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:DeleteBucket",
        "s3:ForceDeleteBucket,
        "s3:ListAllMyBuckets"
      ],
      "Resource": [
        "arn:aws:s3:::*"
      ]
    }
  ]
}
EOF

Then assign the policy to the service account:

```sh
mc admin user svcacct edit myminio <ACCESS_KEY> --policy policy.json
```

### Running on the cluster

#### Install Custom Resources and deploy the controller

```sh
kubectl apply -f https://raw.githubusercontent.com/linka-cloud/minio-bucket-controller/main/manifests.yaml
```

The controller will not be created as it requires a secret named **minio-bucket-controller-credentials** with the MinIO credentials.

Then create a secret with the credentials:

```sh
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: minio-bucket-controller-credentials
  namespace: minio-bucket-controller-system
stringData:
  MINIO_ACCESS_KEY: <ACCESS_KEY>
  MINIO_SECRET_KEY: <SECRET_KEY>
  MINIO_ENDPOINT: myminio:9000
  # uncomment if you don't use tls
  # MINIO_INSECURE: "true"
EOF
```
## License

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

