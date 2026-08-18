# GCP deployment

This is the first production-shaped re_gent deployment: one private VM for
`dev`, one private VM for `main`, and branch-driven GitHub Actions deployment.

## Security boundary

- Neither VM has an external IP.
- Firewall ingress accepts only GCP IAP traffic on SSH (`22`) and the combined
  UI/API entrypoint (`8080`). The raw server container is never published.
- GitHub authenticates with Workload Identity Federation and short-lived OIDC
  credentials. There are no service-account keys or deploy secrets.
- Each VM has its own service account, data disk, daily snapshot policy, deploy
  identity, and branch-bound identity provider.
- Production deletion protection is enabled by default.

This boundary is intentional because `regent-server` does not yet implement
application authentication or tenancy. IAP grants access using GCP IAM. A
public hostname must wait for the auth/security epic or be placed behind an
approved HTTPS load balancer + IAP design.

## One-time provisioning

Use a dedicated re_gent GCP project with billing enabled. Do not use an
unrelated product project.

```bash
gcloud auth login
gcloud auth application-default login
cd infra/gcp
./provision.sh YOUR_REGENT_PROJECT europe-west4 europe-west4-a
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

The UI and API are accessed through a local IAP tunnel:

```bash
# Development: http://127.0.0.1:7654
gcloud compute start-iap-tunnel regent-dev 8080 \
  --local-host-port=localhost:7654 --zone=europe-west4-a --project=PROJECT

# Production: http://127.0.0.1:7655
gcloud compute start-iap-tunnel regent-main 8080 \
  --local-host-port=localhost:7655 --zone=europe-west4-a --project=PROJECT
```

Grant teammates `roles/iap.tunnelResourceAccessor` on only the instance they
need. UI users do not need OS Login; operators additionally need OS Login.

Point a repository at the tunnel while it is running:

```bash
rgt connect http://127.0.0.1:7654
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

The known external blockers are operational rather than code changes:

1. authenticate `gcloud` and select/create the dedicated re_gent project;
2. repair GitHub Actions billing, because GitHub currently refuses to start
   any jobs for this private repository;
3. restore a paid GitHub organization plan (or make the repository public) so
   private-branch protection and protected deployment environments can enforce
   the review policy. The organization currently reports the Free plan.
