# InstanceHA Application Credential Authentication

By default InstanceHA authenticates to OpenStack using username/password
credentials from `clouds.yaml` and `secure.yaml`. As an alternative you can
use a **Keystone Application Credential**, which provides scoped, time-limited
access that can be rotated independently of the service user password.

## Prerequisites

- A Keystone user with sufficient privileges to query and evacuate VMs
  (typically the `admin` role on the relevant project).
- The `keystone-operator` deployed with Application Credential support
  ([keystone-operator PR #567](https://github.com/openstack-k8s-operators/keystone-operator/pull/567)).
- The `clouds.yaml` ConfigMap must still be present — InstanceHA reads
  `auth_url` and `region_name` from it even when using Application Credentials.

## Step 1 — Create a KeystoneApplicationCredential CR

Create a `KeystoneApplicationCredential` custom resource. The keystone-operator
will create the credential in Keystone and store the resulting ID and secret in
a Kubernetes Secret.

```yaml
apiVersion: keystone.openstack.org/v1beta1
kind: KeystoneApplicationCredential
metadata:
  name: ac-instanceha
  namespace: openstack
spec:
  # Kubernetes Secret holding the service user password
  secret: osp-secret
  passwordSelector: InstanceHaPassword

  # Keystone user that owns the Application Credential
  userName: instanceha

  # Roles granted to the credential
  roles:
    - admin

  # Optional: restrict API access to only what InstanceHA needs
  accessRules:
    - service: compute
      path: "/**"
      method: GET
    - service: compute
      path: "/servers/*/action"
      method: POST
    - service: compute
      path: "/os-services/**"
      method: PUT
    - service: compute
      path: "/os-services/**"
      method: GET
    - service: compute
      path: "/os-hypervisors/**"
      method: GET
    - service: compute
      path: "/os-aggregates/**"
      method: GET
    - service: compute
      path: "/flavors/**"
      method: GET
    - service: image
      path: "/v2/images/**"
      method: GET

  # Credential lifetime and rotation
  expirationDays: 365
  gracePeriodDays: 182
```

Once the CR reaches `Ready`, the operator creates a Secret named
`ac-instanceha-secret` (convention: `ac-<cr-name>-secret`) containing two keys:

| Key         | Description                                |
|-------------|--------------------------------------------|
| `AC_ID`     | Application Credential ID                  |
| `AC_SECRET` | Application Credential secret              |

Verify it:

```bash
oc get keystoneapplicationcredential ac-instanceha -o jsonpath='{.status.conditions[0].status}'
# True

oc get secret ac-instanceha-secret -o jsonpath='{.data}' | jq
```

## Step 2 — Configure the InstanceHa CR

Set `spec.auth.applicationCredentialSecret` to the name of the Secret created
above:

```yaml
apiVersion: instanceha.openstack.org/v1beta1
kind: InstanceHa
metadata:
  name: instanceha
  namespace: openstack
spec:
  # ... existing fields ...

  auth:
    applicationCredentialSecret: ac-instanceha-secret
```

The controller will:

1. Wait for the Secret to exist.
2. Hash its contents into the deployment's config hash (so the pod restarts
   when the credential rotates).
3. Mount the Secret at `/secrets/ac-credentials` inside the container.
4. Set the `AC_ENABLED=True` environment variable.

The InstanceHA Python process detects `AC_ENABLED`, reads `AC_ID` and
`AC_SECRET` from the mounted files, and authenticates using the
`v3applicationcredential` keystoneauth plugin. If the credential files are
missing or empty it falls back to password authentication.

## Step 3 — Verify

Check that the pod has the AC volume mounted:

```bash
oc get deployment instanceha -o jsonpath='{.spec.template.spec.volumes[?(@.name=="ac-credentials")]}'
```

Check the pod logs for the AC login message:

```bash
oc logs deployment/instanceha | grep "application credential"
# Nova login successful (application credential)
```

## Disabling Application Credential auth

Remove the `auth` section (or set `applicationCredentialSecret` to an empty
string) and the controller falls back to password-based authentication:

```yaml
spec:
  auth: {}
```

You can then delete the `KeystoneApplicationCredential` CR if it is no longer
needed:

```bash
oc delete keystoneapplicationcredential ac-instanceha
```

## Required roles and access rules

InstanceHA needs to:

| Operation                         | API                                    | Minimum role |
|-----------------------------------|----------------------------------------|--------------|
| List/show compute services        | `GET /os-services`                     | `admin`      |
| Enable/disable compute services   | `PUT /os-services/*`                   | `admin`      |
| List/show hypervisors             | `GET /os-hypervisors`                  | `admin`      |
| List/show aggregates              | `GET /os-aggregates`                   | `admin`      |
| List/show servers (all projects)  | `GET /servers` with `all_tenants=True` | `admin`      |
| Evacuate servers                  | `POST /servers/*/action` (evacuate)    | `admin`      |
| List/show flavors                 | `GET /flavors`                         | `admin`      |
| List/show images (Glance)         | `GET /v2/images`                       | `admin`      |

If you omit `accessRules` from the `KeystoneApplicationCredential` spec, the
credential inherits all permissions of its parent user — simpler but less
restrictive.

## Credential rotation

The keystone-operator handles rotation automatically based on `expirationDays`
and `gracePeriodDays`. When a credential is rotated:

1. The operator updates the `ac-instanceha-secret` Secret with new `AC_ID` and
   `AC_SECRET` values.
2. The InstanceHA controller detects the Secret hash change and triggers a
   rolling restart of the pod.
3. The new pod picks up the rotated credentials.

No manual intervention is required.
