variable "project_name" {
  description = "Project name used for naming and tagging."
  type        = string
  default     = "cortex"
}

variable "project_id" {
  description = "Google Cloud project ID that hosts Cortex."
  type        = string
}

variable "environment" {
  description = "Deployment environment name, such as dev, staging, or prod."
  type        = string
}

variable "gcp_region" {
  description = "Google Cloud region to deploy into."
  type        = string
  default     = "us-west1"
}

variable "service_name" {
  description = "Optional explicit Cloud Run service name. Defaults to the project and environment prefix."
  type        = string
  default     = null
}

variable "artifact_repository_id" {
  description = "Optional explicit Artifact Registry repository ID. Defaults to the project and environment prefix."
  type        = string
  default     = null
}

variable "container_image" {
  description = "Fully-qualified container image URL deployed to Cloud Run."
  type        = string
}

variable "grpc_port" {
  description = "Container port exposed by Cortex for gRPC traffic."
  type        = number
  default     = 8080
}

variable "request_timeout_seconds" {
  description = "Cloud Run request timeout in seconds for blocking gRPC calls."
  type        = number
  default     = 300

  validation {
    condition     = var.request_timeout_seconds >= 1 && var.request_timeout_seconds <= 3600
    error_message = "request_timeout_seconds must be between 1 and 3600 for Cloud Run."
  }
}

variable "min_instance_count" {
  description = "Minimum number of Cloud Run instances kept warm."
  type        = number
  default     = 0
}

variable "max_instance_count" {
  description = "Maximum number of Cloud Run instances allowed."
  type        = number
  default     = 1

  validation {
    condition     = var.max_instance_count >= var.min_instance_count
    error_message = "max_instance_count must be greater than or equal to min_instance_count."
  }
}

variable "allow_unauthenticated" {
  description = "Whether to grant public invoker access to the Cloud Run service."
  type        = bool
  default     = true
}

variable "slack_signing_secret_id" {
  description = "Secret Manager secret ID reserved for the Slack signing secret."
  type        = string
  default     = "cortex-slack-signing-secret"
}

variable "slack_bot_token_secret_id" {
  description = "Secret Manager secret ID reserved for the Slack bot token."
  type        = string
  default     = "cortex-slack-bot-token"
}

variable "gcp_labels" {
  description = "Additional labels applied to supported GCP resources. Keys and values should follow GCP label rules."
  type        = map(string)
  default     = {}
}
