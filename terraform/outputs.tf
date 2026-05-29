output "project_id" {
  description = "Current GCP project ID."
  value       = data.google_project.current.project_id
}

output "project_number" {
  description = "Current GCP project number."
  value       = data.google_project.current.number
}

output "current_region" {
  description = "Configured GCP region."
  value       = var.gcp_region
}

output "cloud_run_service_name" {
  description = "Cloud Run service name."
  value       = google_cloud_run_v2_service.cortex.name
}

output "cloud_run_service_url" {
  description = "Cloud Run service URL."
  value       = google_cloud_run_v2_service.cortex.uri
}

output "name_prefix" {
  description = "Common resource name prefix."
  value       = local.name_prefix
}

output "artifact_registry_repository" {
  description = "Artifact Registry repository resource name."
  value       = google_artifact_registry_repository.images.name
}

output "artifact_registry_repository_url" {
  description = "Artifact Registry repository URL prefix."
  value       = "${var.gcp_region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
}

output "runtime_service_account_email" {
  description = "Runtime service account used by Cloud Run."
  value       = google_service_account.runtime.email
}

output "slack_secret_ids" {
  description = "Secret Manager secret IDs reserved for Slack integration."
  value = {
    signing_secret = google_secret_manager_secret.slack_signing.secret_id
    bot_token      = google_secret_manager_secret.slack_bot_token.secret_id
  }
}
