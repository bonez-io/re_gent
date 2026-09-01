locals {
  environments = {
    dev = {
      branch       = "dev"
      machine_type = var.machine_types["dev"]
      disk_size_gb = var.data_disk_sizes_gb["dev"]
    }
    main = {
      branch       = "main"
      machine_type = var.machine_types["main"]
      disk_size_gb = var.data_disk_sizes_gb["main"]
    }
  }

  public_access_environments = {
    for environment, config in local.environments : environment => config
    if length(lookup(var.public_access_source_ranges, environment, [])) > 0
  }

  required_services = toset([
    "artifactregistry.googleapis.com",
    "compute.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "iap.googleapis.com",
    "sts.googleapis.com",
  ])
}

resource "google_project_service" "required" {
  for_each           = local.required_services
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "regent" {
  name                    = "regent"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.required]
}

resource "google_compute_subnetwork" "regent" {
  name                     = "regent-${var.region}"
  region                   = var.region
  network                  = google_compute_network.regent.id
  ip_cidr_range            = "10.30.0.0/24"
  private_ip_google_access = true
}

resource "google_compute_router" "regent" {
  name    = "regent-${var.region}"
  region  = var.region
  network = google_compute_network.regent.id
}

resource "google_compute_router_nat" "regent" {
  name                               = "regent-${var.region}"
  router                             = google_compute_router.regent.name
  region                             = var.region
  nat_ip_allocate_option             = "AUTO_ONLY"
  source_subnetwork_ip_ranges_to_nat = "LIST_OF_SUBNETWORKS"

  subnetwork {
    name                    = google_compute_subnetwork.regent.id
    source_ip_ranges_to_nat = ["ALL_IP_RANGES"]
  }

  log_config {
    enable = true
    filter = "ERRORS_ONLY"
  }
}

resource "google_compute_firewall" "iap" {
  name          = "regent-allow-iap"
  network       = google_compute_network.regent.name
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["regent-iap"]

  allow {
    protocol = "tcp"
    ports    = ["22", "8080"]
  }
}

# The application has no authentication yet, so a reserved address does not
# imply internet-wide ingress. Only explicitly approved source CIDRs can reach
# the combined UI/API port. SSH and CI deployment continue to use IAP.
resource "google_compute_firewall" "public_ui_api" {
  for_each      = local.public_access_environments
  name          = "regent-${each.key}-allow-public-ui-api"
  network       = google_compute_network.regent.name
  direction     = "INGRESS"
  source_ranges = var.public_access_source_ranges[each.key]
  target_tags   = ["regent-public-${each.key}"]

  allow {
    protocol = "tcp"
    ports    = ["8080"]
  }
}

resource "google_compute_address" "public" {
  for_each     = local.environments
  name         = "regent-${each.key}-public"
  region       = var.region
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"

  depends_on = [google_project_service.required]
}

resource "google_artifact_registry_repository" "regent" {
  location      = var.region
  repository_id = "regent"
  description   = "Immutable re_gent server and UI deployment images"
  format        = "DOCKER"
  depends_on    = [google_project_service.required]

  cleanup_policies {
    id     = "keep-recent"
    action = "KEEP"
    most_recent_versions {
      keep_count = 30
    }
  }

  cleanup_policies {
    id     = "delete-old"
    action = "DELETE"
    condition {
      tag_state  = "ANY"
      older_than = "2592000s"
    }
  }
}

resource "google_service_account" "vm" {
  for_each     = local.environments
  account_id   = "regent-${each.key}-vm"
  display_name = "re_gent ${each.key} VM"
}

resource "google_artifact_registry_repository_iam_member" "vm_reader" {
  for_each   = local.environments
  location   = google_artifact_registry_repository.regent.location
  repository = google_artifact_registry_repository.regent.name
  role       = "roles/artifactregistry.reader"
  member     = "serviceAccount:${google_service_account.vm[each.key].email}"
}

resource "google_compute_disk" "data" {
  for_each = local.environments
  name     = "regent-${each.key}-data"
  type     = "pd-balanced"
  zone     = var.zone
  size     = each.value.disk_size_gb
  labels   = { app = "regent", environment = each.key }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_compute_resource_policy" "snapshots" {
  for_each = local.environments
  name     = "regent-${each.key}-daily-snapshots"
  region   = var.region

  snapshot_schedule_policy {
    schedule {
      daily_schedule {
        days_in_cycle = 1
        start_time    = each.key == "main" ? "01:00" : "03:00"
      }
    }
    retention_policy {
      max_retention_days    = each.key == "main" ? 30 : 14
      on_source_disk_delete = "KEEP_AUTO_SNAPSHOTS"
    }
    snapshot_properties {
      storage_locations = [var.region]
      guest_flush       = false
      labels            = { app = "regent", environment = each.key }
    }
  }
}

resource "google_compute_disk_resource_policy_attachment" "snapshots" {
  for_each = local.environments
  name     = google_compute_resource_policy.snapshots[each.key].name
  disk     = google_compute_disk.data[each.key].name
  zone     = var.zone
}

resource "google_compute_instance" "regent" {
  for_each                  = local.environments
  name                      = "regent-${each.key}"
  zone                      = var.zone
  machine_type              = each.value.machine_type
  allow_stopping_for_update = true
  deletion_protection       = each.key == "main" && var.production_deletion_protection
  tags                      = ["regent-iap", "regent-public-${each.key}"]
  labels                    = { app = "regent", environment = each.key }

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = 15
      type  = "pd-balanced"
    }
  }

  attached_disk {
    source      = google_compute_disk.data[each.key].id
    device_name = "regent-data"
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = google_compute_subnetwork.regent.id

    access_config {
      nat_ip       = google_compute_address.public[each.key].address
      network_tier = google_compute_address.public[each.key].network_tier
    }
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
    serial-port-enable     = "FALSE"
    startup-script         = templatefile("${path.module}/startup.sh.tftpl", { environment = each.key })
  }

  service_account {
    email  = google_service_account.vm[each.key].email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  depends_on = [
    google_compute_router_nat.regent,
    google_compute_disk_resource_policy_attachment.snapshots,
    google_artifact_registry_repository_iam_member.vm_reader,
  ]
}

# GitHub Actions uses short-lived OIDC credentials. No service-account key is
# created or stored in GitHub.
resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "regent-github"
  display_name              = "re_gent GitHub Actions"
  depends_on                = [google_project_service.required]
}

resource "google_iam_workload_identity_pool_provider" "build" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "build"
  display_name                       = "Build dev and main"
  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
  }
  attribute_condition = "assertion.repository == '${var.github_repository}' && (assertion.ref == 'refs/heads/dev' || assertion.ref == 'refs/heads/main')"
  oidc { issuer_uri = "https://token.actions.githubusercontent.com" }
}

resource "google_iam_workload_identity_pool_provider" "deploy" {
  for_each                           = local.environments
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "deploy-${each.key}"
  display_name                       = "Deploy ${each.key}"
  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
  }
  attribute_condition = "assertion.repository == '${var.github_repository}' && assertion.ref == 'refs/heads/${each.value.branch}'"
  oidc { issuer_uri = "https://token.actions.githubusercontent.com" }
}

resource "google_service_account" "github_build" {
  account_id   = "regent-github-build"
  display_name = "re_gent GitHub image builder"
}

resource "google_service_account" "github_deploy" {
  for_each     = local.environments
  account_id   = "regent-${each.key}-deploy"
  display_name = "re_gent GitHub ${each.key} deployer"
}

locals {
  github_repo_principal = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_repository}"
}

resource "google_service_account_iam_member" "github_build_identity" {
  service_account_id = google_service_account.github_build.name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.github_repo_principal
}

resource "google_artifact_registry_repository_iam_member" "github_writer" {
  location   = google_artifact_registry_repository.regent.location
  repository = google_artifact_registry_repository.regent.name
  role       = "roles/artifactregistry.writer"
  member     = "serviceAccount:${google_service_account.github_build.email}"
}

resource "google_service_account_iam_member" "github_deploy_identity" {
  for_each           = local.environments
  service_account_id = google_service_account.github_deploy[each.key].name
  role               = "roles/iam.workloadIdentityUser"
  member             = local.github_repo_principal
}

# Compute checks iam.serviceAccounts.actAs on an instance's attached service
# account before opening SSH, even when OS Login and IAP are already authorized.
# Keep this environment-matched so a dev deploy identity cannot act as main.
resource "google_service_account_iam_member" "github_deploy_act_as_vm" {
  for_each           = local.environments
  service_account_id = google_service_account.vm[each.key].name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.github_deploy[each.key].email}"
}

resource "google_project_iam_member" "deploy_viewer" {
  for_each = local.environments
  project  = var.project_id
  role     = "roles/compute.viewer"
  member   = "serviceAccount:${google_service_account.github_deploy[each.key].email}"
}

resource "google_compute_instance_iam_member" "deploy_os_admin" {
  for_each      = local.environments
  project       = var.project_id
  zone          = var.zone
  instance_name = google_compute_instance.regent[each.key].name
  role          = "roles/compute.osAdminLogin"
  member        = "serviceAccount:${google_service_account.github_deploy[each.key].email}"
}

resource "google_iap_tunnel_instance_iam_member" "deploy_iap" {
  for_each = local.environments
  project  = var.project_id
  zone     = var.zone
  instance = google_compute_instance.regent[each.key].name
  role     = "roles/iap.tunnelResourceAccessor"
  member   = "serviceAccount:${google_service_account.github_deploy[each.key].email}"
}
