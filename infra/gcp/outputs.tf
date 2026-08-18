output "github" {
  value = {
    project_id                    = var.project_id
    region                        = var.region
    zone                          = var.zone
    artifact_repository           = google_artifact_registry_repository.regent.repository_id
    build_identity_provider       = google_iam_workload_identity_pool_provider.build.name
    build_service_account         = google_service_account.github_build.email
    dev_deploy_identity_provider  = google_iam_workload_identity_pool_provider.deploy["dev"].name
    dev_deploy_service_account    = google_service_account.github_deploy["dev"].email
    main_deploy_identity_provider = google_iam_workload_identity_pool_provider.deploy["main"].name
    main_deploy_service_account   = google_service_account.github_deploy["main"].email
    dev_instance                  = google_compute_instance.regent["dev"].name
    main_instance                 = google_compute_instance.regent["main"].name
  }
}
output "access" {
  value = {
    dev  = "http://${google_compute_address.public["dev"].address}:8080"
    main = "http://${google_compute_address.public["main"].address}:8080"
  }
}

output "public_ips" {
  value = {
    dev  = google_compute_address.public["dev"].address
    main = google_compute_address.public["main"].address
  }
}
