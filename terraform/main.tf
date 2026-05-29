resource "google_project_service" "services" {
  for_each = toset([
    "artifactregistry.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
  ])

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_artifact_registry_repository" "images" {
  project       = var.project_id
  location      = var.gcp_region
  repository_id = local.artifact_repository_id
  description   = "Container images for Cortex ${var.environment}."
  format        = "DOCKER"
  labels        = local.labels

  depends_on = [google_project_service.services]
}

resource "google_service_account" "runtime" {
  project      = var.project_id
  account_id   = local.runtime_service_account_id
  display_name = "Cortex runtime (${var.environment})"
}

resource "google_secret_manager_secret" "slack_signing" {
  project   = var.project_id
  secret_id = var.slack_signing_secret_id
  labels    = local.labels

  replication {
    auto {}
  }

  depends_on = [google_project_service.services]
}

resource "google_secret_manager_secret" "slack_bot_token" {
  project   = var.project_id
  secret_id = var.slack_bot_token_secret_id
  labels    = local.labels

  replication {
    auto {}
  }

  depends_on = [google_project_service.services]
}

resource "google_secret_manager_secret_iam_member" "runtime_slack_signing" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.slack_signing.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_secret_manager_secret_iam_member" "runtime_slack_bot_token" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.slack_bot_token.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime.email}"
}

resource "google_cloud_run_v2_service" "cortex" {
  project             = var.project_id
  name                = local.service_name
  location            = var.gcp_region
  ingress             = "INGRESS_TRAFFIC_ALL"
  deletion_protection = false
  labels              = local.labels

  template {
    service_account = google_service_account.runtime.email
    timeout         = "${var.request_timeout_seconds}s"

    scaling {
      min_instance_count = var.min_instance_count
      max_instance_count = var.max_instance_count
    }

    containers {
      image = var.container_image

      ports {
        container_port = var.grpc_port
      }

      env {
        name  = "CORTEX_HOST"
        value = "0.0.0.0"
      }

      env {
        name  = "CORTEX_PORT"
        value = tostring(var.grpc_port)
      }
    }
  }

  traffic {
    percent = 100
    type    = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
  }

  depends_on = [
    google_project_service.services,
    google_artifact_registry_repository.images,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  count = var.allow_unauthenticated ? 1 : 0

  project  = var.project_id
  location = google_cloud_run_v2_service.cortex.location
  name     = google_cloud_run_v2_service.cortex.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
