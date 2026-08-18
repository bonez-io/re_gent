# GCP deployment

This is the first production-shaped re_gent deployment: one VM with a reserved
static IPv4 for `dev`, one for `main`, and branch-driven GitHub Actions
deployment.

## Security boundary

- Each VM has its own reserved external IPv4. The address remains stable across
  VM stops and updates.
- Direct ingress to the combined UI/API entrypoint (`8080`) is restricted to
  the CIDRs in `public_access_source_ranges`. Never allow `0.0.0.0/0` before
  application authentication and TLS exist. The raw server container is never
  published.
- SSH and CI deployment remain IAP-only; port `22` is not publicly reachable.
- GitHub authenticates with Workload Identity Federation and short-lived OIDC
  credentials. There are no service-account keys or deploy secrets.
- Each VM has its own service account, data disk, daily snapshot policy, deploy
  identity, and branch-bound identity provider.
- Production deletion protection is enabled by default.

This boundary is intentional because `regent-server` does not yet implement
application authentication or tenancy. The direct endpoint is HTTP, not HTTPS,
and is suitable only for an explicitly allowlisted development network. A
public hostname must wait for the auth/security epic and an approved TLS design.

## One-time provisioning

Use a dedicated re_gent GCP project with billing enabled. Do not use an
unrelated product project.

```bash
gcloud auth login
gcloud auth application-default login
cd infra/gcp
./provision.sh YOUR_REGENT_PROJECT europe-west4 europe-west4-a YOUR.PUBLIC.IP/32
```

The script creates a versioned GCS Terraform state bucket, applies the stack,
and writes non-secret deployment identifiers to GitHub variables. Review the
Terraform plan before approving it.

Required local permissions include Project IAM Admin, Service Account Admin,
Workload Identity Pool Admin, Compute Admin, Artifact Registry Admin, Service
Usage Admin, Storage Admin, and IAP Admin. These are provisioning permissions,
not runtime permissions.

## Delivery flow

1. A push to `dev` or `main` runs Go and UI validation.
2. GitHub exchanges its OIDC token for a short-lived, branch-bound GCP identity.
3. The workflow publishes immutable server and web images tagged with the Git
   SHA to Artifact Registry, then boots the real two-container topology and
   smoke-tests UI, health, repository registration, and API routing.
4. It reaches only the matching VM through IAP, installs the deployment runner,
   and starts both containers.
5. `/healthz` must pass within 60 seconds. A failure automatically restores the
   prior image pair and fails the workflow.

`dev` uses the GitHub `development` environment and `main` uses `production`.
Protect `main` and require Shay's PR review before merge; production then
deploys exactly the reviewed merge commit.

## Access

Terraform prints the stable URLs after apply:

```bash
terraform -chdir=infra/gcp output access
```

Only callers whose public source IP matches the Terraform allowlist can connect.
To change it, apply the complete reviewed set of CIDRs:

```bash
terraform -chdir=infra/gcp apply \
  -var='project_id=PROJECT' \
  -var='public_access_source_ranges={dev=["203.0.113.10/32"],main=["203.0.113.10/32"]}'
```

Use a `/32` for each teammate's fixed public egress address. Never put API keys
or secrets in this variable; CIDRs are ordinary infrastructure metadata.
Operators additionally need IAP and OS Login permissions for SSH.

Point a repository at the development address:

```bash
rgt connect http://DEV_STATIC_IP:8080
```

## Operations

Inspect a release:

```bash
gcloud compute ssh regent-dev --tunnel-through-iap --zone=europe-west4-a --project=PROJECT \
  --command='sudo cat /var/lib/regent-deploy/current.env; sudo docker ps'
```

The canonical server data is mounted from the protected `regent-ENV-data`
disk at `/var/lib/regent/data`. Daily snapshots are retained for 14 days in dev
and 30 days in main. Destroying the Terraform stack deliberately fails while
the data disks are protected; removing that lifecycle protection must be a
reviewed, explicit change.

Before opening the allowlist beyond trusted development networks, implement
application authentication, HTTPS, rate limiting, and security monitoring.
