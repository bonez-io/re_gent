variable "project_id" {
  description = "Dedicated GCP project for the re_gent dev and production hosts."
  type        = string
}
variable "region" {
  description = "Region for compute, disks, snapshots, and Artifact Registry."
  type        = string
  default     = "europe-west4"
}

variable "zone" {
  description = "Zone for both first-MVP hosts."
  type        = string
  default     = "europe-west4-a"
}

variable "github_repository" {
  description = "GitHub owner/repository trusted by Workload Identity Federation."
  type        = string
  default     = "regent-vcs/re_gent_headless"
}

variable "machine_types" {
  description = "Machine type per environment."
  type        = map(string)
  default = {
    dev  = "e2-small"
    main = "e2-medium"
  }
}

variable "data_disk_sizes_gb" {
  description = "Persistent balanced-disk size per environment."
  type        = map(number)
  default = {
    dev  = 20
    main = 50
  }
}

variable "production_deletion_protection" {
  description = "Protect the main VM against accidental deletion."
  type        = bool
  default     = true
}

variable "public_access_source_ranges" {
  description = "Approved source CIDRs allowed to reach each environment on TCP 8080. Empty lists reserve static IPs without exposing the application."
  type        = map(list(string))
  default = {
    dev  = []
    main = []
  }

  validation {
    condition = alltrue([
      for environment in ["dev", "main"] : can(var.public_access_source_ranges[environment])
    ])
    error_message = "public_access_source_ranges must define both dev and main."
  }
}
