#!/usr/bin/env bash
set -Eeuo pipefail

command -v gh >/dev/null
command -v terraform >/dev/null
command -v jq >/dev/null

repo=${GITHUB_REPOSITORY:-bonez-io/re_gent}
infra_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
output=$(terraform -chdir="$infra_dir" output -json github)

value() { jq -er ".$1" <<<"$output"; }

configure_environment() {
  local environment=$1 branch=$2
  gh api --method PUT "repos/${repo}/environments/${environment}" \
    --input - >/dev/null <<EOF
{"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}
EOF
  if ! gh api "repos/${repo}/environments/${environment}/deployment-branch-policies" \
    --jq ".branch_policies[] | select(.name == \"${branch}\") | .name" | grep -qx "$branch"; then
    gh api --method POST "repos/${repo}/environments/${environment}/deployment-branch-policies" \
      -f "name=${branch}" >/dev/null
  fi
}

configure_environment development dev
configure_environment production main

gh variable set GCP_PROJECT_ID --repo "$repo" --body "$(value project_id)"
gh variable set GCP_REGION --repo "$repo" --body "$(value region)"
gh variable set GCP_ZONE --repo "$repo" --body "$(value zone)"
gh variable set GCP_ARTIFACT_REPOSITORY --repo "$repo" --body "$(value artifact_repository)"
gh variable set GCP_BUILD_IDENTITY_PROVIDER --repo "$repo" --body "$(value build_identity_provider)"
gh variable set GCP_BUILD_SERVICE_ACCOUNT --repo "$repo" --body "$(value build_service_account)"

gh variable set GCP_DEPLOY_IDENTITY_PROVIDER --repo "$repo" --env development --body "$(value dev_deploy_identity_provider)"
gh variable set GCP_DEPLOY_SERVICE_ACCOUNT --repo "$repo" --env development --body "$(value dev_deploy_service_account)"
gh variable set GCP_INSTANCE --repo "$repo" --env development --body "$(value dev_instance)"

gh variable set GCP_DEPLOY_IDENTITY_PROVIDER --repo "$repo" --env production --body "$(value main_deploy_identity_provider)"
gh variable set GCP_DEPLOY_SERVICE_ACCOUNT --repo "$repo" --env production --body "$(value main_deploy_service_account)"
gh variable set GCP_INSTANCE --repo "$repo" --env production --body "$(value main_instance)"

# Environment names are durable deployment records. The OIDC providers also
# enforce the branch boundary in GCP, so a modified workflow on another branch
# cannot obtain either deploy identity.
echo "Configured GitHub repository and environment variables for $repo."
