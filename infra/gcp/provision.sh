#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $# -lt 1 || $# -gt 4 ]]; then
  echo "usage: $0 <gcp-project-id> [region] [zone] [public-source-cidr]" >&2
  exit 2
fi

project_id=$1
region=${2:-europe-west4}
zone=${3:-europe-west4-a}
public_source_cidr=${4:-}

if [[ ! $project_id =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]]; then
  echo "invalid GCP project id: $project_id" >&2
  exit 2
fi
if [[ ! $region =~ ^[a-z]+-[a-z]+[0-9]+$ || ! $zone =~ ^${region}-[a-z]$ ]]; then
  echo "invalid region/zone pair: $region / $zone" >&2
  exit 2
fi
if [[ -n $public_source_cidr && ! $public_source_cidr =~ ^[0-9a-fA-F:.]+/[0-9]{1,3}$ ]]; then
  echo "invalid public source CIDR: $public_source_cidr" >&2
  exit 2
fi

command -v gcloud >/dev/null
command -v terraform >/dev/null
command -v gh >/dev/null
command -v jq >/dev/null

repo_root=$(git rev-parse --show-toplevel)
infra_dir="$repo_root/infra/gcp"
state_bucket="${project_id}-regent-tfstate"

gcloud projects describe "$project_id" >/dev/null
gcloud services enable \
  cloudresourcemanager.googleapis.com \
  serviceusage.googleapis.com \
  storage.googleapis.com \
  --project "$project_id"

if ! gcloud storage buckets describe "gs://${state_bucket}" --project "$project_id" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${state_bucket}" \
    --project "$project_id" \
    --location "$region" \
    --uniform-bucket-level-access
fi
gcloud storage buckets update "gs://${state_bucket}" --versioning

terraform -chdir="$infra_dir" init -reconfigure \
  -backend-config="bucket=${state_bucket}" \
  -backend-config="prefix=platform"
apply_args=(
  -var="project_id=${project_id}"
  -var="region=${region}"
  -var="zone=${zone}"
)
if [[ -n $public_source_cidr ]]; then
  public_access=$(jq -cn --arg cidr "$public_source_cidr" '{dev:[$cidr],main:[$cidr]}')
  apply_args+=("-var=public_access_source_ranges=${public_access}")
fi
terraform -chdir="$infra_dir" apply "${apply_args[@]}"

"$infra_dir/configure-github.sh"

echo
echo "Infrastructure is ready. Push this branch and merge it into dev to perform the first dev deployment."
terraform -chdir="$infra_dir" output access
