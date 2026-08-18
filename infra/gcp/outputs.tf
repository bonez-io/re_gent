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
    dev  = "gcloud compute start-iap-tunnel regent-dev 8080 --local-host-port=localhost:7654 --zone=${var.zone} --project=${var.project_id}"
    main = "gcloud compute start-iap-tunnel regent-main 8080 --local-host-port=localhost:7655 --zone=${var.zone} --project=${var.project_id}"
  }
}
