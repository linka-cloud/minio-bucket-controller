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
```

```
Access Key: <ACCESS_KEY>
Secret Key: <SECRET_KEY>
```

Export the credentials as environment variables:

```sh
export MINIO_ACCESS_KEY="<ACCESS_KEY>"
export MINIO_SECRET_KEY="<SECRET_KEY>"
export MINIO_ENDPOINT="myminio:9000"
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
```

Then assign the policy to the service account:

```sh
mc admin user svcacct edit myminio <ACCESS_KEY> --policy policy.json
```

### Running on the cluster

#### Install Custom Resources and deploy the controller

```sh
kubectl apply -f https://raw.githubusercontent.com/linka-cloud/minio-bucket-controller/main/deploy/manifests.yaml
```

The controller should soon be running:

```sh
kubectl get po -n minio-bucket-controller-system 
```
```
NAME                                                          READY   STATUS    RESTARTS   AGE
minio-bucket-controller-controller-manager-857dd9d7ff-279n6   2/2     Running   0          12m
```

Create the BucketProvider and the secret containing the credentials:

```sh
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: minio-bucket-controller-credentials
  namespace: minio-bucket-controller-system
stringData:
  MINIO_ACCESS_KEY: $MINIO_ACCESS_KEY
  MINIO_SECRET_KEY: $MINIO_SECRET_KEY
---
apiVersion: s3.linka.cloud/v1alpha1
kind: BucketProvider
metadata:
  name: my-bucket-provider
  annotations:
    s3.linka.cloud/is-default-provider: ""
spec:
  endpoint: $MINIO_ENDPOINT
  # uncomment if you don't use tls
  # insecure: true
  accessKey:
    name: minio-bucket-controller-credentials
    namespace: minio-bucket-controller-system
    key: MINIO_ACCESS_KEY
  secretKey:
    name: minio-bucket-controller-credentials
    namespace: minio-bucket-controller-system
    key: MINIO_SECRET_KEY
EOF
```

#### Create a sample bucket and a sample application

We can now create a Bucket resource:

```sh
cat <<EOF | kubectl apply -f -
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
  namespace: default
spec:
  reclaimPolicy: Delete
  secretName: bucket-sample-creds
  secretTemplate:
    config.json: |
      {
        "version": "10",
        "aliases": {
          "minio": {
            "url": "http{{ if .Secure }}s{{ end }}://{{ .Endpoint }}",
            "accessKey": "{{ .AccessKey }}",
            "secretKey": "{{ .SecretKey }}",
            "api": "s3v4",
            "path": "auto"
          }
        }
      }
EOF
```

Validate that the bucket has been created:

```sh
kubectl get buckets -n default
```

```yaml
NAME            RECLAIM   STATUS   SECRET                AGE
bucket-sample   Delete    Ready    bucket-sample-creds   2s
```

And that the secret has been created:

```sh
kubectl describe secret -n default bucket-sample-creds
```

```
Name:         bucket-sample-creds
Namespace:    default
Labels:       <none>
Annotations:  <none>

Type:  Opaque

Data
====
config.json:       254 bytes
MINIO_ACCESS_KEY:  20 bytes
MINIO_BUCKET:      13 bytes
MINIO_ENDPOINT:    18 bytes
MINIO_SECRET_KEY:  40 bytes
MINIO_SECURE:      4 bytes
```

You can get more information about the bucket, like the created minio service account and the endpoint:

```sh
kubectl get buckets -n default -o wide
```

```
NAME            RECLAIM   STATUS   ENDPOINT             SERVICE ACCOUNT   SECRET                AGE
bucket-sample   Delete    Ready    myminio:9000         bucket-sample     bucket-sample-creds   6s
```

We can now create a sample application that uses the bucket.

The deployment uses the secret to configure the `mc` client and then runs a container that does nothing but keep the pod alive:

```sh
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bucket-sample-mc
  namespace: default
  labels:
    app: bucket-sample-mc
spec:
  replicas: 1
  selector:
    matchLabels:
      app: bucket-sample-mc
  template:
    metadata:
      name: bucket-sample-mc
      labels:
        app: bucket-sample-mc
    spec:
      containers:
      - name: mc
        image: minio/mc
        imagePullPolicy: IfNotPresent
        command:
        - /bin/sh
        args:
        - -c
        - tail -f /dev/null
        volumeMounts:
        - name: mc-config
          mountPath: /root/.mc/config.json
          subPath: config.json
      restartPolicy: Always
      volumes:
      - name: mc-config
        secret:
          secretName: bucket-sample-creds
EOF
```

It should soon be running:

```sh
kubectl get deployments.apps -n default bucket-sample-mc 
```

```
NAME               READY   UP-TO-DATE   AVAILABLE   AGE
bucket-sample-mc   1/1     1            1           3s   
```

You can now exec into the pod and run `mc` commands:

```sh
kubectl exec -n default -i -t deployments/bucket-sample-mc -- mc ls minio
```

```
mc: Successfully created `/root/.mc/share`.
mc: Initialized share uploads `/root/.mc/share/uploads.json` file.
mc: Initialized share downloads `/root/.mc/share/downloads.json` file.
[2023-12-07 18:01:12 UTC]     0B bucket-sample/
```

#### Cleanup

```sh
kubectl delete -n default deploy bucket-sample-mc
kubectl delete -n default bucket bucket-sample
```

### Uninstall

Always delete the Buckets before uninstalling the controller, or the buckets will be stuck in the **Deleting** state.

**Warning**: This will delete all the created buckets with `reclaimPolicy` set to `Delete`.

```sh
kubectl delete buckets.s3.linka.cloud --all --all-namespaces
kubectl delete -f https://raw.githubusercontent.com/linka-cloud/minio-bucket-controller/main/deploy/manifests.yaml
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

